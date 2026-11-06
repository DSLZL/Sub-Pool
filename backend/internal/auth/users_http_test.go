package auth

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestUsersHTTPStrictBoundaryAndOrdinaryIsolation(t *testing.T) {
	s, pool := setup(t)
	bootstrap(t, s)
	_, admin := login(t, s)
	user, member := newMember(t, s, admin, "http.reader")
	origin := "https://console.example.test"
	h, err := NewHandler(s, origin, false)
	if err != nil {
		t.Fatal(err)
	}
	adminCookie := "__Host-sub_pool_session=" + admin
	memberCookie := "__Host-sub_pool_session=" + member
	valid, err := json.Marshal(userInput(user))
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		"{}", "null", "[]", string(valid) + "{}",
		strings.Replace(string(valid), `"enabled":true`, `"enabled":null`, 1),
		strings.Replace(string(valid), `"enabled":true`, `"enabled":"true"`, 1),
		strings.Replace(string(valid), `"enabled":true`, `"enabled":true,"enabled":false`, 1),
		strings.Replace(string(valid), `"enabled":true`, `"Enabled":true`, 1),
		strings.Replace(string(valid), `"features":[]`, `"features":null`, 1),
		strings.Replace(string(valid), `"features":[]`, `"features":[null]`, 1),
		strings.Replace(string(valid), `"features":[]`, `"features":["users"]`, 1),
		strings.Replace(string(valid), `"features":[]`, `"features":["usage","usage"]`, 1),
		strings.Replace(string(valid), `"account_ids":[]`, `"account_ids":["\ud800"]`, 1),
		strings.Replace(string(valid), `"revision":"1"`, `"revision":"9223372036854775808"`, 1),
		strings.Replace(string(valid), `"revision":"1"`, `"revision":1`, 1),
		strings.TrimSuffix(string(valid), "}") + `,"role":"admin"}`,
		strings.Repeat(" ", 65537),
	} {
		w := request(h, "POST", "/api/v1/users/update", body, origin, adminCookie)
		if w.Code != 400 {
			t.Fatalf("malformed user update accepted: %d", w.Code)
		}
	}
	for _, action := range []struct{ name, body string }{
		{"list", `{"after":"","search":"","limit":10}`}, {"get", `{"id":"` + user.ID + `"}`},
		{"create", `{"username":"http.created","password":"OriginalPassword123","enabled":true,"features":[],"account_ids":[],"participant_ids":[]}`},
		{"update", string(valid)},
		{"delete", `{"id":"` + user.ID + `","revision":"1","confirmation":"http.reader"}`},
	} {
		path := "/api/v1/users/" + action.name
		if w := request(h, "POST", path, action.body, "https://attacker.invalid", adminCookie); w.Code != 403 {
			t.Fatal("user action accepted foreign Origin", action.name)
		}
		if w := request(h, "GET", path, "", "", adminCookie); w.Code != 405 {
			t.Fatal("user action accepted wrong method", action.name)
		}
		if w := request(h, "POST", path, action.body, origin, memberCookie); w.Code != 403 {
			t.Fatal("ordinary identity crossed HTTP management boundary", action.name, w.Code)
		}
		if w := request(h, "POST", path+"?", action.body, origin, adminCookie); w.Code != 400 {
			t.Fatal("user action ignored bare query", action.name)
		}
	}
	for _, cookie := range []string{"", memberCookie + "; __Host-sub_pool_session=forged", memberCookie + "; __Host-sub_pool_session=\""} {
		if w := request(h, "GET", "/api/v1/access", "", "", cookie); w.Code != 401 {
			t.Fatal("ambiguous or missing cookie authenticated")
		}
	}
	if w := request(h, "GET", "/api/v1/access", "{}", "", memberCookie); w.Code != 400 {
		t.Fatal("access read accepted a body")
	}
	w := request(h, "GET", "/api/v1/access", "", "", memberCookie)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("actual access read failed", w.Code)
	}
	var access Access
	if json.Unmarshal(w.Body.Bytes(), &access) != nil || access.User.ID != user.ID || access.AllData {
		t.Fatal("access read invented role")
	}
	for _, forbidden := range []string{"password_hash", "OriginalPassword123", member, admin, "subpool_test"} {
		if strings.Contains(w.Body.String(), forbidden) {
			t.Fatal("access output leaked secret")
		}
	}
	w = request(h, "POST", "/api/v1/users/update", string(valid), origin, adminCookie)
	if w.Code != 200 {
		t.Fatal("valid user update failed", w.Code, w.Body.String())
	}
	if w = request(h, "POST", "/api/v1/users/update", string(valid), origin, adminCookie); w.Code != 409 {
		t.Fatal("HTTP stale edit was not a defined conflict", w.Code)
	}
	exec(t, pool, `CREATE FUNCTION reject_http_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'private database diagnostic'; END $$;
		CREATE TRIGGER reject_http_event BEFORE INSERT ON auth_events FOR EACH ROW EXECUTE FUNCTION reject_http_event()`)
	w = request(h, "POST", "/api/v1/users/create", `{"username":"http.rollback","password":"OriginalPassword123","enabled":true,"features":[],"account_ids":[],"participant_ids":[]}`, origin, adminCookie)
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "private database diagnostic") || strings.Contains(w.Body.String(), "OriginalPassword123") {
		t.Fatal("internal user error was exposed or falsely successful")
	}
}
