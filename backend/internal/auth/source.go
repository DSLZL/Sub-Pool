package auth

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

type RequestSource struct {
	IP     *string `json:"ip"`
	PeerIP *string `json:"peer_ip"`
	Kind   string  `json:"kind"`
}

type sourceKey struct{}

func sourceContext(ctx context.Context, ip netip.Addr) RequestSource {
	if source, ok := ctx.Value(sourceKey{}).(RequestSource); ok {
		return source
	}
	if ip.IsValid() {
		value := ip.Unmap().String()
		return RequestSource{IP: &value, PeerIP: &value, Kind: "peer"}
	}
	return RequestSource{Kind: "unknown"}
}

func parseIP(value string) (netip.Addr, bool) {
	ip, err := netip.ParseAddr(value)
	return ip.Unmap(), err == nil && ip.Zone() == ""
}

// ConfigureSources is startup-only; an empty list trusts no forwarded headers.
func (h *Handler) ConfigureSources(value string) error {
	var prefixes []netip.Prefix
	if value != "" {
		if len(value) > 4096 || len(strings.Split(value, ",")) > 32 {
			return errors.New("invalid trusted proxy configuration")
		}
		for _, item := range strings.Split(value, ",") {
			prefix, err := netip.ParsePrefix(strings.TrimSpace(item))
			if err != nil || prefix.Addr().Zone() != "" || prefix.Addr().Is4In6() {
				return errors.New("invalid trusted proxy configuration")
			}
			prefixes = append(prefixes, prefix.Masked())
		}
	}
	h.trustedProxies = prefixes
	return nil
}

func (h *Handler) trusted(ip netip.Addr) bool {
	for _, prefix := range h.trustedProxies {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

func (h *Handler) requestSource(r *http.Request) RequestSource {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return RequestSource{Kind: "unknown"}
	}
	peer, ok := parseIP(host)
	if !ok {
		return RequestSource{Kind: "unknown"}
	}
	result := sourceContext(context.Background(), peer)
	lines := r.Header.Values("X-Forwarded-For")
	if !h.trusted(peer) || len(lines) != 1 || len(lines[0]) > 2048 {
		return result
	}
	parts := strings.Split(lines[0], ",")
	if len(parts) > 20 {
		return result
	}
	chain := make([]netip.Addr, len(parts))
	for i, part := range parts {
		ip, valid := parseIP(strings.TrimSpace(part))
		if !valid {
			return result
		}
		chain[i] = ip
	}
	current := peer
	for i := len(chain) - 1; i >= 0 && h.trusted(current); i-- {
		current = chain[i]
	}
	ip := current.String()
	result.IP, result.Kind = &ip, "trusted_forwarded"
	return result
}

func (h *Handler) prepareSource(r *http.Request) (*http.Request, error) {
	if _, ok := r.Context().Value(sourceKey{}).(RequestSource); ok {
		return r, nil
	}
	source := h.requestSource(r)
	ctx := context.WithValue(r.Context(), sourceKey{}, source)
	if r.URL.Path != "/api/v1/auth/login" && r.URL.Path != "/api/v1/security/public" {
		_, machine := ctx.Value(machineKey{}).(machineCredential)
		_, err := tokenDigest(h.token(r))
		if machine {
			_, _, err = apiKeyDigest(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		}
		if err != nil {
			if r.URL.Path == "/api/v1/auth/logout" {
				return r.WithContext(ctx), nil
			}
			return r, ErrUnauthorized
		}
	}
	if source.IP != nil {
		var blocked bool
		err := h.service.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM source_blocks WHERE ip=$1::inet AND blocked)`, *source.IP).Scan(&blocked)
		if err != nil {
			return r, err
		}
		if blocked {
			if r.URL.Path == "/api/v1/auth/login" {
				tx, err := h.service.pool.Begin(ctx)
				if err != nil {
					return r, err
				}
				defer tx.Rollback(ctx)
				if err = event(ctx, tx, "login", "blocked", *source.IP, nil); err != nil {
					return r, err
				}
				if err = tx.Commit(ctx); err != nil {
					return r, err
				}
			}
			return r, ErrSourceBlocked
		}
	}
	return r.WithContext(ctx), nil
}
