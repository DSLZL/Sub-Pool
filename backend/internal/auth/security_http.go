package auth

import (
	"context"
	"encoding/json"
	"net/http"
)

func (h *Handler) serveSecurity(w http.ResponseWriter, r *http.Request) {
	method := http.MethodPost
	var names []string
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
	case "/api/v1/security/login-audit":
		names = []string{"from", "until", "after", "limit", "outcome"}
	case "/api/v1/security/management-audit":
		names = []string{"from", "until", "after", "limit"}
	default:
		h.HandleJSON(w, r, r.Method, nil, func(context.Context, string, map[string]json.RawMessage) (any, error) { return nil, ErrNotFound })
		return
	}
	h.HandleJSON(w, r, method, names, func(ctx context.Context, token string, fields map[string]json.RawMessage) (any, error) {
		var input struct {
			AuditRange
			Revision   string `json:"revision"`
			Enabled    bool   `json:"network_diagnostics_enabled"`
			ReportedIP string `json:"reported_ip"`
			Consent    bool   `json:"consent"`
			State      string `json:"state"`
			IP         string `json:"ip"`
			Blocked    bool   `json:"blocked"`
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
			return h.service.UpdateSource(ctx, token, input.IP, input.Revision, input.Blocked)
		case "/api/v1/security/login-audit":
			return h.service.LoginAudit(ctx, token, input.AuditRange, input.Outcome)
		default:
			return h.service.ManagementAudit(ctx, token, input.AuditRange)
		}
	})
}
