package auth

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestSecuritySourceUsesCurrentAuthorityAndSharedAttribution(t *testing.T) {
	s, pool := setup(t)
	bootstrap(t, s)
	_, admin := login(t, s)
	h, err := NewHandler(s, "http://127.0.0.1:8081", true)
	if err != nil {
		t.Fatal(err)
	}
	read := func(token, forwarded, kind, ip string) {
		t.Helper()
		w := sourceRequest(h, "/api/v1/security/source", "", token, "127.0.0.1", forwarded)
		var source RequestSource
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &source) != nil || source.Kind != kind || source.IP == nil || source.PeerIP == nil || *source.IP != ip || *source.PeerIP != "127.0.0.1" {
			t.Fatal("source read did not preserve current trusted attribution")
		}
	}
	read(admin, "203.0.113.55", "peer", "127.0.0.1")
	if err := h.ConfigureSources("127.0.0.1/32"); err != nil {
		t.Fatal(err)
	}
	read(admin, "203.0.113.55", "trusted_forwarded", "203.0.113.55")
	read(admin, "invalid", "peer", "127.0.0.1")
	if w := sourceRequest(h, "/api/v1/security/source", "", "", "127.0.0.1", ""); w.Code != 401 {
		t.Fatal("anonymous request obtained a protected source")
	}
	for _, request := range []struct {
		path, body string
		status     int
	}{{"/api/v1/security/source", `{}`, 405}, {"/api/v1/security/source?extra=1", "", 400}, {"/api/v1/security/source?extra=%GG", "", 400}} {
		if w := sourceRequest(h, request.path, request.body, admin, "127.0.0.1", ""); w.Code != request.status {
			t.Fatal("undeclared method or query accepted")
		}
	}
	ctx := managementContext()
	member, err := s.CreateUser(ctx, admin, UserCreate{Username: "source.reader", Password: testPassword, Enabled: true, Grants: Grants{Features: []string{}, AccountIDs: []string{}, ParticipantIDs: []string{}}})
	if err != nil {
		t.Fatal(err)
	}
	_, memberToken, err := s.Login(ctx, member.Username, testPassword, testIP)
	if err != nil {
		t.Fatal(err)
	}
	if w := sourceRequest(h, "/api/v1/security/source", "", memberToken, "127.0.0.1", ""); w.Code != 403 {
		t.Fatal("ordinary user received security read access by default")
	}
	exec(t, pool, `UPDATE auth_users SET read_features=ARRAY['security'],revision=revision+1 WHERE id=$1`, member.ID)
	read(memberToken, "203.0.113.55", "trusted_forwarded", "203.0.113.55")
	if w := sourceRequest(h, "/api/v1/security/config", "", memberToken, "127.0.0.1", ""); w.Code != 403 {
		t.Fatal("security read grant obtained administrator configuration")
	}
	exec(t, pool, `UPDATE auth_users SET read_features='{}',revision=revision+1 WHERE id=$1`, member.ID)
	if w := sourceRequest(h, "/api/v1/security/source", "", memberToken, "127.0.0.1", ""); w.Code != 403 {
		t.Fatal("source read retained a withdrawn grant")
	}
	exec(t, pool, `UPDATE auth_users SET enabled=false,revision=revision+1 WHERE id=$1`, member.ID)
	if w := sourceRequest(h, "/api/v1/security/source", "", memberToken, "127.0.0.1", ""); w.Code != 401 {
		t.Fatal("source read retained a disabled session")
	}
	key, err := s.IssueAPIKey(ctx, admin, "security source denial", "read")
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/api/v1/security/source", nil)
	r.RemoteAddr = "127.0.0.1:12345"
	r.Header.Set("Authorization", "Bearer "+key.Secret)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("machine credential obtained an operation outside its catalog")
	}
	rule, err := s.UpdateSource(ctx, admin, "203.0.113.55", "0", true)
	if err != nil {
		t.Fatal(err)
	}
	if w := sourceRequest(h, "/api/v1/security/source", "", admin, "127.0.0.1", rule.IP); w.Code != 403 {
		t.Fatal("source read bypassed the shared source block")
	}
	if w := sourceRequest(h, "/api/v1/security/public", "", "", "127.0.0.1", ""); w.Code != 200 {
		t.Fatal("public diagnostics policy requires a session")
	}
}
