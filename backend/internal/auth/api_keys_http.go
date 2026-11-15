package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"
)

type machineKey struct{}
type machineCredential struct {
	ID         string
	Management bool
}

// APIKeyActor is safe metadata; callers use it only after transactional authorization.
func APIKeyActor(ctx context.Context) *string {
	credential, ok := ctx.Value(machineKey{}).(machineCredential)
	if !ok {
		return nil
	}
	return &credential.ID
}

type APIOperation struct {
	Name         string `json:"name"`
	Path         string `json:"path"`
	Method       string `json:"method"`
	Feature      string `json:"feature"`
	Contract     string `json:"contract"`
	Management   bool   `json:"management"`
	Availability string `json:"availability"`
}

// The same closed catalog both admits machine requests and describes them.
var machineOperations = []APIOperation{
	{"apiKeys.access", "/api/v1/api-keys/access", "GET", "", "api-keys-v1", false, "available"},
	{"apiKeys.catalog", "/api/v1/api-keys/catalog", "GET", "", "api-keys-v1", false, "available"},
	{"accounts.list", "/api/v1/accounts/list", "POST", "accounts", "accounts-v1", false, "available"},
	{"accounts.get", "/api/v1/accounts/get", "POST", "accounts", "accounts-v1", false, "available"},
	{"monitoring.account", "/api/v1/monitoring/accounts/get", "POST", "accounts", "monitoring-v1", false, "available"},
	{"participants.list", "/api/v1/participants/list", "POST", "pools", "participants-v2", false, "available"},
	{"participants.get", "/api/v1/participants/get", "POST", "pools", "participants-v2", false, "available"},
	{"pools.list", "/api/v1/pools/list", "POST", "pools", "pools-v1", false, "available"},
	{"pools.get", "/api/v1/pools/get", "POST", "pools", "pools-v1", false, "available"},
	{"pools.contract", "/api/v1/pools/contract", "POST", "pools", "pools-v1", false, "available"},
	{"pools.history", "/api/v1/pools/history", "POST", "pools", "pools-v1", false, "available"},
	{"pools.evidence", "/api/v1/pools/usage-evidence", "POST", "pools", "pools-v1", false, "available"},
	{"participants.captures", "/api/v1/participants/usage/captures", "POST", "usage", "participants-v2", false, "available"},
	{"participants.usage", "/api/v1/participants/usage/view", "POST", "usage", "participants-v2", false, "available"},
	{"participants.window", "/api/v1/participants/usage/window", "POST", "usage", "participants-v2", false, "available"},
	{"participants.history", "/api/v1/participants/usage/history", "POST", "usage", "participants-v2", false, "available"},
	{"observations.list", "/api/v1/observations/list", "POST", "observations", "observations-v1", false, "available"},
	{"observations.get", "/api/v1/observations/get", "POST", "observations", "observations-v1", false, "available"},
	{"observations.events", "/api/v1/observations/events", "POST", "observations", "observations-v1", false, "available"},
	{"observations.state", "/api/v1/observations/state", "POST", "observations", "observations-v1", false, "available"},
	{"estimates.current", "/api/v1/estimates/current", "POST", "estimates", "estimates-v1", false, "available"},
	{"estimates.record", "/api/v1/estimates/record", "POST", "estimates", "estimates-v1", false, "available"},
	{"estimates.history", "/api/v1/estimates/history", "POST", "estimates", "estimates-v1", false, "available"},
	{"wallets.list", "/api/v1/wallets/list", "POST", "balances", "wallets-v3", false, "available"},
	{"wallets.get", "/api/v1/wallets/get", "POST", "balances", "wallets-v3", false, "available"},
	{"wallets.history", "/api/v1/wallets/history", "POST", "balances", "wallets-v3", false, "available"},
	{"walletApplications.get", "/api/v1/wallet-applications/get", "POST", "balances", "wallet-applications-v3", false, "available"},
	{"walletApplications.list", "/api/v1/wallet-applications/list", "POST", "balances", "wallet-applications-v3", false, "available"},
	{"notifications.get", "/api/v1/notifications/get", "POST", "notifications", "notifications-v3", false, "available"},
	{"notifications.list", "/api/v1/notifications/list", "POST", "notifications", "notifications-v3", false, "available"},
	{"walletApplications.request", "/api/v1/wallet-applications/request", "POST", "balances", "wallet-applications-v3", true, "not_dispatched_only"},
	{"walletApplications.simulate", "/api/v1/wallet-applications/simulate", "POST", "balances", "wallet-applications-v3", true, "protocol_simulation_only"},
	{"walletApplications.reconcile", "/api/v1/wallet-applications/reconcile", "POST", "balances", "wallet-applications-v3", true, "protocol_simulation_only"},
}

func (h *Handler) domainCredentials(r *http.Request) (*http.Request, string, error) {
	lines := r.Header.Values("Authorization")
	if len(lines) == 0 {
		return r, h.token(r), nil
	}
	if len(lines) != 1 || len(r.Header.Values("Cookie")) != 0 || !strings.HasPrefix(lines[0], "Bearer ") {
		return r, "", ErrUnauthorized
	}
	token := strings.TrimPrefix(lines[0], "Bearer ")
	id, _, err := apiKeyDigest(token)
	if err != nil {
		return r, "", err
	}
	for _, operation := range machineOperations {
		if operation.Path == r.URL.Path && operation.Method == r.Method {
			ctx := context.WithValue(r.Context(), machineKey{}, machineCredential{ID: id, Management: operation.Management})
			return r.WithContext(ctx), token, nil
		}
	}
	return r, "", ErrForbidden
}

type APICatalog struct {
	Version        string         `json:"version"`
	Access         Access         `json:"access"`
	CredentialMode string         `json:"credential_mode"`
	Operations     []APIOperation `json:"operations"`
	Limitations    []string       `json:"limitations"`
	Errors         []APIError     `json:"errors"`
}

type APIError struct {
	Code    string `json:"code"`
	Status  int    `json:"status"`
	Meaning string `json:"meaning"`
}

func (s *Service) APIKeyCatalog(ctx context.Context, token string) (APICatalog, error) {
	out := APICatalog{Version: "api-keys-v1", CredentialMode: "session", Operations: []APIOperation{}, Limitations: []string{"RETAINED_LOCAL_FACTS_ONLY", "FULL_USAGE_COVERAGE_UNVERIFIED", "FINANCIAL_APPLICATION_UNAVAILABLE", "NO_COMBINED_OVERVIEW_ENDPOINT", "MUTATION_RESPONSE_LOSS_REQUIRES_READBACK"}}
	out.Errors = []APIError{
		{"BAD_REQUEST", 400, "The request does not match the frozen input contract."},
		{"UNAUTHORIZED", 401, "The credential is invalid, revoked, or its owner is disabled."},
		{"FORBIDDEN", 403, "The operation, feature, or credential mode is not permitted."},
		{"SOURCE_BLOCKED", 403, "The server-observed source is blocked."},
		{"NOT_FOUND", 404, "The resource is absent or outside the current data scope."},
		{"METHOD_NOT_SUPPORTED", 405, "Use the exact catalog method."},
		{"CONFLICT", 409, "The expected revision or business basis is stale."},
		{"CONNECTION_NOT_CONFIGURED", 409, "The business query requires a configured source."},
		{"TOO_MANY_REQUESTS", 429, "Wait until the declared retry time; do not replay an uncertain mutation."},
		{"INTERNAL_SERVER_ERROR", 500, "The request failed; a lost mutation response requires readback."},
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	out.Access, err = currentAccess(ctx, tx, token)
	if err != nil {
		return out, err
	}
	if credential, ok := ctx.Value(machineKey{}).(machineCredential); ok {
		if err = tx.QueryRow(ctx, `SELECT mode FROM auth_api_keys WHERE id=$1`, credential.ID).Scan(&out.CredentialMode); err != nil {
			return out, err
		}
	}
	for _, operation := range machineOperations {
		if operation.Feature != "" && !out.Access.AllData && !slices.Contains(out.Access.Features, operation.Feature) || operation.Management && (!out.Access.AllData || out.CredentialMode == "read") {
			continue
		}
		out.Operations = append(out.Operations, operation)
	}
	if _, err = currentAccess(ctx, tx, token); err == nil {
		err = tx.Commit(ctx)
	}
	return out, err
}

func (h *Handler) serveAPIKeys(w http.ResponseWriter, r *http.Request) {
	method := http.MethodPost
	var names []string
	switch r.URL.Path {
	case "/api/v1/api-keys/access", "/api/v1/api-keys/catalog":
		method = http.MethodGet
	case "/api/v1/api-keys/list":
		names = []string{"after", "limit"}
	case "/api/v1/api-keys/create":
		names = []string{"name", "mode"}
	case "/api/v1/api-keys/rotate", "/api/v1/api-keys/revoke":
		names = []string{"id", "revision"}
	default:
		h.HandleJSON(w, r, r.Method, nil, func(context.Context, string, map[string]json.RawMessage) (any, error) { return nil, ErrNotFound })
		return
	}
	h.HandleJSON(w, r, method, names, func(ctx context.Context, token string, fields map[string]json.RawMessage) (any, error) {
		ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		var input struct {
			ID       string `json:"id"`
			Revision string `json:"revision"`
			Name     string `json:"name"`
			Mode     string `json:"mode"`
			After    string `json:"after"`
			Limit    int    `json:"limit"`
		}
		body, err := json.Marshal(fields)
		if err != nil || method == http.MethodPost && json.Unmarshal(body, &input) != nil {
			return nil, ErrInput
		}
		switch r.URL.Path {
		case "/api/v1/api-keys/access":
			return h.service.CurrentAccess(ctx, token)
		case "/api/v1/api-keys/catalog":
			return h.service.APIKeyCatalog(ctx, token)
		case "/api/v1/api-keys/list":
			return h.service.ListAPIKeys(ctx, token, input.After, input.Limit)
		case "/api/v1/api-keys/create":
			return h.service.IssueAPIKey(ctx, token, input.Name, input.Mode)
		case "/api/v1/api-keys/rotate":
			return h.service.RotateAPIKey(ctx, token, input.ID, input.Revision)
		default:
			return h.service.RevokeAPIKey(ctx, token, input.ID, input.Revision)
		}
	})
}
