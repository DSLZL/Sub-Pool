package connection

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"math/big"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type weeklyWalletPlan struct {
	Gross         string  `json:"gross"`
	Spent         string  `json:"spent"`
	OpeningDebt   string  `json:"opening_debt"`
	Debt          string  `json:"debt"`
	BalanceBefore string  `json:"balance_before"`
	Target        string  `json:"target"`
	ContractID    string  `json:"contract_id"`
	EstimateID    *string `json:"estimate_id"`
	SharePercent  string  `json:"share_percent,omitempty"`
}

type weeklyWalletManual struct {
	token, basis string
	cutoff       *time.Time
	preview      bool
}

func weeklyWalletAmount(value string) *big.Rat {
	v, _ := new(big.Rat).SetString(value)
	return v
}

func weeklyWalletFloor(value *big.Rat) *big.Rat {
	quantum := big.NewInt(100000000)
	ticks := new(big.Int).Mul(value.Num(), quantum)
	ticks.Quo(ticks, value.Denom())
	return new(big.Rat).SetFrac(ticks, quantum)
}

func calculateWeeklyWallet(gross *big.Rat, before string, prior *weeklyWalletEntry, sameCycle bool) (weeklyWalletPlan, bool) {
	balance := weeklyWalletAmount(before)
	if gross == nil || gross.Sign() < 0 || balance == nil || weeklyWalletFloor(balance).Cmp(balance) != 0 {
		return weeklyWalletPlan{}, false
	}
	debt, spent := new(big.Rat), new(big.Rat)
	if prior != nil {
		if sameCycle {
			debt = weeklyWalletAmount(prior.Plan.OpeningDebt)
			previous := weeklyWalletAmount(prior.Plan.Target)
			if previous == nil || previous.Sign() < 0 {
				return weeklyWalletPlan{}, false
			}
			spent.Sub(previous, balance)
			if spent.Sign() < 0 {
				return weeklyWalletPlan{}, false
			}
		} else {
			debt = weeklyWalletAmount(prior.Plan.Debt)
		}
	}
	if debt == nil || debt.Sign() < 0 {
		return weeklyWalletPlan{}, false
	}
	if !sameCycle && balance.Sign() < 0 {
		debt.Sub(debt, balance)
	}
	gross = weeklyWalletFloor(gross)
	net := new(big.Rat).Sub(gross, debt)
	net.Sub(net, spent)
	target, carried := net, new(big.Rat)
	if net.Sign() < 0 {
		target, carried = new(big.Rat), new(big.Rat).Neg(net)
	}
	return weeklyWalletPlan{
		Gross: usageDecimal(gross), Spent: usageDecimal(spent), OpeningDebt: usageDecimal(debt),
		Debt: usageDecimal(carried), BalanceBefore: before, Target: usageDecimal(target),
	}, true
}

func previousWeeklyWallet(ctx context.Context, tx pgx.Tx, p weeklyWalletPool, member Participant, finalized bool) (*weeklyWalletEntry, error) {
	var out weeklyWalletEntry
	err := tx.QueryRow(ctx, `SELECT w.id::text,w.phase,w.period_end,w.snapshot FROM weekly_wallet_allocations w
	 LEFT JOIN wallet_application_requests r ON r.id=w.application_id
	 WHERE w.pool_id=$1 AND w.account_id=$2 AND w.binding_id=$3 AND (NOT $4 OR w.phase='settlement')
	  AND (w.application_id IS NULL OR `+walletApplicationSnapshotSQL+`->>'state'='applied')
	 ORDER BY w.id DESC LIMIT 1`, p.ID, p.AccountID, member.Binding.ID, finalized).Scan(&out.ID, &out.Phase, &out.PeriodEnd, &out.Plan)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &out, err
}

// Round robin keeps a blocked wallet from delaying the other members.
func (s *Service) processWeeklyWallet(ctx context.Context, after string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	release, err := s.Runtime.Enter(ctx)
	if err != nil {
		return after, err
	}
	defer release()
	var pool, member string
	afterMember, afterPool, _ := strings.Cut(after, "/")
	err = s.pool.QueryRow(ctx, `SELECT p.id,m.participant_id FROM pools p
	 JOIN pool_contracts c ON c.pool_id=p.id AND c.revision=p.revision
	 JOIN pool_contract_participants m ON m.contract_id=c.id
	 JOIN participants x ON x.id=m.participant_id AND x.enabled AND x.deleted_at IS NULL
	 WHERE p.enabled AND (p.weekly_wallet->>'enabled'='true' OR p.weekly_wallet->>'auto_allocate'='true')
	 ORDER BY ((m.participant_id,p.id)<=($1,$2)),m.participant_id,p.id LIMIT 1`, afterMember, afterPool).Scan(&pool, &member)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return after, err
	}
	return member + "/" + pool, s.runWeeklyWallet(ctx, pool, member)
}

func (s *Service) runWeeklyWallet(ctx context.Context, pool, participant string) error {
	_, err := s.applyWeeklyWallet(ctx, pool, participant, nil)
	return err
}

func (s *Service) applyWeeklyWallet(ctx context.Context, pool, participant string, manual *weeklyWalletManual) (weeklyWalletEntry, error) {
	return s.applyWeeklyIssuance(ctx, pool, participant, manual)
}

func makeWeeklyIssuanceIntent(ctx context.Context, tx pgx.Tx, actor string, c storedConfig, member Participant, entry weeklyWalletEntry) (WalletApplication, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return WalletApplication{}, err
	}
	key := base64.RawURLEncoding.EncodeToString(raw)
	input := WalletApplicationInput{
		WalletExpected: WalletExpected{ParticipantID: member.ID, ParticipantRevision: member.Revision,
			BindingID: member.Binding.ID, SourceID: *c.SourceID, ConnectionRevision: c.Revision, LatestID: "0"},
		RequestID: key, RecommendationID: "0", NativeDelta: entry.Issuance.Amount,
	}
	out := WalletApplication{ParticipantID: member.ID, AttemptedAt: entry.RecordedAt, Visibility: "administrator", WalletApplicationSnapshot: WalletApplicationSnapshot{
		ParticipantName: member.Name, State: "pending", DispatchState: "pending", Reason: "NATIVE_WALLET_ADJUST_PENDING",
		Unit: "native_wallet", UpdatedAt: &entry.RecordedAt,
		Details: &WalletApplicationDetails{ActorID: actor, Input: input, UpstreamUserID: member.Binding.UpstreamUserID,
			EvaluatedBasisHash: entry.basis, Reasons: []string{"NATIVE_WALLET_ADJUST_PENDING"},
			NativeAdjustment: &nativeWalletAdjustment{BalanceBefore: entry.Issuance.Balance, WeeklyIssuance: true}},
	}}
	err := tx.QueryRow(ctx, `INSERT INTO wallet_application_requests(actor_id,request_id,request_digest,participant_id,binding_id,attempted_at,snapshot)
	 VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id::text`, actor, key, entry.basis, member.ID, member.Binding.ID,
		entry.RecordedAt, out.WalletApplicationSnapshot).Scan(&out.ID)
	return out, err
}

func (s *Service) weeklyWalletWriteAvailable(ctx context.Context, c storedConfig, kind, secret string, credit bool) bool {
	mode, _ := s.upstream.readRunMode(ctx, c.BaseURL, kind, secret)
	if mode == nil || *mode != "standard" {
		return false
	}
	if !credit {
		return true
	}
	body, check := s.upstream.get(ctx, c.BaseURL, "/api/v1/admin/settings", kind, secret, "settings.read", true)
	var fields struct {
		Affiliate *bool `json:"affiliate_enabled"`
		Rebate    *bool `json:"affiliate_admin_recharge_enabled"`
	}
	return check.State == "available" && decode(body, &fields) == nil &&
		(fields.Affiliate != nil && !*fields.Affiliate || fields.Rebate != nil && !*fields.Rebate)
}

func equalStringPtr(left, right *string) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}
