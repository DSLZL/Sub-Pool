package connection

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/allocation"
	"sub-pool/backend/internal/audit"
	"sub-pool/backend/internal/auth"
	"sub-pool/backend/internal/estimation"
)

type walletContract struct {
	ID                 string  `json:"id"`
	SourceID           string  `json:"source_id"`
	ConnectionRevision string  `json:"connection_revision"`
	Enabled            bool    `json:"enabled"`
	BindingID          *string `json:"binding_id"`
	ShareUnits         *int64  `json:"share_units"`
}

type walletSource struct {
	UpstreamID          string            `json:"upstream_id"`
	MemberHead          string            `json:"member_head"`
	CollectionAvailable bool              `json:"collection_available"`
	AccountID           string            `json:"account_id"`
	Revision            string            `json:"revision"`
	SourceID            string            `json:"source_id"`
	SourceMode          string            `json:"source_mode"`
	IdentityState       string            `json:"identity_state"`
	ObservedConnection  string            `json:"observed_connection"`
	ReadState           string            `json:"read_state"`
	ContractHead        string            `json:"contract_head"`
	Contract            *walletContract   `json:"contract"`
	ObservationHead     string            `json:"observation_head"`
	CaptureHead         string            `json:"capture_head"`
	ManagementRevision  string            `json:"management_revision"`
	EstimateRevision    string            `json:"estimate_revision"`
	EstimateID          *string           `json:"estimate_id"`
	Estimate            *EstimateSnapshot `json:"estimate"`
}

type walletBasis struct {
	ParticipantRevision string         `json:"participant_revision"`
	BindingID           string         `json:"binding_id"`
	SourceID            string         `json:"source_id"`
	Enabled             bool           `json:"enabled"`
	CurrentSource       *string        `json:"current_source"`
	ConnectionRevision  string         `json:"connection_revision"`
	PolicyRevision      string         `json:"policy_revision"`
	StaleAfterSeconds   int            `json:"stale_after_seconds"`
	WalletHead          string         `json:"wallet_head"`
	Sources             []walletSource `json:"sources"`
}

// One statement captures all locally recorded sources before any reader projection.
const walletBasisSQL = `SELECT jsonb_build_object(
 'participant_revision',p.revision::text,'binding_id',b.id::text,'source_id',b.source_id,'enabled',p.enabled,
 'current_source',cfg.source_id,'connection_revision',cfg.revision::text,
 'policy_revision',m.revision::text,'stale_after_seconds',m.stale_after_seconds,
 'wallet_head',COALESCE((SELECT max(id) FROM wallet_reads WHERE participant_id=p.id AND binding_id=b.id),0)::text,
 'sources',COALESCE((SELECT jsonb_agg(v.body ORDER BY v.account_id) FROM (
 SELECT a.id AS account_id,jsonb_build_object('account_id',a.id,'upstream_id',a.upstream_id::text,'revision',a.revision::text,'source_id',a.source_id,
	'member_head',(SELECT COALESCE(jsonb_agg(jsonb_build_array(ref.participant_id,mp.revision::text,mp.enabled,mp.deleted_at,mb.id::text,mb.ended_at) ORDER BY ref.participant_id,ref.binding_id),'[]'::jsonb)::text FROM
	 (SELECT DISTINCT cp.participant_id,cp.binding_id FROM pool_contract_accounts ca JOIN pool_contract_participants cp ON cp.contract_id=ca.contract_id WHERE ca.account_id=a.id ORDER BY cp.participant_id,cp.binding_id LIMIT 501) ref
	 JOIN participants mp ON mp.id=ref.participant_id JOIN participant_bindings mb ON mb.id=ref.binding_id),
	'collection_available',COALESCE((SELECT state='succeeded' AND read_check->>'state'='available' FROM collection_jobs WHERE account_id=a.id AND started_at IS NOT NULL ORDER BY collection_jobs.id DESC LIMIT 1),false),
 'source_mode',a.source_mode,'identity_state',a.identity_state,'observed_connection',COALESCE(a.observed_connection_revision,0)::text,
 'read_state',COALESCE(a.read_check->>'state','unverified'),
 'contract_head',COALESCE((SELECT max(ca.contract_id) FROM pool_contract_accounts ca WHERE ca.account_id=a.id),0)::text,
 'contract',(SELECT jsonb_build_object('id',c.id::text,'source_id',c.source_id,'connection_revision',c.connection_revision::text,
     'enabled',c.enabled,'binding_id',cp.binding_id::text,'share_units',cp.share_units)
     FROM pool_contract_accounts ca JOIN pool_contracts c ON c.id=ca.contract_id
     LEFT JOIN pool_contract_participants cp ON cp.contract_id=c.id AND cp.participant_id=p.id
     WHERE ca.account_id=a.id AND ca.current),
 'observation_head',COALESCE((SELECT max(id) FROM quota_observations WHERE account_id=a.id),0)::text,
 'capture_head',COALESCE((SELECT max(id) FROM usage_captures WHERE account_id=a.id),0)::text,
 'management_revision',COALESCE((SELECT revision FROM observation_management WHERE account_id=a.id),1)::text,
 'estimate_revision',COALESCE((SELECT revision FROM estimate_config WHERE account_id=a.id),1)::text,
 'estimate_id',(SELECT id::text FROM estimate_results WHERE account_id=a.id ORDER BY estimate_results.id DESC LIMIT 1),
 'estimate',(SELECT snapshot FROM estimate_results WHERE account_id=a.id ORDER BY estimate_results.id DESC LIMIT 1)) AS body
 FROM managed_accounts a WHERE EXISTS(SELECT 1 FROM pool_contract_accounts ca JOIN pool_contract_participants cp ON cp.contract_id=ca.contract_id WHERE ca.account_id=a.id AND cp.participant_id=p.id)
 OR EXISTS(SELECT 1 FROM usage_attributions u JOIN usage_captures c ON c.id=u.capture_id WHERE u.participant_id=p.id AND c.account_id=a.id)
 OR EXISTS(SELECT 1 FROM usage_captures c JOIN usage_capture_facts f ON f.capture_id=c.id
     WHERE c.account_id=a.id AND c.snapshot->>'source_id'=b.source_id AND f.fact->>'user_id'=b.upstream_user_id::text)
 OR EXISTS(SELECT 1 FROM wallet_reads r, jsonb_array_elements(r.snapshot->'usage_sources'->'accounts') u
     WHERE r.source_id=b.source_id AND a.source_id=b.source_id AND r.snapshot->>'upstream_user_id'=b.upstream_user_id::text
     AND u->>'upstream_account_id'=a.upstream_id::text)
 OR EXISTS(SELECT 1 FROM wallet_reads r, jsonb_array_elements_text(r.snapshot->'simulation_scope'->'source_account_ids') u
     WHERE r.source_id=b.source_id AND a.source_id=b.source_id AND r.snapshot->>'upstream_user_id'=b.upstream_user_id::text AND u=a.upstream_id::text)
 ORDER BY a.id LIMIT 501) v),'[]'::jsonb))
 FROM participants p JOIN participant_bindings b ON b.participant_id=p.id AND b.ended_at IS NULL
 CROSS JOIN connection_config cfg CROSS JOIN monitoring_config m
 WHERE p.id=$1 AND p.deleted_at IS NULL AND cfg.singleton AND m.singleton`

type WalletContribution struct {
	AccountID              string                       `json:"account_id"`
	AccountRevision        string                       `json:"account_revision"`
	CurrentContractID      *string                      `json:"current_contract_id"`
	HistoricalContractHead string                       `json:"historical_contract_head"`
	SharePercent           *string                      `json:"share_percent"`
	EstimateID             *string                      `json:"estimate_id"`
	EstimateState          string                       `json:"estimate_state"`
	ObservationHead        string                       `json:"observation_head"`
	CaptureHead            string                       `json:"capture_head"`
	State                  string                       `json:"state"`
	Amount                 *estimation.Amount           `json:"amount"`
	Simulation             *SimulationProvenance        `json:"simulation,omitempty"`
	SimulationProof        *simulationWalletSourceProof `json:"simulation_proof,omitempty"`
}

type WalletGlobal struct {
	State       string   `json:"state"`
	SourceCount int      `json:"source_count"`
	Limitations []string `json:"limitations"`
}

type WalletRecommendationSnapshot struct {
	RulesVersion        string                `json:"rules_version"`
	Trigger             string                `json:"trigger"`
	ParticipantRevision string                `json:"participant_revision"`
	BasisHash           string                `json:"basis_hash"`
	ExpiresAt           *time.Time            `json:"expires_at"`
	Global              WalletGlobal          `json:"global"`
	Contributions       []WalletContribution  `json:"contributions"`
	Target              *estimation.Amount    `json:"target"`
	Complete            bool                  `json:"complete"`
	Applicable          bool                  `json:"applicable"`
	Simulation          *SimulationProvenance `json:"simulation,omitempty"`
}

type WalletRecommendation struct {
	ID            string    `json:"id"`
	ParticipantID string    `json:"participant_id"`
	BindingID     string    `json:"binding_id"`
	GeneratedAt   time.Time `json:"generated_at"`
	WalletRecommendationSnapshot
}

func (s *Service) walletBasis(ctx context.Context, tx pgx.Tx, id string) (walletBasis, error) {
	var body []byte
	var basis walletBasis
	err := tx.QueryRow(ctx, walletBasisSQL, id).Scan(&body)
	if errors.Is(err, pgx.ErrNoRows) {
		return basis, auth.ErrNotFound
	}
	if err == nil {
		err = json.Unmarshal(body, &basis)
	}
	return basis, err
}

func walletRecommendation(b walletBasis, participant string, now time.Time) WalletRecommendationSnapshot {
	body, _ := json.Marshal(b)
	hash := sha256.Sum256(body)
	out := WalletRecommendationSnapshot{RulesVersion: estimation.Rules, ParticipantRevision: b.ParticipantRevision, BasisHash: hex.EncodeToString(hash[:]), Contributions: []WalletContribution{}, Global: WalletGlobal{State: "evidence_incomplete", SourceCount: len(b.Sources), Limitations: []string{
		"WALLET_DEDUCTION_UNVERIFIED", "FULL_USAGE_COVERAGE_UNVERIFIED", "FULL_WALLET_OWNERSHIP_UNVERIFIED", "FREEZE_CONCURRENCY_UNVERIFIED", "NATIVE_PRECISION_UNVERIFIED", "FINANCIAL_APPLICATION_NOT_IMPLEMENTED",
	}}}
	if len(b.Sources) == 0 {
		out.Global.State = "no_recorded_sources"
	}
	if b.CurrentSource == nil || b.SourceID != *b.CurrentSource || !b.Enabled {
		out.Global.State = "condition_changed"
	}
	if len(b.Sources) > 500 {
		out.Global.State = "evidence_limit"
		return out
	}
	results := make([]allocation.Result, 0, len(b.Sources))
	for _, a := range b.Sources {
		c := WalletContribution{AccountID: a.AccountID, AccountRevision: a.Revision, HistoricalContractHead: a.ContractHead, EstimateID: a.EstimateID, EstimateState: "not_configured", ObservationHead: a.ObservationHead, CaptureHead: a.CaptureHead, State: "evidence_incomplete"}
		if a.Contract != nil {
			c.CurrentContractID = &a.Contract.ID
			if a.Contract.ShareUnits != nil {
				v := sharePercent(*a.Contract.ShareUnits)
				c.SharePercent = &v
			}
		}
		if a.SourceID != b.SourceID || a.IdentityState != "verified" || a.ReadState != "available" || a.ObservedConnection != b.ConnectionRevision {
			c.State = "source_unavailable"
		}
		if a.Contract == nil || !a.Contract.Enabled || a.Contract.SourceID != b.SourceID || a.Contract.ConnectionRevision != b.ConnectionRevision || a.Contract.BindingID == nil || *a.Contract.BindingID != b.BindingID {
			c.State = "contract_unavailable"
		}
		if e := a.Estimate; e != nil {
			c.EstimateState = e.State
			if e.Configuration.Assumptions == nil || e.Configuration.Revision != a.EstimateRevision || e.PolicyRevision != b.PolicyRevision || e.ObservationHead != a.ObservationHead || e.CaptureHead != a.CaptureHead || e.ManagementRevision != a.ManagementRevision {
				c.EstimateState = "recalculation_required"
			} else if cfg := e.Configuration.Assumptions; cfg.AccountRevision != a.Revision || cfg.ConnectionRevision != b.ConnectionRevision || cfg.SourceID != a.SourceID || cfg.SourceMode != a.SourceMode {
				c.EstimateState = "condition_changed"
			}
			for _, w := range e.Windows {
				for _, at := range []*time.Time{w.SourceAt, w.OldestSampleAt} {
					if at == nil {
						continue
					}
					expires := at.Add(time.Duration(b.StaleAfterSeconds) * time.Second)
					if at.After(now) || !now.Before(expires) {
						c.EstimateState = "source_stale"
					}
					if expires.After(now) && (out.ExpiresAt == nil || expires.Before(*out.ExpiresAt)) {
						out.ExpiresAt = &expires
					}
				}
				if w.Period != nil {
					expires := w.Period.EndAt
					if !now.Before(expires) {
						c.EstimateState = "source_stale"
					} else if out.ExpiresAt == nil || expires.Before(*out.ExpiresAt) {
						out.ExpiresAt = &expires
					}
				}
			}
		}
		// shortcut: native wallet-unit spending/coverage is unqualified; require native evidence before supplying allocation inputs.
		result := allocation.Evaluate(allocation.Input{Complete: false, CostBasis: "unknown"})
		results = append(results, result)
		out.Contributions = append(out.Contributions, c)
	}
	out.Target = allocation.Global(results, participant)
	return out
}

const walletRecommendationColumns = `id::text,participant_id,binding_id::text,generated_at,snapshot`

func scanWalletRecommendation(row pgx.Row) (WalletRecommendation, error) {
	var out WalletRecommendation
	var body []byte
	err := row.Scan(&out.ID, &out.ParticipantID, &out.BindingID, &out.GeneratedAt, &body)
	if err == nil {
		err = json.Unmarshal(body, &out.WalletRecommendationSnapshot)
	}
	out.GeneratedAt = out.GeneratedAt.UTC()
	return out, err
}

func latestWalletRecommendation(ctx context.Context, tx pgx.Tx, id, binding string) (*WalletRecommendation, error) {
	out, err := scanWalletRecommendation(tx.QueryRow(ctx, `SELECT `+walletRecommendationColumns+` FROM wallet_recommendations WHERE participant_id=$1 AND binding_id=$2 ORDER BY wallet_recommendations.id DESC LIMIT 1`, id, binding))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &out, err
}

func (s *Service) appendWalletRecommendation(ctx context.Context, tx pgx.Tx, actor, task *string, participant, trigger string) (WalletRecommendation, error) {
	basis, err := s.walletBasis(ctx, tx, participant)
	if err != nil {
		return WalletRecommendation{}, err
	}
	out := WalletRecommendation{ParticipantID: participant, BindingID: basis.BindingID, GeneratedAt: time.Now().UTC().Truncate(time.Microsecond)}
	out.WalletRecommendationSnapshot = walletRecommendation(basis, participant, out.GeneratedAt)
	if err = s.qualifySimulationWallet(ctx, tx, basis, participant, &out.WalletRecommendationSnapshot); err != nil {
		return WalletRecommendation{}, err
	}
	out.GeneratedAt = time.Now().UTC().Truncate(time.Microsecond)
	if out.ExpiresAt != nil && !out.GeneratedAt.Before(*out.ExpiresAt) {
		out.WalletRecommendationSnapshot = walletRecommendation(basis, participant, out.GeneratedAt)
	}
	out.Trigger = trigger
	body, err := json.Marshal(out.WalletRecommendationSnapshot)
	if err == nil {
		err = tx.QueryRow(ctx, `INSERT INTO wallet_recommendations(participant_id,binding_id,generated_at,snapshot) VALUES($1,$2,$3,$4) RETURNING id::text`, participant, basis.BindingID, out.GeneratedAt, body).Scan(&out.ID)
	}
	if err == nil {
		_, err = audit.Record(ctx, tx, audit.Entry{ActorID: actor, TaskID: task, Action: "wallet.recommendation", Outcome: "succeeded", TargetType: "participant", TargetID: participant, Revision: &basis.ParticipantRevision, SourceID: &basis.SourceID})
	}
	return out, err
}

func (s *Service) RecomputeWallet(ctx context.Context, token string, in WalletExpected) (WalletRecommendation, error) {
	if !validWalletExpected(in) {
		return WalletRecommendation{}, auth.ErrInput
	}
	tx, actor, err := s.begin(ctx, token, true)
	if err != nil {
		return WalletRecommendation{}, err
	}
	defer tx.Rollback(ctx)
	if _, _, err = s.walletExpected(ctx, tx, in); err != nil {
		return WalletRecommendation{}, err
	}
	latest, err := latestWalletRecommendation(ctx, tx, in.ParticipantID, in.BindingID)
	if err != nil {
		return WalletRecommendation{}, err
	}
	head := "0"
	if latest != nil {
		head = latest.ID
	}
	if head != in.LatestID {
		return WalletRecommendation{}, auth.ErrConflict
	}
	if _, err = s.auth.AuthorizeAdmin(ctx, tx, token); err != nil {
		return WalletRecommendation{}, err
	}
	out, err := s.appendWalletRecommendation(ctx, tx, &actor.ID, nil, in.ParticipantID, "manual_recompute")
	if err == nil {
		err = s.finish(ctx, tx, token, true)
	}
	if err != nil {
		return WalletRecommendation{}, err
	}
	return out, nil
}
