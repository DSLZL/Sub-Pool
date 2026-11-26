package connection

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/audit"
	"sub-pool/backend/internal/auth"
)

type WalletReadSnapshot struct {
	ParticipantRevision string                 `json:"participant_revision"`
	UpstreamUserID      string                 `json:"upstream_user_id"`
	ReadAt              *time.Time             `json:"read_at"`
	Balance             WalletField            `json:"balance"`
	FrozenBalance       WalletField            `json:"frozen_balance"`
	Read                Check                  `json:"read"`
	FieldAuthority      Check                  `json:"field_authority"`
	Unit                string                 `json:"unit"`
	UsageSources        *WalletUsageSources    `json:"usage_sources,omitempty"`
	Simulation          *SimulationProvenance  `json:"simulation,omitempty"`
	SimulationScope     *simulationWalletScope `json:"simulation_scope,omitempty"`
}

type WalletRead struct {
	ID                 string    `json:"id"`
	ParticipantID      string    `json:"participant_id"`
	BindingID          string    `json:"binding_id"`
	SourceID           string    `json:"source_id"`
	ConnectionRevision string    `json:"connection_revision"`
	AttemptedAt        time.Time `json:"attempted_at"`
	WalletReadSnapshot
}

type WalletExpected struct {
	ParticipantID       string `json:"participant_id"`
	ParticipantRevision string `json:"participant_revision"`
	BindingID           string `json:"binding_id"`
	ConnectionRevision  string `json:"connection_revision"`
	SourceID            string `json:"source_id"`
	LatestID            string `json:"latest_id"`
}

func validWalletExpected(in WalletExpected) bool {
	return managedID(in.ParticipantID) && validRevision(in.ParticipantRevision) && validRevision(in.BindingID) && validRevision(in.ConnectionRevision) && managedID(in.SourceID) && validUsageAfter(in.LatestID)
}

// The lock only serializes our local receipts/explanations, never native wallet consumers.
func lockWallet(ctx context.Context, tx pgx.Tx, id string) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared($1)`, temporaryLock); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 1380991564))`, id)
	return err
}

func (s *Service) walletExpected(ctx context.Context, tx pgx.Tx, in WalletExpected) (storedConfig, Participant, error) {
	if err := lockWallet(ctx, tx, in.ParticipantID); err != nil {
		return storedConfig{}, Participant{}, err
	}
	return s.walletReferences(ctx, tx, in)
}

func (s *Service) walletReferences(ctx context.Context, tx pgx.Tx, in WalletExpected) (storedConfig, Participant, error) {
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return c, Participant{}, err
	}
	p, err := readParticipant(ctx, tx, in.ParticipantID, c, false, false)
	if err == nil && (p.Revision != in.ParticipantRevision || p.Binding.ID != in.BindingID || !p.SourceCurrent || p.Binding.EndedAt != nil || p.Binding.SourceID != in.SourceID || c.Revision != in.ConnectionRevision) {
		err = auth.ErrConflict
	}
	return c, p, err
}

const walletReadColumns = `id::text,participant_id,binding_id::text,source_id,connection_revision::text,attempted_at,snapshot`

func scanWalletRead(row pgx.Row) (WalletRead, error) {
	var out WalletRead
	var body []byte
	err := row.Scan(&out.ID, &out.ParticipantID, &out.BindingID, &out.SourceID, &out.ConnectionRevision, &out.AttemptedAt, &body)
	if err == nil {
		err = json.Unmarshal(body, &out.WalletReadSnapshot)
	}
	out.AttemptedAt, out.ReadAt = out.AttemptedAt.UTC(), utc(out.ReadAt)
	return out, err
}

func latestWalletRead(ctx context.Context, tx pgx.Tx, id, binding string, confirmed bool) (*WalletRead, error) {
	filter := ` AND snapshot->>'unit'='native_wallet'`
	if confirmed {
		filter += ` AND snapshot->'balance'->>'state'='reported' AND snapshot->'frozen_balance'->>'state'='reported'`
	}
	out, err := scanWalletRead(tx.QueryRow(ctx, `SELECT `+walletReadColumns+` FROM wallet_reads WHERE participant_id=$1 AND binding_id=$2`+filter+` ORDER BY wallet_reads.id DESC LIMIT 1`, id, binding))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &out, err
}

func (s *Service) RefreshWallet(ctx context.Context, token string, in WalletExpected) (WalletRead, error) {
	if !validWalletExpected(in) {
		return WalletRead{}, auth.ErrInput
	}
	tx, actor, err := s.begin(ctx, token, true)
	if err != nil {
		return WalletRead{}, err
	}
	defer tx.Rollback(ctx)
	c, p, err := s.walletExpected(ctx, tx, in)
	if err != nil {
		return WalletRead{}, err
	}
	latest, err := latestWalletRead(ctx, tx, p.ID, p.Binding.ID, false)
	if err != nil || in.LatestID != walletReadID(latest) {
		if err == nil {
			err = auth.ErrConflict
		}
		return WalletRead{}, err
	}
	kind, secret, err := s.decrypt(c)
	if err != nil {
		return WalletRead{}, err
	}
	if _, err = s.auth.AuthorizeAdmin(ctx, tx, token); err != nil {
		return WalletRead{}, err
	}
	out := newWalletRead(c, p)
	// Leave the handler time to persist the independent receipts and recheck authority.
	network, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out.FieldAuthority = s.nativeWalletAuthority(network, c, p, kind, secret)
	if _, err = s.auth.AuthorizeAdmin(ctx, tx, token); err != nil {
		return WalletRead{}, err
	}
	qualified := s.readNativeWalletProfile(network, c, p, kind, secret, &out)
	out.UsageSources, err = s.walletUsageSources(ctx, network, tx, token, c, p, kind, secret)
	cancel()
	if err != nil {
		return WalletRead{}, err
	}
	body, err := json.Marshal(out.WalletReadSnapshot)
	if err == nil {
		err = tx.QueryRow(ctx, `INSERT INTO wallet_reads(participant_id,binding_id,source_id,connection_revision,attempted_at,snapshot) VALUES($1,$2,$3,$4,$5,$6) RETURNING id::text`, p.ID, p.Binding.ID, p.Binding.SourceID, c.Revision, out.AttemptedAt, body).Scan(&out.ID)
	}
	if err == nil {
		outcome := "failed"
		if qualified && out.Balance.Value != nil && out.FrozenBalance.Value != nil {
			outcome = "succeeded"
		}
		_, err = audit.Record(ctx, tx, audit.Entry{ActorID: &actor.ID, Action: "wallet.read", Outcome: outcome, TargetType: "participant", TargetID: p.ID, Revision: &p.Revision, SourceID: &p.Binding.SourceID})
	}
	if err == nil {
		err = s.finish(ctx, tx, token, true)
	}
	if err != nil {
		return WalletRead{}, err
	}
	return out, nil
}

func newWalletRead(c storedConfig, p Participant) WalletRead {
	return WalletRead{
		ParticipantID: p.ID, BindingID: p.Binding.ID, SourceID: p.Binding.SourceID,
		ConnectionRevision: c.Revision, AttemptedAt: time.Now().UTC().Truncate(time.Microsecond),
		WalletReadSnapshot: WalletReadSnapshot{
			ParticipantRevision: p.Revision, UpstreamUserID: p.Binding.UpstreamUserID, Unit: "native_wallet",
			FieldAuthority: unknown("users.balance.read", "FIELD_PERMISSION_UNVERIFIED"),
		},
	}
}

func (s *Service) nativeWalletAuthority(ctx context.Context, c storedConfig, p Participant, kind, secret string) Check {
	if kind == "bearer" {
		body, check := s.upstream.get(ctx, c.BaseURL, "/api/v1/auth/me", kind, secret, "users.balance.read", true)
		if check.State == "available" && !walletAdmin(body) {
			return invalid(check)
		}
		return check
	}
	// The native key policy gates balance history separately from user-profile reads.
	body, check := s.upstream.get(ctx, c.BaseURL, "/api/v1/admin/users/"+p.Binding.UpstreamUserID+"/balance-history?page=1&page_size=1", kind, secret, "users.balance.read", true)
	if check.State == "available" && !walletBalanceAuthority(body) {
		return invalid(check)
	}
	return check
}

func (s *Service) readNativeWalletProfile(ctx context.Context, c storedConfig, p Participant, kind, secret string, out *WalletRead) bool {
	body, check := s.upstream.get(ctx, c.BaseURL, "/api/v1/admin/users/"+p.Binding.UpstreamUserID, kind, secret, "users.read", true)
	out.Read = check
	fields, valid := usageObject(body)
	if _, validIdentity := projectNativeUser(body, p.Binding.UpstreamUserID, secret); check.State == "available" && (!valid || !validIdentity) {
		out.Read = invalid(check)
	}
	qualified := out.Read.State == "available" && out.FieldAuthority.State == "available"
	out.Balance, out.FrozenBalance = walletField(fields["balance"], false, qualified), walletField(fields["frozen_balance"], true, qualified)
	if out.Read.State == "available" {
		now := time.Now().UTC()
		out.ReadAt = &now
	}
	return qualified
}

func walletReadID(read *WalletRead) string {
	if read == nil {
		return "0"
	}
	return read.ID
}
