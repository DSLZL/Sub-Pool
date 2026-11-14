package connection

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const simulationTestEpoch = "AgICAgICAgICAgICAgICAg"

func simulationConfig() storedConfig {
	id, kind := "AQEBAQEBAQEBAQEBAQEBAQ", "api_key"
	return storedConfig{Configuration: Configuration{SourceID: &id, Revision: "1", BaseURL: "http://127.0.0.1:1234",
		CredentialSet: true, CredentialKind: &kind, SecretStorageAvailable: true}}
}

func TestSimulationBindingIsExplicitLoopbackSourceRevisionAndEpoch(t *testing.T) {
	s := &Service{allowHTTP: true}
	c := simulationConfig()
	active := storedAccount{ManagedAccount: ManagedAccount{SourceMode: "active"}}
	if s.collectionSourceAvailable(active, c) {
		t.Fatal("native active gate changed without an explicit simulation binding")
	}
	binding := *c.SourceID + ":" + c.Revision + ":" + simulationTestEpoch
	if err := s.ConfigureSimulation(binding); err != nil || s.simulationEpoch(c) != simulationTestEpoch || !s.collectionSourceAvailable(active, c) {
		t.Fatal("valid simulation binding unavailable", err)
	}
	for _, bad := range []string{"anything", *c.SourceID + ":01:" + simulationTestEpoch, *c.SourceID + ":1:invalid", binding + ":extra"} {
		if (&Service{allowHTTP: true}).ConfigureSimulation(bad) == nil {
			t.Fatal("malformed simulation binding accepted")
		}
	}
	if (&Service{}).ConfigureSimulation(binding) == nil {
		t.Fatal("simulation enabled without explicit loopback HTTP")
	}
	for _, change := range []func(*storedConfig){
		func(c *storedConfig) { c.Revision = "2" },
		func(c *storedConfig) { c.SourceID = nil },
		func(c *storedConfig) { c.CredentialKind = nil },
		func(c *storedConfig) { c.CredentialSet = false },
		func(c *storedConfig) { c.SecretStorageAvailable = false },
		func(c *storedConfig) { c.reverificationRequired = true },
		func(c *storedConfig) { c.BaseURL = "https://127.0.0.1:1234" },
		func(c *storedConfig) { c.BaseURL = "http://localhost:1234" },
		func(c *storedConfig) { c.BaseURL = "http://example.test:1234" },
		func(c *storedConfig) { c.BaseURL = "http://user@127.0.0.1:1234" },
	} {
		current := c
		change(&current)
		if s.simulationEpoch(current) != "" {
			t.Fatal("unbound or non-loopback configuration retained simulation capability")
		}
	}
}

func TestSimulationQuotaRequiresCanonicalReadOnlyProvenance(t *testing.T) {
	s := &Service{allowHTTP: true}
	c := simulationConfig()
	if err := s.ConfigureSimulation(*c.SourceID + ":1:" + simulationTestEpoch); err != nil {
		t.Fatal(err)
	}
	good := map[string]any{"protocol": simulationProtocol, "environment": "simulated", "epoch": simulationTestEpoch, "read_only": true,
		"account": fullManagedAccount(1, "simulation-resource")}
	body, _ := json.Marshal(good)
	account, provenance, ok := s.simulationQuota(body, c)
	if !ok || provenance.Environment != "simulated" || !bytes.Contains(account, []byte("simulation-resource")) {
		t.Fatal("valid simulation provenance lost")
	}
	for _, key := range []string{"protocol", "environment", "epoch", "read_only"} {
		for _, value := range []any{nil, "wrong", false} {
			mutated := map[string]any{}
			for k, v := range good {
				mutated[k] = v
			}
			mutated[key] = value
			data, _ := json.Marshal(mutated)
			_, p, valid := s.simulationQuota(data, c)
			if valid || p != nil {
				t.Fatalf("invalid %s accepted", key)
			}
		}
	}
	for _, raw := range []string{
		strings.Replace(string(body), "\"read_only\":true", "\"read_only\":true,\"read_only\":false", 1),
		strings.Replace(string(body), "\"environment\":", "\"Environment\":", 1),
		strings.TrimSuffix(string(body), "}") + ",\"complete\":true}",
		string(body) + "{}",
	} {
		if _, _, valid := s.simulationQuota([]byte(raw), c); valid {
			t.Fatal("ambiguous or caller-qualified simulation response accepted")
		}
	}
}

type simulationFixture struct {
	mode    atomic.Int32
	active  atomic.Int32
	normal  atomic.Int32
	block   atomic.Bool
	arrived chan struct{}
	release chan struct{}
}

func simulationServer(t *testing.T) (*httptest.Server, *simulationFixture) {
	t.Helper()
	f := &simulationFixture{arrived: make(chan struct{}), release: make(chan struct{})}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("Cookie") != "" || r.Header.Get("x-api-key") != "adm_sc_test_only_secret" {
			t.Error("simulation exceeded fixed credential/method boundary")
			http.Error(w, "denied", 403)
			return
		}
		active := r.URL.Path == simulationPrefix+"/accounts/1/quota"
		if !active && r.URL.Path != "/api/v1/admin/accounts/1" {
			t.Error("collector dispatched an unapproved native quota route")
			http.Error(w, "unexpected", 400)
			return
		}
		resource := "simulation-resource"
		if active && f.mode.Load() == 2 {
			resource = "different-resource"
		}
		account := fullManagedAccount(1, resource)
		account["extra"] = map[string]any{"codex_usage_updated_at": time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano),
			"codex_5h_used_percent": 0, "codex_5h_window_minutes": 180, "codex_5h_reset_at": time.Now().UTC().Add(time.Hour).Format(time.RFC3339)}
		if !active {
			f.normal.Add(1)
			respond(w, account)
			return
		}
		f.active.Add(1)
		if f.block.Load() {
			close(f.arrived)
			<-f.release
		}
		epoch := simulationTestEpoch
		if f.mode.Load() == 1 {
			epoch = "AwMDAwMDAwMDAwMDAwMDAw"
		}
		respond(w, map[string]any{"protocol": simulationProtocol, "environment": "simulated", "epoch": epoch, "read_only": true, "account": account})
	}))
	t.Cleanup(server.Close)
	return server, f
}

func bindSimulation(t *testing.T, s *Service, c Configuration) {
	t.Helper()
	if err := s.ConfigureSimulation(*c.SourceID + ":" + c.Revision + ":" + simulationTestEpoch); err != nil {
		t.Fatal(err)
	}
}

func TestSimulationActiveCollectorPreservesProvenanceCooldownFailureAndReplacement(t *testing.T) {
	s, pool, token := setup(t)
	ctx := context.Background()
	server, f := simulationServer(t)
	c := save(t, s, token, server.URL)
	a := adoptManaged(t, s, token, c, 1, "Simulation")
	bindSimulation(t, s, c)
	run := queueAccount(t, s, token, c, a.ID)
	if run.Counts.Queued != "1" {
		t.Fatal("bound active job was not queued")
	}
	processQueued(t, s)
	view, err := s.GetAccountObservation(ctx, token, a.ID)
	if err != nil || view.Snapshot == nil || view.Snapshot.SourceMode != "active" || view.Snapshot.Simulation == nil ||
		view.Snapshot.Simulation.Epoch != simulationTestEpoch || *view.Snapshot.Windows[0].UsedPercent != "0" ||
		*view.Snapshot.Windows[0].DurationSeconds != "10800" || view.LatestAttempt.Reason != "SIMULATED_READ_ONLY_SNAPSHOT" {
		t.Fatalf("active simulation facts/provenance lost: %+v %v", view, err)
	}
	status, err := s.GetMonitoringStatus(ctx, token)
	if err != nil || !status.ActiveAvailable || status.ActiveReason != "SIMULATED_PROTOCOL_ONLY" {
		t.Fatal("simulation mislabeled as native capability", err)
	}
	if queueAccount(t, s, token, c, a.ID).Counts.Skipped != "1" || f.active.Load() != 1 {
		t.Fatal("simulation bypassed cooldown")
	}
	replacement, err := New(pool, s.auth, bytes.Repeat([]byte{7}, 32), true)
	if err != nil {
		t.Fatal(err)
	}
	retained, err := replacement.GetAccountObservation(ctx, token, a.ID)
	if err != nil || retained.Snapshot.Simulation == nil || !retained.Snapshot.ReceivedAt.Equal(view.Snapshot.ReceivedAt) {
		t.Fatal("replacement erased simulation provenance")
	}
	native, err := replacement.GetMonitoringStatus(ctx, token)
	if err != nil || native.ActiveAvailable || native.ActiveReason != "ACTIVE_QUERY_SIDE_EFFECT" {
		t.Fatal("replacement inferred an unconfigured simulation gate")
	}
	advanceCollectionFixture(t, pool)
	f.mode.Store(1)
	queueAccount(t, s, token, c, a.ID)
	processQueued(t, s)
	failed, err := s.GetAccountObservation(ctx, token, a.ID)
	if err != nil || failed.LatestAttempt.Reason != "SIMULATION_PROTOCOL_UNVERIFIED" || !failed.Snapshot.ReceivedAt.Equal(view.Snapshot.ReceivedAt) {
		t.Fatal("wrong epoch replaced or refreshed immutable facts")
	}
	if f.active.Load() != 2 || f.normal.Load() != 1 {
		t.Fatal("read retry or passive/native quota fallback occurred")
	}
}

func TestSimulationUsesTheSamePlannerAndRejectsChangedNativeIdentity(t *testing.T) {
	s, pool, token := setup(t)
	ctx := context.Background()
	server, f := simulationServer(t)
	c := save(t, s, token, server.URL)
	a := adoptManaged(t, s, token, c, 1, "Simulation")
	bindSimulation(t, s, c)
	policy, _ := s.GetMonitoringConfig(ctx, token)
	policy.Enabled = true
	if _, err := s.UpdateMonitoringConfig(ctx, token, policy); err != nil {
		t.Fatal(err)
	}
	status, err := s.GetMonitoringStatus(ctx, token)
	view, readErr := s.GetAccountObservation(ctx, token, a.ID)
	if err != nil || readErr != nil || status.NextScheduledAt == nil || view.NextScheduledAt == nil {
		t.Fatal("planner excluded a bound simulation")
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if err := s.scheduleCollections(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if _, err := s.processCollection(ctx, conn); err != nil {
		t.Fatal(err)
	}
	view, err = s.GetAccountObservation(ctx, token, a.ID)
	if err != nil || view.Snapshot == nil || view.LatestAttempt.State != "succeeded" {
		t.Fatal("scheduled simulation did not execute")
	}
	advanceCollectionFixture(t, pool)
	f.mode.Store(2)
	queueAccount(t, s, token, c, a.ID)
	processQueued(t, s)
	changed, err := s.GetAccountObservation(ctx, token, a.ID)
	if err != nil || changed.LatestAttempt.State != "conflict" || changed.Freshness != "identity_unverified" ||
		!changed.Snapshot.ReceivedAt.Equal(view.Snapshot.ReceivedAt) {
		t.Fatal("changed identity silently rebound prior simulation facts")
	}
}

func TestSimulationActiveReadCannotPersistAfterObservedActorExpiry(t *testing.T) {
	s, pool, token := setup(t)
	ctx := context.Background()
	server, f := simulationServer(t)
	c := save(t, s, token, server.URL)
	a := adoptManaged(t, s, token, c, 1, "Simulation")
	bindSimulation(t, s, c)
	queueAccount(t, s, token, c, a.ID)
	var expires time.Time
	if err := pool.QueryRow(ctx, "UPDATE auth_sessions SET idle_expires_at=clock_timestamp()+interval '800 milliseconds' RETURNING idle_expires_at").Scan(&expires); err != nil {
		t.Fatal(err)
	}
	f.block.Store(true)
	defer func() {
		select {
		case <-f.release:
		default:
			close(f.release)
		}
	}()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	done := make(chan error, 1)
	go func() { _, err := s.processCollection(ctx, conn); done <- err }()
	select {
	case <-f.arrived:
	case <-time.After(2 * time.Second):
		t.Fatal("active GET barrier not observed")
	}
	timer := time.NewTimer(time.Until(expires.Add(50 * time.Millisecond)))
	<-timer.C
	close(f.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM quota_observations").Scan(&count); err != nil || count != 0 {
		t.Fatal("expired actor persisted protected simulation facts")
	}
	var reason string
	if err := pool.QueryRow(ctx, "SELECT reason FROM collection_jobs ORDER BY id DESC LIMIT 1").Scan(&reason); err != nil || reason != "ACTOR_UNAVAILABLE" {
		t.Fatal("expired operation result not retained safely")
	}
}
