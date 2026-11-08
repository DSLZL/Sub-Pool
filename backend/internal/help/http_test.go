package help

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sub-pool/backend/internal/auth"
)

func TestHelpHTTPKeepsStrictSessionOriginSourceAndMachineBoundaries(t *testing.T) {
	f := setup(t)
	const origin = "http://127.0.0.1:4300"
	boundary, err := auth.NewHandler(f.identity, origin, true)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{Service: f.s, Boundary: boundary}
	request := func(path, body, token, requestOrigin, bearer string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, origin+path, strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:51002"
		r.Header.Set("Content-Type", "application/json")
		if requestOrigin != "" {
			r.Header.Set("Origin", requestOrigin)
		}
		if token != "" {
			r.Header.Set("Cookie", "sub_pool_dev_session="+token)
		}
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, tc := range []struct {
		body, token, origin string
		status              int
	}{
		{"{}", f.token, origin, 200}, {"{}", "", origin, 401}, {"{}", f.token, "", 403}, {"{}", f.token, "https://other.invalid", 403},
		{"{\"extra\":1}", f.token, origin, 400}, {"{} {}", f.token, origin, 400}, {"null", f.token, origin, 400},
	} {
		if w := request("/api/v1/help/catalog", tc.body, tc.token, tc.origin, ""); w.Code != tc.status {
			t.Fatal("catalogue boundary", tc.status, w.Code)
		}
	}
	a := announcements()[0]
	body, _ := json.Marshal(map[string]string{"id": a.ID, "content_sha256": a.ContentSHA256})
	for _, bad := range []string{string(body[:len(body)-1]) + ",\"id\":\"" + a.ID + "\"}", strings.Replace(string(body), "\"id\"", "\"ID\"", 1), strings.Replace(string(body), "\""+a.ID+"\"", "null", 1), strings.Repeat(" ", 65536) + string(body)} {
		if w := request("/api/v1/help/read", bad, f.token, origin, ""); w.Code != 400 {
			t.Fatal("invalid acknowledgement accepted", w.Code)
		}
	}
	key, err := f.identity.IssueAPIKey(context.Background(), f.token, "owned help exclusion", "management")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/help/catalog", "/api/v1/help/read"} {
		if w := request(path, "{}", "", "", key.Secret); w.Code != 403 {
			t.Fatal("machine reached human help", w.Code)
		}
	}
	exec(t, f, "INSERT INTO source_blocks(ip,revision,blocked,saved_at,actor_id) VALUES('127.0.0.1',1,true,clock_timestamp(),$1)", f.admin.ID)
	if w := request("/api/v1/help/read", string(body), f.token, origin, ""); w.Code != 403 {
		t.Fatal("blocked source acknowledged", w.Code)
	}
	exec(t, f, "DELETE FROM source_blocks WHERE ip='127.0.0.1'")
	if w := request("/api/v1/help/read", string(body), f.token, origin, ""); w.Code != 200 {
		t.Fatal("real acknowledgement failed", w.Code)
	}
}
