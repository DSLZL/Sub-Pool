package connection

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/auth"
)

type DispatchCapability struct {
	CanPause                  bool   `json:"can_pause"`
	CanRestore                bool   `json:"can_restore"`
	AutomaticRestoreAvailable bool   `json:"automatic_restore_available"`
	HistoryState              string `json:"history_state"`
	Reason                    string `json:"reason"`
}

func dispatchCapability() DispatchCapability {
	// shortcut: native scheduling has no ownership guard; enable control only after independent control is qualified.
	return DispatchCapability{HistoryState: "unavailable", Reason: "SCHEDULING_CONTROL_UNQUALIFIED"}
}

type DispatchListInput struct {
	After  string `json:"after"`
	Search string `json:"search"`
	Limit  int    `json:"limit"`
}

type DispatchAccount struct {
	ID            string `json:"id"`
	Revision      string `json:"revision"`
	Name          string `json:"name"`
	SourceID      string `json:"source_id"`
	SourceCurrent bool   `json:"source_current"`
}

type DispatchAccounts struct {
	DispatchCapability
	Accounts           []DispatchAccount `json:"accounts"`
	NextAfter          *string           `json:"next_after"`
	ConnectionRevision string            `json:"connection_revision"`
}

type DispatchObservation struct {
	DispatchCapability
	AccountReadInput
	CheckedAt             time.Time `json:"checked_at"`
	ConfiguredSchedulable *bool     `json:"configured_schedulable"`
	NativeStatus          *string   `json:"native_status"`
	Read                  Check     `json:"read"`
}

func (s *Service) finishDispatchRead(ctx context.Context, tx pgx.Tx, token string) error {
	if _, err := s.auth.AuthorizeRead(ctx, tx, token, "dispatch"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) ListDispatchAccounts(ctx context.Context, token string, input DispatchListInput) (DispatchAccounts, error) {
	if input.After != "" && !managedID(input.After) || input.Limit < 1 || input.Limit > 100 || len(input.Search) > 256 || !utf8.ValidString(input.Search) || strings.ContainsFunc(input.Search, unicode.IsControl) {
		return DispatchAccounts{}, auth.ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return DispatchAccounts{}, err
	}
	defer tx.Rollback(ctx)
	access, err := s.auth.AuthorizeRead(ctx, tx, token, "dispatch")
	if err != nil {
		return DispatchAccounts{}, err
	}
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return DispatchAccounts{}, err
	}
	scope, args, err := access.ScopeSQL(pgx.Identifier{"id"}, nil, 1)
	if err != nil {
		return DispatchAccounts{}, err
	}
	first := len(args) + 1
	pattern := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(input.Search) + "%"
	args = append(args, input.After, pattern, input.Limit+1)
	rows, err := tx.Query(ctx, `SELECT id,revision,name,source_id FROM managed_accounts WHERE `+scope+` AND id>$`+strconv.Itoa(first)+` AND name ILIKE $`+strconv.Itoa(first+1)+` ORDER BY id LIMIT $`+strconv.Itoa(first+2), args...)
	if err != nil {
		return DispatchAccounts{}, err
	}
	page := DispatchAccounts{DispatchCapability: dispatchCapability(), Accounts: []DispatchAccount{}, ConnectionRevision: c.Revision}
	for rows.Next() {
		var account DispatchAccount
		var revision int64
		if err := rows.Scan(&account.ID, &revision, &account.Name, &account.SourceID); err != nil {
			rows.Close()
			return DispatchAccounts{}, err
		}
		account.Revision = strconv.FormatInt(revision, 10)
		account.SourceCurrent = c.SourceID != nil && account.SourceID == *c.SourceID
		page.Accounts = append(page.Accounts, account)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return DispatchAccounts{}, err
	}
	if len(page.Accounts) > input.Limit {
		page.Accounts = page.Accounts[:input.Limit]
		next := page.Accounts[input.Limit-1].ID
		page.NextAfter = &next
	}
	if err = s.finishDispatchRead(ctx, tx, token); err != nil {
		return DispatchAccounts{}, err
	}
	return page, nil
}

func (s *Service) ReadDispatchAccount(ctx context.Context, token string, input AccountReadInput) (DispatchObservation, error) {
	if !validAccountRead(input) {
		return DispatchObservation{}, auth.ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return DispatchObservation{}, err
	}
	defer tx.Rollback(ctx)
	access, err := s.auth.AuthorizeRead(ctx, tx, token, "dispatch")
	if err == nil {
		err = access.CheckAccount(input.ID)
	}
	if err != nil {
		return DispatchObservation{}, err
	}
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return DispatchObservation{}, err
	}
	a, err := readManaged(ctx, tx, input.ID, c, false)
	if err != nil {
		return DispatchObservation{}, err
	}
	if !accountReadMatches(input, a, c) {
		return DispatchObservation{}, auth.ErrConflict
	}
	kind, secret, err := s.decrypt(c)
	if err != nil {
		return DispatchObservation{}, err
	}
	if _, err = s.auth.AuthorizeRead(ctx, tx, token, "dispatch"); err != nil {
		return DispatchObservation{}, err
	}
	body, check := s.upstream.get(ctx, c.BaseURL, "/api/v1/admin/accounts/"+a.UpstreamID, kind, secret, "dispatch.account.read", true)
	out := DispatchObservation{DispatchCapability: dispatchCapability(), AccountReadInput: input}
	if check.State == "available" {
		observed, valid := inspectAccount(body, a.UpstreamID, secret)
		switch {
		case !valid:
			check = invalid(check)
		case a.IdentityState == "conflict" || a.Snapshot.Account.Platform != observed.Account.Platform || a.Snapshot.Account.Type != observed.Account.Type || observed.identity == "shadow" || len(a.fingerprint) > 0 && len(observed.fingerprint) > 0 && !bytes.Equal(a.fingerprint, observed.fingerprint):
			check.State, check.Reason = "failed", "ACCOUNT_IDENTITY_CHANGED"
		case a.IdentityState != "verified" || observed.identity != "verified" || len(a.fingerprint) == 0:
			check.State, check.Reason = "unverified", "RESOURCE_IDENTITY_UNVERIFIED"
		default:
			out.ConfiguredSchedulable, out.NativeStatus = &observed.Account.Schedulable, &observed.Account.Status
		}
	}
	out.CheckedAt, out.Read = time.Now().UTC().Truncate(time.Microsecond), check
	if err = s.finishDispatchRead(ctx, tx, token); err != nil {
		return DispatchObservation{}, err
	}
	return out, nil
}
