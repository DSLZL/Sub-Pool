package connection

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"sub-pool/backend/internal/fakesub2api"
)

func TestUnverifiedSub2APIVersionWithKeyAndJWT(t *testing.T) {
	s, pool, token := setup(t)
	ctx := context.Background()
	fixture := fakesub2api.New(fakesub2api.Options{Version: "0.2.24"})
	var administratorReads atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Cookie") != "" {
			t.Error("diagnostic sent an unexpected upstream method or session")
			http.Error(w, "unexpected diagnostic request", http.StatusBadRequest)
			return
		}
		if r.URL.RequestURI() == keyRunModePath {
			t.Error("diagnostic probed an unqualified version with a management Key")
		}
		if r.URL.Path == "/api/v1/auth/me" {
			administratorReads.Add(1)
			if r.Header.Get("Authorization") != "Bearer "+fakesub2api.BearerToken || r.Header.Get("x-api-key") != "" {
				t.Error("administrator profile received an unexpected credential")
			}
		}
		fixture.ServeHTTP(w, r)
	}))
	defer upstream.Close()

	assertVersion := func(d Diagnostic, err error, saved bool) {
		t.Helper()
		if err != nil || d.Saved != saved || d.Version == nil || *d.Version != "0.2.24" {
			t.Fatalf("incorrect diagnostic: %+v %v", d, err)
		}
		version := checkByID(t, d, "version.read")
		if version.State != "available" || version.Reason != "VERSION_UNVERIFIED" ||
			version.HTTPStatus == nil || *version.HTTPStatus != http.StatusOK {
			t.Fatalf("incorrect version qualification: %+v", version)
		}
		for _, id := range []string{"wallet.write", "scheduling.write", "pricing.write"} {
			if check := checkByID(t, d, id); check.State != "unverified" || check.Evidence != nil {
				t.Fatalf("diagnostic granted a write capability: %+v", check)
			}
		}
	}
	assertJWT := func(d Diagnostic) {
		t.Helper()
		if mode := checkByID(t, d, "run_mode"); d.RunMode == nil || *d.RunMode != "standard" ||
			mode.State != "available" || mode.Reason != "VERIFIED_READ" {
			t.Fatalf("missing administrator mode evidence: %+v", mode)
		}
	}

	c := save(t, s, token, upstream.URL)
	input := TestInput{Revision: c.Revision, BaseURL: upstream.URL}
	keyResult, err := s.Test(ctx, token, input)
	assertVersion(keyResult, err, true)
	if mode := checkByID(t, keyResult, "run_mode"); keyResult.RunMode != nil ||
		mode.State != "unverified" || mode.Reason != "MODE_REQUIRES_JWT" || administratorReads.Load() != 0 {
		t.Fatalf("management Key established an unexpected mode: %+v", mode)
	}

	input.Credential = "Bearer " + fakesub2api.BearerToken
	preview, err := s.Test(ctx, token, input)
	assertVersion(preview, err, false)
	assertJWT(preview)

	updated, err := s.Save(ctx, token, SaveInput{Revision: c.Revision, BaseURL: upstream.URL,
		CredentialAction: "replace", Credential: input.Credential})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := s.Test(ctx, token, TestInput{Revision: updated.Configuration.Revision, BaseURL: upstream.URL})
	assertVersion(saved, err, true)
	assertJWT(saved)
	if administratorReads.Load() != 2 {
		t.Fatalf("unexpected count of administrator profile reads: %d", administratorReads.Load())
	}

	saved.capability("version.read").Reason = "VERIFIED_READ"
	legacy, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, "UPDATE connection_config SET diagnostic=$1 WHERE singleton", legacy); err != nil {
		t.Fatal(err)
	}
	view, err := s.Get(ctx, token)
	if err != nil || view.Diagnostic == nil {
		t.Fatalf("missing retained diagnostic: %v", err)
	}
	assertVersion(*view.Diagnostic, nil, true)
	assertJWT(*view.Diagnostic)
	var persistedBytes []byte
	if err := pool.QueryRow(ctx, "SELECT diagnostic FROM connection_config WHERE singleton").Scan(&persistedBytes); err != nil {
		t.Fatal(err)
	}
	var persisted Diagnostic
	if err := json.Unmarshal(persistedBytes, &persisted); err != nil || checkByID(t, persisted, "version.read").Reason != "VERIFIED_READ" {
		t.Fatalf("readback rewrote the historical diagnostic: %v", err)
	}
}
