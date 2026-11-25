package backups

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sub-pool/backend/internal/auth"
)

const testOrigin = "http://127.0.0.1:4200"

func httpRequest(handler http.Handler, path, body, cookie, authorization, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, testOrigin+path, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:51321"
	r.Header.Set("Content-Type", "application/json")
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if cookie != "" {
		r.Header.Set("Cookie", cookie)
	}
	if authorization != "" {
		r.Header.Set("Authorization", authorization)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}
func browser(t *testing.T, f fixture) (*Handler, string) {
	t.Helper()
	boundary, err := auth.NewHandler(f.identity, testOrigin, true)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"username": f.admin.Username, "password": fixturePassword})
	w := httpRequest(boundary, "/api/v1/auth/login", string(body), "", "", testOrigin)
	if w.Code != 200 || len(w.Result().Cookies()) != 1 {
		t.Fatal("native HTTP login failed", w.Code)
	}
	cookie := w.Result().Cookies()[0]
	return &Handler{Service: f.s, Boundary: boundary}, cookie.Name + "=" + cookie.Value
}
func jsonBody(value any) string { body, _ := json.Marshal(value); return string(body) }

func TestBackupHTTPCurrentAdminOriginSourceAndClosedMachineCatalog(t *testing.T) {
	f := setup(t)
	h, cookie := browser(t, f)
	key, err := f.identity.IssueAPIKey(context.Background(), f.token, "owned recovery exclusion", "management")
	if err != nil {
		t.Fatal(err)
	}
	file := export(t, f, false)
	p := preflight(t, f, file)
	requests := map[string]any{
		"export": map[string]any{"request_id": id(t), "include_secrets": false}, "get": map[string]any{"id": file.Archive.ID}, "list": map[string]any{"after": "", "limit": 10},
		"preflight": map[string]any{"request_id": id(t), "content": file.Content}, "plan": map[string]any{"id": p.ID}, "apply": ApplyInput{p.ID, p.TargetBasis, true}, "checks": map[string]any{"id": p.ID}, "review": map[string]any{"id": p.ID, "acknowledged": true},
	}
	for path, input := range requests {
		t.Run(path, func(t *testing.T) {
			body := jsonBody(input)
			route := "/api/v1/backups/" + path
			if w := httpRequest(h, route, body, "", "", testOrigin); w.Code != 401 {
				t.Fatal("missing session accepted", w.Code)
			}
			if w := httpRequest(h, route, body, cookie, "", "http://127.0.0.1:4201"); w.Code != 403 {
				t.Fatal("foreign Origin accepted", w.Code)
			}
			if w := httpRequest(h, route, body, "", "Bearer "+key.Secret, ""); w.Code != 403 {
				t.Fatal("management key reached recovery domain", w.Code)
			}
		})
	}
	if w := httpRequest(h, "/api/v1/backups/get", jsonBody(requests["get"]), cookie, "", testOrigin); w.Code != 200 {
		t.Fatal("current admin cannot download actual encrypted file", w.Code)
	}
	exec(t, f.pool, `INSERT INTO source_blocks(ip,revision,blocked,saved_at,actor_id) VALUES('127.0.0.1',1,true,clock_timestamp(),$1)`, f.admin.ID)
	if w := httpRequest(h, "/api/v1/backups/list", jsonBody(requests["list"]), cookie, "", testOrigin); w.Code != 403 {
		t.Fatal("blocked source reached backups", w.Code)
	}
	exec(t, f.pool, `DELETE FROM source_blocks WHERE ip='127.0.0.1'`)
	exec(t, f.pool, `UPDATE auth_users SET role='user',revision=revision+1 WHERE id=$1`, f.admin.ID)
	for path, input := range requests {
		if w := httpRequest(h, "/api/v1/backups/"+path, jsonBody(input), cookie, "", testOrigin); w.Code != 403 {
			t.Fatal("current ordinary user reached recovery domain", path, w.Code)
		}
	}
	var exports, applying int
	if err = f.pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM backup_archives WHERE kind='export'),(SELECT count(*) FROM restore_plans WHERE state IN ('applying','applied'))`).Scan(&exports, &applying); err != nil || exports != 1 || applying != 0 {
		t.Fatal("denied request mutated backup intent", exports, applying, err)
	}
}

func TestBackupHTTPTransportsActualLargeFileAndKeepsStrictJSON(t *testing.T) {
	f := setup(t)
	h, cookie := browser(t, f)
	exec(t, f.pool, `INSERT INTO auth_login_limits(bucket,window_start) VALUES($1,clock_timestamp())`, strings.Repeat("x", 100000))
	file := export(t, f, false)
	if len(file.Content) <= 65536 {
		t.Fatal("fixture does not cross original JSON bound")
	}
	w := httpRequest(h, "/api/v1/backups/preflight", jsonBody(map[string]any{"request_id": id(t), "content": file.Content}), cookie, "", testOrigin)
	var p Plan
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &p) != nil || p.State != "ready" || p.BeforeArchiveID == nil {
		t.Fatal("actual file was not accepted through expanded upload boundary", w.Code)
	}
	for _, body := range []string{
		`{"request_id":"` + id(t) + `","filename":"name-only.spbak"}`,
		`{"request_id":"` + id(t) + `","content":null}`,
		`{"request_id":"` + id(t) + `","content":"bad","content":"bad"}`,
		`{"request_ID":"` + id(t) + `","content":"bad"}`,
	} {
		if w = httpRequest(h, "/api/v1/backups/preflight", body, cookie, "", testOrigin); w.Code != 400 {
			t.Fatal("loose upload JSON accepted", w.Code)
		}
	}
	w = httpRequest(h, "/api/v1/backups/preflight", jsonBody(map[string]any{"request_id": id(t), "content": strings.Repeat("x", maxJSON)}), cookie, "", testOrigin)
	if w.Code != 400 && w.Code != 413 {
		t.Fatal("HTTP transport bound ignored", w.Code)
	}
	called := false
	ordinary := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.Boundary.HandleJSON(w, r, http.MethodPost, []string{"payload"}, func(context.Context, string, map[string]json.RawMessage) (any, error) {
			called = true
			return struct{}{}, nil
		})
	})
	w = httpRequest(ordinary, "/api/v1/ordinary", jsonBody(map[string]string{"payload": strings.Repeat("x", 65536)}), cookie, "", testOrigin)
	if w.Code != 400 || called {
		t.Fatal("default domain body bound was widened", w.Code)
	}
}

func TestBackupHTTPPreflightRejectsImportedCurrentSourceBlock(t *testing.T) {
	f := setup(t)
	h, cookie := browser(t, f)
	for _, tc := range []struct{ offset, state string }{{"", "blocked"}, {"1 hour", "blocked"}, {"-1 second", "ready"}} {
		// Represent a valid archive exported by an operator from a different network.
		exec(t, f.pool, `INSERT INTO source_blocks(ip,revision,blocked,saved_at,actor_id,reason,expires_at)
			VALUES('127.0.0.1',1,true,clock_timestamp(),$1,'maintenance',clock_timestamp()+NULLIF($2,'')::interval)`, f.admin.ID, tc.offset)
		file := export(t, f, false)
		exec(t, f.pool, `DELETE FROM source_blocks WHERE ip='127.0.0.1'`)
		w := httpRequest(h, "/api/v1/backups/preflight", jsonBody(map[string]any{"request_id": id(t), "content": file.Content}), cookie, "", testOrigin)
		var p Plan
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &p) != nil || p.State != tc.state ||
			contains(p.Blockers, "BACKUP_SOURCE_BLOCKED") != (tc.state == "blocked") || (p.BeforeArchiveID != nil) != (tc.state == "ready") {
			t.Fatal("restore source gate disagreed with the candidate deadline", w.Code, p.State, p.Blockers)
		}
	}
}
