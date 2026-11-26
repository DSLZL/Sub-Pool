package connection

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/auth"
)

type nativeWalletAdjustment struct {
	BalanceBefore  *string `json:"balance_before"`
	BalanceAfter   *string `json:"balance_after"`
	LedgerID       string  `json:"ledger_id,omitempty"`
	WeeklyIssuance bool    `json:"weekly_issuance,omitempty"`
}

var nativeDeltaPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]{0,11})(\.[0-9]{1,8})?$`)

func validNativeDelta(value string) bool {
	if !nativeDeltaPattern.MatchString(value) {
		return false
	}
	n, err := strconv.ParseFloat(value, 64)
	if err != nil || n == 0 {
		return false
	}
	exact, ok := walletNumber(json.RawMessage(value), false)
	// The stock handler decodes balance as float64.
	return ok && *exact == strconv.FormatFloat(n, 'f', -1, 64)
}

func (s *Service) prepareNativeWalletApplication(ctx context.Context, tx pgx.Tx, c storedConfig, p Participant, read *WalletRead, out *WalletApplication) error {
	if !p.Enabled || c.reverificationRequired || s.simulationEpoch(c) != "" ||
		read == nil || read.Unit != "native_wallet" || read.ConnectionRevision != c.Revision ||
		read.ParticipantRevision != p.Revision || read.UpstreamUserID != p.Binding.UpstreamUserID ||
		read.Balance.Value == nil || read.FrozenBalance.Value == nil ||
		read.Read.State != "available" || read.FieldAuthority.State != "available" {
		return auth.ErrConflict
	}
	wallet := nativeWalletLockKey(p.Binding.SourceID, p.Binding.UpstreamUserID)
	if err := lockWallet(ctx, tx, wallet); err != nil {
		return err
	}
	pending, _, err := pendingWeeklyIssuance(ctx, tx, p)
	if err != nil {
		return err
	}
	if pending != "" {
		return auth.ErrConflict
	}
	var blocked bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM wallet_application_requests r WHERE r.participant_id=$1 AND NOT (`+walletApplicationResolvedSQL+`))
	 OR EXISTS(SELECT 1 FROM pools p JOIN pool_contracts c ON c.pool_id=p.id AND c.revision=p.revision
	  JOIN pool_contract_participants m ON m.contract_id=c.id
	  WHERE m.participant_id=$1 AND p.enabled AND (p.weekly_wallet->>'enabled'='true' OR p.weekly_wallet->>'auto_allocate'='true'))`, p.ID).Scan(&blocked); err != nil {
		return err
	}
	if blocked {
		return auth.ErrConflict
	}
	out.State, out.DispatchState, out.Reason = "pending", "pending", "NATIVE_WALLET_ADJUST_PENDING"
	out.UpdatedAt = &out.AttemptedAt
	out.Details.Reasons = []string{out.Reason}
	out.Details.NativeAdjustment = &nativeWalletAdjustment{BalanceBefore: read.Balance.Value}
	return nil
}

func nativeWalletRequestKey(out *WalletApplication) string {
	return "sub-pool-wallet-" + out.Details.Input.RequestID + "-" + out.ID
}

func nativeWalletLockKey(source, user string) string {
	return "native-wallet:" + source + ":" + user
}

func (s *Service) nativeWalletBalance(ctx context.Context, c storedConfig, kind, secret, user string) *string {
	balance, _ := s.nativeWalletFields(ctx, c, kind, secret, user)
	return balance
}

func (s *Service) nativeWalletFields(ctx context.Context, c storedConfig, kind, secret, user string) (*string, *string) {
	balance, frozen, _ := s.nativeWalletUserFields(ctx, c, kind, secret, user)
	return balance, frozen
}

func (s *Service) nativeWalletUserFields(ctx context.Context, c storedConfig, kind, secret, user string) (*string, *string, bool) {
	body, check := s.upstream.get(ctx, c.BaseURL, "/api/v1/admin/users/"+user, kind, secret, "users.balance.read", true)
	if check.State != "available" {
		return nil, nil, false
	}
	if _, ok := projectNativeUser(body, user, secret); !ok {
		return nil, nil, false
	}
	fields, _ := usageObject(body)
	balance, _ := walletNumber(fields["balance"], false)
	frozen, _ := walletNumber(fields["frozen_balance"], true)
	return balance, frozen, true
}

func (s *Service) observeNativeWalletApplication(ctx context.Context, c storedConfig, out *WalletApplication) {
	out.State, out.DispatchState, out.Reason = "unknown", "unknown", "NATIVE_WALLET_WRITE_UNCONFIRMED"
	kind, secret, err := s.decrypt(c)
	if err != nil {
		return
	}
	user := out.Details.UpstreamUserID
	// shortcut: search the newest 100 admin adjustments; older unconfirmed entries need ledger inspection.
	body, check := s.upstream.get(ctx, c.BaseURL, "/api/v1/admin/users/"+user+"/balance-history?page=1&page_size=100&type=admin_balance", kind, secret, "users.balance.history", true)
	id, found := nativeWalletLedgerID(body, user, nativeWalletRequestKey(out), out.Details.Input.NativeDelta)
	if check.State != "available" || !found {
		return
	}
	after := s.nativeWalletBalance(ctx, c, kind, secret, user)
	if after != nil || out.Details.NativeAdjustment.WeeklyIssuance {
		out.Details.NativeAdjustment.LedgerID, out.Details.NativeAdjustment.BalanceAfter = id, after
		out.State, out.DispatchState, out.Reason = "applied", "confirmed", "NATIVE_WALLET_ADJUSTED"
	}
}

func nativeWalletLedgerID(body json.RawMessage, user, notes, delta string) (string, bool) {
	page, ok := parsePage(body, 1, 100)
	expected, valid := walletNumber(json.RawMessage(delta), false)
	if !ok || !valid {
		return "", false
	}
	var id string
	for _, item := range page.Items {
		fields, valid := usageObject(item)
		var tag string
		if !valid {
			return "", false
		}
		if decode(fields["notes"], &tag) != nil || tag != notes {
			continue
		}
		var ledgerID, userID int64
		var kind, status string
		value, valid := walletNumber(fields["value"], false)
		if id != "" || !valid || *value != *expected ||
			decode(fields["id"], &ledgerID) != nil || ledgerID <= 0 ||
			decode(fields["used_by"], &userID) != nil || strconv.FormatInt(userID, 10) != user ||
			decode(fields["type"], &kind) != nil || kind != "admin_balance" ||
			decode(fields["status"], &status) != nil || status != "used" {
			return "", false
		}
		id = strconv.FormatInt(ledgerID, 10)
	}
	return id, id != ""
}

func saveNativeWalletResult(ctx context.Context, tx pgx.Tx, out *WalletApplication, actor string) error {
	out.Details.Reasons = []string{out.Reason}
	return saveWalletApplicationResult(ctx, tx, out, actor, "")
}

func (s *Service) sendNativeWalletAdjustment(ctx context.Context, tx pgx.Tx, c storedConfig, token, grantID string, out *WalletApplication) error {
	out.Reason = "NATIVE_WALLET_WRITE_UNAVAILABLE"
	kind, secret, err := s.decrypt(c)
	if err != nil {
		return err
	}
	mode, _ := s.upstream.readRunMode(ctx, c.BaseURL, kind, secret)
	if mode == nil || *mode != "standard" {
		return nil
	}
	user := out.Details.UpstreamUserID
	before := s.nativeWalletBalance(ctx, c, kind, secret, user)
	if before == nil {
		return nil
	}
	out.Details.NativeAdjustment.BalanceBefore = before
	if amount, debitRequest := strings.CutPrefix(out.Details.Input.NativeDelta, "-"); debitRequest {
		balance, _ := new(big.Rat).SetString(*before)
		debit, _ := new(big.Rat).SetString(amount)
		if balance.Cmp(debit) < 0 {
			out.Reason = "NATIVE_WALLET_INSUFFICIENT_BALANCE"
			return nil
		}
	}
	return s.postNativeWalletAdjustment(ctx, tx, c, kind, secret, grantID, out, func() (bool, error) {
		_, err := s.walletApplicationActor(ctx, tx, token, nil)
		return err == nil, err
	})
}

func (s *Service) postNativeWalletAdjustment(ctx context.Context, tx pgx.Tx, c storedConfig, kind, secret, grantID string, out *WalletApplication, canSend func() (bool, error)) error {
	amount, operation := out.Details.Input.NativeDelta, "add"
	if strings.HasPrefix(amount, "-") {
		operation, amount = "subtract", strings.TrimPrefix(amount, "-")
	}
	key := nativeWalletRequestKey(out)
	body, err := json.Marshal(struct {
		Balance   json.Number `json:"balance"`
		Operation string      `json:"operation"`
		Notes     string      `json:"notes"`
	}{json.Number(amount), operation, key})
	if err != nil {
		return err
	}
	reason, err := s.checkWriteDispatch(ctx, tx, c, writeWallet, grantID)
	if err != nil {
		return err
	}
	if reason != "" {
		out.Reason = reason
		return nil
	}
	if ready, err := canSend(); err != nil || !ready {
		return err
	}
	_, _ = s.upstream.requestWithKey(ctx, http.MethodPost, c.BaseURL, "/api/v1/admin/users/"+out.Details.UpstreamUserID+"/balance", kind, secret, "wallet.write", body, true, key)
	s.observeNativeWalletApplication(ctx, c, out)
	return nil
}

func (s *Service) executeNativeWalletApplication(ctx context.Context, conn *pgxpool.Conn, token, grantID string, out WalletApplication) (WalletApplication, error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return WalletApplication{}, err
	}
	defer tx.Rollback(ctx)
	actor, err := s.walletApplicationActor(ctx, tx, token, nil)
	if err != nil {
		return WalletApplication{}, err
	}
	c, p, err := s.walletReferences(ctx, tx, out.Details.Input.WalletExpected)
	if err != nil && !errors.Is(err, auth.ErrConflict) && !errors.Is(err, auth.ErrNotFound) {
		return WalletApplication{}, err
	}
	out.State, out.DispatchState, out.Reason = "rejected", "not_dispatched", "NATIVE_WALLET_PRECONDITION_CHANGED"
	if err == nil && p.Enabled && p.Binding.UpstreamUserID == out.Details.UpstreamUserID && !c.reverificationRequired && s.simulationEpoch(c) == "" {
		if err = s.sendNativeWalletAdjustment(ctx, tx, c, token, grantID, &out); err != nil {
			return WalletApplication{}, err
		}
	}
	if err = saveNativeWalletResult(ctx, tx, &out, actor.ID); err == nil {
		err = s.finish(ctx, tx, token, true)
	}
	if err != nil {
		return WalletApplication{}, err
	}
	return out, nil
}
