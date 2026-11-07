package connection

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sub-pool/backend/internal/auth"
)

const pricingTestID = "9223372036854775807"
const pricingTestGroup = `{"id":9223372036854775807,"name":"Native group","platform":"openai","status":"active","account_count":1,"rate_multiplier":1.25,"force_openai_fast":true,"free_openai_fast":false,"long_context_pricing_enabled":true,"model_pricing":[{"platform":"openai","models":["model-a*"],"billing_mode":"token","input_price":0.000000000000000123,"output_price":2.5e-6,"cache_write_price":null,"cache_write_1h_price":0,"cache_read_price":0.000002,"fast_multiplier":2,"flex_multiplier":null,"image_input_price":null,"image_output_price":null,"per_request_price":null,"intervals":[{"min_tokens":0,"max_tokens":100,"input_price":1}],"time_pricing":null,"reasoning_effort_multipliers":{}}],"credentials":{"token":"PRIVATE_UPSTREAM_BODY"}}`

func pricingTestPage(item string) string {
	return `{"code":0,"data":{"items":[` + item + `],"total":1,"page":1,"page_size":100}}`
}

func TestPricingProjectsExactNativeConfigurationWithoutResolvingComplexRules(t *testing.T) {
	group, ok := projectPricingGroup(json.RawMessage(pricingTestGroup), "secret-not-returned")
	config, valid := projectPricingConfiguration(json.RawMessage(pricingTestGroup), "secret-not-returned")
	if !ok || !valid || group.ID != pricingTestID || group.AccountCount == nil || *group.AccountCount != "1" || config.ModelRules == nil || len(*config.ModelRules) != 1 {
		t.Fatal("native group projection lost identity or explicit values", group, config)
	}
	rule := (*config.ModelRules)[0]
	if *rule.Values["input_price"] != "0.000000000000000123" || *rule.Values["output_price"] != "0.0000025" || *rule.Values["cache_write_1h_price"] != "0" || rule.Values["cache_write_price"] != nil || !reflect.DeepEqual(rule.Unprojected, []string{"intervals"}) || len(config.Unreported) != 0 || config.Projection != "base_fields_only" {
		t.Fatal("projection rounded money, invented inheritance, or hid complex rules", rule, config)
	}
	partial, valid := projectPricingConfiguration(json.RawMessage(`{"id":1,"model_allowlist":{"enabled":true}}`), "secret")
	if !valid || partial.ModelRules != nil || partial.FreeOpenAIFast != nil || len(partial.Unreported) != 5 {
		t.Fatal("restricted model DTO implied pricing visibility", partial)
	}
	for _, rules := range []string{"null", "[]"} {
		empty, valid := projectPricingConfiguration(json.RawMessage(`{"model_pricing":`+rules+`}`), "secret")
		if !valid || empty.ModelRules == nil || len(*empty.ModelRules) != 0 || len(empty.Unreported) != 4 {
			t.Fatal("explicit native no-override was lost or confused with missing pricing", rules, empty)
		}
	}
	for _, bad := range []string{
		`{"model_pricing":{}}`,
		`{"model_pricing":"null"}`,
		strings.Replace(pricingTestGroup, `"rate_multiplier":1.25`, `"rate_multiplier":null`, 1),
		strings.Replace(pricingTestGroup, `"rate_multiplier":1.25`, `"rate_multiplier":-1`, 1),
		strings.Replace(pricingTestGroup, `"input_price":0.000000000000000123`, `"input_price":"0.01"`, 1),
		strings.Replace(pricingTestGroup, `"input_price":0.000000000000000123`, `"input_price":1e9999`, 1),
		strings.Replace(pricingTestGroup, `"force_openai_fast":true`, `"force_openai_fast":null`, 1),
		strings.Replace(pricingTestGroup, `"models":["model-a*"]`, `"models":["secret"]`, 1),
		strings.Replace(pricingTestGroup, `"rate_multiplier":1.25`, `"rate_multiplier":1,"rate_multiplier":2`, 1),
	} {
		if _, valid := projectPricingConfiguration(json.RawMessage(bad), "secret"); valid {
			t.Fatal("invalid or secret-bearing configuration accepted")
		}
	}
}

func TestPricingReadsCurrentSourceGroupsAssociationsDefaultsAndSurvivesServiceReplacement(t *testing.T) {
	s, db, token := setup(t)
	ctx := context.Background()
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.Header.Get("x-api-key") != "adm_sc_test_only_secret" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("pricing forwarded credentials incorrectly or dispatched a mutation")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/admin/groups":
			if r.URL.Query().Get("platform") != "openai" || r.URL.Query().Get("search") != "native & group" {
				t.Error("target filter changed")
			}
			_, _ = w.Write([]byte(pricingTestPage(pricingTestGroup)))
		case "/api/v1/admin/groups/" + pricingTestID:
			_, _ = w.Write([]byte(`{"code":0,"data":` + pricingTestGroup + `}`))
		case "/api/v1/admin/accounts":
			if r.URL.Query().Get("group") != pricingTestID || r.URL.Query().Get("platform") != "openai" {
				t.Error("association scope changed")
			}
			_, _ = w.Write([]byte(pricingTestPage(`{"id":17,"name":"Visible account","platform":"openai","type":"oauth","status":"active","schedulable":true,"group_ids":[9223372036854775807],"credentials":{"token":"PRIVATE_UPSTREAM_BODY"}}`)))
		case "/api/v1/admin/channels/model-pricing":
			if r.URL.Query().Get("model") != "model-a & name" {
				t.Error("model query not encoded")
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"found":true,"input_price":1.25e-6,"output_price":0,"cache_write_price":null,"cache_write_1h_price":null,"cache_read_price":0.000000000000000123,"image_input_price":0,"image_output_price":0,"reasoning_effort_multipliers":{"high":2},"secret":"PRIVATE_UPSTREAM_BODY"}}`))
		default:
			t.Error("unexpected upstream path", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c := save(t, s, token, server.URL)
	input := PricingGroupsInput{Revision: c.Revision, SourceID: *c.SourceID, Page: 1, Search: "native & group"}
	groups, err := s.ListPricingGroups(ctx, token, input)
	if err != nil || groups.Read.State != "available" || len(groups.Groups) != 1 || groups.Groups[0].ID != pricingTestID || groups.RunMode != nil || groups.ModeRead.Reason != "MODE_REQUIRES_JWT" || groups.CanApply {
		t.Fatal("groups not safely read", groups, err)
	}
	detailInput := PricingGroupInput{Revision: c.Revision, SourceID: *c.SourceID, GroupID: pricingTestID, Page: 1}
	detail, err := s.ReadPricingGroup(ctx, token, detailInput)
	if err != nil || detail.Read.State != "available" || detail.Configuration == nil || detail.AccountsRead.State != "available" || len(detail.Accounts) != 1 || detail.Total == nil || *detail.Total != "1" || detail.NextPage != nil || detail.CanApply {
		t.Fatal("group scope/configuration not read", detail, err)
	}
	defaults, err := s.ReadPricingDefault(ctx, token, PricingDefaultInput{Revision: c.Revision, SourceID: *c.SourceID, Model: "model-a & name"})
	if err != nil || defaults.Found == nil || !*defaults.Found || defaults.Values["input_price"] == nil || *defaults.Values["input_price"] != "0.00000125" || defaults.CanApply {
		t.Fatal("native defaults unavailable", defaults, err)
	}
	encoded, _ := json.Marshal([]any{groups, detail, defaults})
	if bytes.Contains(encoded, []byte("PRIVATE_UPSTREAM_BODY")) || bytes.Contains(encoded, []byte("adm_sc_test_only_secret")) {
		t.Fatal("raw upstream data escaped projection")
	}
	restarted, err := New(db, auth.New(db), bytes.Repeat([]byte{7}, 32), true)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := restarted.ReadPricingGroup(ctx, token, detailInput); err != nil || got.Configuration == nil || !reflect.DeepEqual(got.Configuration, detail.Configuration) {
		t.Fatal("replacement lost source or encrypted credential", got, err)
	}
	before := calls.Load()
	input.Revision = "1"
	if _, err = s.ListPricingGroups(ctx, token, input); !errors.Is(err, auth.ErrConflict) || calls.Load() != before {
		t.Fatal("stale revision contacted upstream", err)
	}
	member, err := s.auth.CreateUser(ctx, token, auth.UserCreate{Username: "pricing-reader", Password: "ReaderPassword123", Enabled: true, Grants: auth.Grants{Features: []string{"pricing"}, AccountIDs: []string{}, ParticipantIDs: []string{}}})
	if err != nil {
		t.Fatal(err)
	}
	_, reader, err := s.auth.Login(ctx, member.Username, "ReaderPassword123", netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReadPricingGroup(ctx, reader, detailInput); !errors.Is(err, auth.ErrForbidden) || calls.Load() != before {
		t.Fatal("ordinary feature grant exposed global native pricing", err)
	}
	exec(t, db, "UPDATE auth_users SET role='user' WHERE username='admin'")
	if _, err = s.ReadPricingGroup(ctx, token, detailInput); !errors.Is(err, auth.ErrForbidden) || calls.Load() != before {
		t.Fatal("former admin retained pricing read", err)
	}
}

func TestPricingKeepsPartialDenialsUnknownFieldsAndModeSeparateFromWriteAuthority(t *testing.T) {
	s, _, token := setup(t)
	ctx := context.Background()
	var scenario atomic.Value
	scenario.Store("simple")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != "GET" {
			t.Error("pricing dispatched a mutation")
		}
		current := scenario.Load().(string)
		switch r.URL.Path {
		case "/api/v1/auth/me":
			mode := "standard"
			if current == "simple" {
				mode = "simple"
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"role":"admin","run_mode":"` + mode + `"}}`))
		case "/api/v1/admin/groups/" + pricingTestID:
			body := pricingTestGroup
			if current == "partial" {
				body = `{"id":9223372036854775807,"name":"Restricted group","platform":"openai","model_allowlist":{}}`
			}
			if current == "identity" {
				body = strings.Replace(body, pricingTestID, "1", 1)
			}
			if current == "platform" {
				body = strings.Replace(body, `"platform":"openai"`, `"platform":"anthropic"`, 1)
			}
			_, _ = w.Write([]byte(`{"code":0,"data":` + body + `}`))
		case "/api/v1/admin/accounts":
			if current == "association" {
				_, _ = w.Write([]byte(pricingTestPage(`{"id":17,"name":"Wrong group account","platform":"openai","type":"oauth","status":"active","schedulable":true,"group_ids":[1]}`)))
				return
			}
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"secret":"pricing-jwt-secret"}`))
		case "/api/v1/admin/channels/model-pricing":
			if current == "missing" {
				_, _ = w.Write([]byte(`{"code":0,"data":{"found":false}}`))
				return
			}
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"secret":"pricing-jwt-secret"}`))
		default:
			w.WriteHeader(403)
		}
	}))
	defer server.Close()
	c := save(t, s, token, server.URL)
	saved, err := s.Save(ctx, token, SaveInput{Revision: c.Revision, BaseURL: server.URL, CredentialAction: "replace", Credential: "Bearer pricing-jwt-secret"})
	if err != nil {
		t.Fatal(err)
	}
	c = saved.Configuration
	input := PricingGroupInput{Revision: c.Revision, SourceID: *c.SourceID, GroupID: pricingTestID, Page: 1}
	for _, name := range []string{"simple", "standard", "partial", "identity", "platform", "association"} {
		scenario.Store(name)
		got, err := s.ReadPricingGroup(ctx, token, input)
		if err != nil || got.CanApply || got.WriteReason != "PRICING_WRITE_UNQUALIFIED" || got.RunMode == nil {
			t.Fatal(name, got, err)
		}
		if name == "identity" || name == "platform" {
			if got.Read.Reason != "INVALID_RESPONSE" || got.Group != nil || got.Configuration != nil || got.AccountsRead.Reason != "GROUP_NOT_VERIFIED" {
				t.Fatal("unverified identity contacted association route", got)
			}
		} else if name == "association" {
			if got.AccountsRead.Reason != "INVALID_RESPONSE" || len(got.Accounts) != 0 || got.Total != nil {
				t.Fatal("wrong group association leaked", got)
			}
		} else if got.AccountsRead.State != "denied" || got.Configuration == nil || got.Total != nil {
			t.Fatal("partial denial hid valid config or became empty success", got)
		}
		if name == "partial" && (got.Configuration == nil || len(got.Configuration.Unreported) != 5 || got.Configuration.ModelRules != nil) {
			t.Fatal("model allowlist key acquired pricing read capability", got)
		}
	}
	defInput := PricingDefaultInput{Revision: c.Revision, SourceID: *c.SourceID, Model: "unlisted-model"}
	if got, err := s.ReadPricingDefault(ctx, token, defInput); err != nil || got.Found != nil || got.Values != nil || got.Read.State != "denied" {
		t.Fatal("denied default became zero", got, err)
	}
	scenario.Store("missing")
	if got, err := s.ReadPricingDefault(ctx, token, defInput); err != nil || got.Found == nil || *got.Found || got.Values != nil {
		t.Fatal("missing default became zero", got, err)
	}
}

func TestPricingDiscardsProtectedResultAfterObservedNetworkWaitAndSessionExpiry(t *testing.T) {
	s, db, token := setup(t)
	entered, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(pricingTestPage(pricingTestGroup)))
	}))
	defer server.Close()
	c := save(t, s, token, server.URL)
	digest := sha256.Sum256([]byte(token))
	deadline := time.Now().Add(time.Second)
	exec(t, db, "UPDATE auth_sessions SET idle_expires_at=$2 WHERE digest=$1", digest[:], deadline)
	result := make(chan error, 1)
	go func() {
		out, err := s.ListPricingGroups(context.Background(), token, PricingGroupsInput{Revision: c.Revision, SourceID: *c.SourceID, Page: 1})
		if len(out.Groups) != 0 {
			result <- errors.New("expired session received protected result")
			return
		}
		result <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("native barrier not observed")
	}
	if delay := time.Until(deadline) + 20*time.Millisecond; delay > 0 {
		time.Sleep(delay)
	}
	close(release)
	if err := <-result; !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatal("network wait bypassed session expiry", err)
	}
}
