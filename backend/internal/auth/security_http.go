package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
)

var sourceExpiryUTC = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,6})?Z$`)

func (h *Handler) serveSecurity(w http.ResponseWriter, r *http.Request) {
	method := http.MethodPost
	var names, optional []string
	switch r.URL.Path {
	case "/api/v1/security/public", "/api/v1/security/config", "/api/v1/security/source":
		method = http.MethodGet
	case "/api/v1/security/config/update":
		names = []string{"revision", "network_diagnostics_enabled"}
	case "/api/v1/security/network/compare":
		names = []string{"reported_ip", "consent"}
	case "/api/v1/security/sources/list":
		names = []string{"after", "state", "limit"}
	case "/api/v1/security/sources/update":
		names = []string{"ip", "revision", "blocked"}
		optional = []string{"reason", "expires_at"}
	case "/api/v1/security/login-audit":
		names = []string{"from", "until", "after", "limit", "outcome"}
	case "/api/v1/security/management-audit":
		names = []string{"from", "until", "after", "limit"}
	default:
		h.HandleJSON(w, r, r.Method, nil, func(context.Context, string, map[string]json.RawMessage) (any, error) { return nil, ErrNotFound })
		return
	}
	h.HandleJSONOptional(w, r, method, names, optional, func(ctx context.Context, token string, fields map[string]json.RawMessage) (any, error) {
		if value, ok := fields["expires_at"]; ok {
			var text string
			if json.Unmarshal(value, &text) != nil || !sourceExpiryUTC.MatchString(text) {
				return nil, ErrInput
			}
		}
		var input struct {
			AuditRange
			SourceUpdate
			Enabled    bool   `json:"network_diagnostics_enabled"`
			ReportedIP string `json:"reported_ip"`
			Consent    bool   `json:"consent"`
			State      string `json:"state"`
			Outcome    string `json:"outcome"`
		}
		body, err := json.Marshal(fields)
		if err != nil || json.Unmarshal(body, &input) != nil {
			return nil, ErrInput
		}
		switch r.URL.Path {
		case "/api/v1/security/public":
			var enabled bool
			err := h.service.pool.QueryRow(ctx, `SELECT network_diagnostics_enabled FROM security_config WHERE singleton`).Scan(&enabled)
			return map[string]bool{"network_diagnostics_enabled": enabled}, err
		case "/api/v1/security/config":
			return h.service.GetSecurityConfig(ctx, token)
		case "/api/v1/security/source":
			return h.service.GetSecuritySource(ctx, token)
		case "/api/v1/security/config/update":
			return h.service.UpdateSecurityConfig(ctx, token, input.Revision, input.Enabled)
		case "/api/v1/security/network/compare":
			return h.service.CompareNetwork(ctx, token, input.ReportedIP, input.Consent)
		case "/api/v1/security/sources/list":
			return h.service.ListSources(ctx, token, input.After, input.State, input.Limit)
		case "/api/v1/security/sources/update":
			return h.service.UpdateSource(ctx, token, input.SourceUpdate)
		case "/api/v1/security/login-audit":
			return h.service.LoginAudit(ctx, token, input.AuditRange, input.Outcome)
		default:
			return h.service.ManagementAudit(ctx, token, input.AuditRange)
		}
	})
}
