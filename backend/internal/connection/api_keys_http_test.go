package connection

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"sub-pool/backend/internal/auth"
)

func machineCall(h http.Handler, path, body, secret string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+secret)
	r.RemoteAddr = "127.0.0.1:5000"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func machineMember(t *testing.T, f *walletFixture, name string, grants auth.Grants) (auth.ManagedUser, string) {
	t.Helper()
	ctx := context.Background()
	user, err := f.s.auth.CreateUser(ctx, f.token, auth.UserCreate{Username: name, Password: "MachineReaderPassword123", Enabled: true, Grants: grants})
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := f.s.auth.Login(ctx, name, "MachineReaderPassword123", netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	return user, token
}

func TestMachineQueriesReuseSQLScopeAndIndependentPermissions(t *testing.T) {
	f := newWalletFixture(t, true)
	ctx := context.Background()
	a := adoptManaged(t, f.s, f.token, f.c, 1, "Visible account")
	b := adoptManaged(t, f.s, f.token, f.c, 2, "Hidden account")
	q := createParticipant(t, f.s, f.token, f.c, 22, "Hidden participant")
	pool := savePool(t, f.s, f.token, PoolUpdate{PoolInput: poolInput(f.c, []ManagedAccount{a, b}, []Participant{f.p, q}, []string{"60", "40"})})
	user, member := machineMember(t, f, "machine.reader", auth.Grants{Features: []string{"accounts", "pools", "balances"}, AccountIDs: []string{a.ID}, ParticipantIDs: []string{f.p.ID}})
	key, err := f.s.auth.IssueAPIKey(ctx, member, "reports", "read")
	if err != nil {
		t.Fatal(err)
	}
	boundary, err := auth.NewHandler(f.s.auth, "http://127.0.0.1:3000", true)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/api/v1/accounts/", &AccountsHandler{Service: f.s, Boundary: boundary})
	mux.Handle("/api/v1/pools/", &PoolsHandler{Service: f.s, Boundary: boundary})
	mux.Handle("/api/v1/participants/", &ParticipantsHandler{Service: f.s, Boundary: boundary})
	mux.Handle("/api/v1/wallets/", &WalletsHandler{Service: f.s, Boundary: boundary})
	before := f.calls.Load()
	for _, c := range []struct {
		path, body string
		status     int
	}{
		{"/api/v1/accounts/list", `{"after":"","search":"","limit":100}`, 200},
		{"/api/v1/accounts/get", `{"id":"` + b.ID + `"}`, 404},
		{"/api/v1/participants/list", `{"after":"","search":"","limit":100}`, 200},
		{"/api/v1/participants/get", `{"id":"` + q.ID + `"}`, 404},
		{"/api/v1/pools/get", `{"id":"` + pool.PoolID + `"}`, 200},
		{"/api/v1/wallets/list", `{"after":"","limit":100}`, 200},
		{"/api/v1/accounts/refresh", `{}`, 403},
	} {
		w := machineCall(mux, c.path, c.body, key.Secret)
		if w.Code != c.status {
			t.Fatal("machine route status", c.path, w.Code, c.status)
		}
		if strings.Contains(w.Body.String(), b.ID) || strings.Contains(w.Body.String(), q.ID) || strings.Contains(w.Body.String(), "Hidden account") || strings.Contains(w.Body.String(), "Hidden participant") {
			t.Fatal("hidden scope leaked", c.path)
		}
		if c.path == "/api/v1/pools/get" {
			var out PoolContract
			if json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out.Accounts) != 1 || len(out.Participants) != 1 || out.Accounts[0].ID != a.ID || out.Participants[0].ID != f.p.ID {
				t.Fatal("pool SQL was not scoped before projection")
			}
		}
	}
	update := auth.UserUpdate{ID: user.ID, Revision: user.Revision, Username: user.Username, Enabled: true, Grants: user.Grants}
	update.AccountIDs = []string{}
	user, err = f.s.auth.UpdateUser(ctx, f.token, update)
	if err != nil {
		t.Fatal(err)
	}
	if w := machineCall(mux, "/api/v1/accounts/get", `{"id":"`+a.ID+`"}`, key.Secret); w.Code != 404 {
		t.Fatal("key retained account scope")
	}
	if w := machineCall(mux, "/api/v1/pools/get", `{"id":"`+pool.PoolID+`"}`, key.Secret); w.Code != 404 {
		t.Fatal("key retained dual pool scope")
	}
	if w := machineCall(mux, "/api/v1/wallets/get", `{"participant_id":"`+f.p.ID+`"}`, key.Secret); w.Code != 200 {
		t.Fatal("account contraction removed independent participant read")
	}
	update = auth.UserUpdate{ID: user.ID, Revision: user.Revision, Username: user.Username, Enabled: true, Grants: user.Grants}
	update.ParticipantIDs = []string{}
	user, err = f.s.auth.UpdateUser(ctx, f.token, update)
	if err != nil {
		t.Fatal(err)
	}
	if w := machineCall(mux, "/api/v1/wallets/get", `{"participant_id":"`+f.p.ID+`"}`, key.Secret); w.Code != 404 {
		t.Fatal("key retained participant scope")
	}
	update = auth.UserUpdate{ID: user.ID, Revision: user.Revision, Username: user.Username, Enabled: true, Grants: auth.Grants{Features: []string{}, AccountIDs: []string{}, ParticipantIDs: []string{}}}
	if _, err = f.s.auth.UpdateUser(ctx, f.token, update); err != nil {
		t.Fatal(err)
	}
	if w := machineCall(mux, "/api/v1/wallets/list", `{"after":"","limit":100}`, key.Secret); w.Code != 403 {
		t.Fatal("key retained page permission")
	}
	if f.calls.Load() != before {
		t.Fatal("retained machine queries contacted native source")
	}
}

func TestManagementKeyKeepsApplicationRejectedAndCorrelatesSafeActor(t *testing.T) {
	f := newWalletFixture(t, true)
	ctx := context.Background()
	read, err := f.s.auth.IssueAPIKey(ctx, f.token, "admin report", "read")
	if err != nil {
		t.Fatal(err)
	}
	management, err := f.s.auth.IssueAPIKey(ctx, f.token, "application control", "management")
	if err != nil {
		t.Fatal(err)
	}
	_, member := machineMember(t, f, "application.reader", auth.Grants{Features: []string{"balances"}, AccountIDs: []string{}, ParticipantIDs: []string{f.p.ID}})
	ordinary, err := f.s.auth.IssueAPIKey(ctx, member, "personal report", "read")
	if err != nil {
		t.Fatal(err)
	}
	boundary, err := auth.NewHandler(f.s.auth, "http://127.0.0.1:3000", true)
	if err != nil {
		t.Fatal(err)
	}
	h := &WalletApplicationsHandler{Service: f.s, Boundary: boundary}
	in := applicationInput(f, 1)
	body, _ := json.Marshal(in)
	before := f.calls.Load()
	for _, secret := range []string{read.Secret, ordinary.Secret} {
		if w := machineCall(h, "/api/v1/wallet-applications/request", string(body), secret); w.Code != 403 {
			t.Fatal("read key reached management business request", w.Code)
		}
	}
	w := machineCall(h, "/api/v1/wallet-applications/request", string(body), management.Secret)
	var first WalletApplication
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &first) != nil || first.Target != nil || first.State != "rejected" || first.DispatchState != "not_dispatched" || first.Details == nil || first.Details.ActorAPIKeyID == nil || *first.Details.ActorAPIKeyID != management.Key.ID {
		t.Fatal("machine application claimed dispatch or lost safe actor", w.Code)
	}
	duplicate := machineCall(h, "/api/v1/wallet-applications/request", string(body), management.Secret)
	var repeated WalletApplication
	if duplicate.Code != 200 || json.Unmarshal(duplicate.Body.Bytes(), &repeated) != nil || repeated.ID != first.ID {
		t.Fatal("machine request replay changed immutable receipt")
	}
	var count int
	if err = f.db.QueryRow(ctx, "SELECT count(*) FROM management_events WHERE operation_id=$1", "wallet-application:"+first.ID).Scan(&count); err != nil || count != 2 {
		t.Fatal("application audit correlation missing", err)
	}
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM management_events WHERE operation_id=$1 AND action='api_key.wallet.application' AND target_id=$2 AND outcome='denied'`, "wallet-application:"+first.ID, management.Key.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("safe API actor audit missing", err)
	}
	summary := machineCall(h, "/api/v1/wallet-applications/get", `{"id":"`+first.ID+`"}`, ordinary.Secret)
	var projected WalletApplication
	if summary.Code != 200 || json.Unmarshal(summary.Body.Bytes(), &projected) != nil || projected.Details != nil || strings.Contains(summary.Body.String(), management.Key.ID) || strings.Contains(summary.Body.String(), "actor_api_key_id") {
		t.Fatal("ordinary summary leaked API actor")
	}
	if strings.Contains(w.Body.String(), management.Secret) || strings.Contains(w.Body.String(), read.Secret) || f.calls.Load() != before {
		t.Fatal("application exposed secret or contacted native source")
	}
	exec(t, f.db, `CREATE FUNCTION reject_machine_application_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='api_key.wallet.application' THEN RAISE EXCEPTION 'forced machine audit failure'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_machine_application_audit BEFORE INSERT ON management_events FOR EACH ROW EXECUTE FUNCTION reject_machine_application_audit()`)
	in = applicationInput(f, 2)
	body, _ = json.Marshal(in)
	if failed := machineCall(h, "/api/v1/wallet-applications/request", string(body), management.Secret); failed.Code != 500 {
		t.Fatal("machine audit failure acknowledged receipt")
	}
	if err = f.db.QueryRow(ctx, "SELECT count(*) FROM wallet_application_requests").Scan(&count); err != nil || count != 1 {
		t.Fatal("failed companion audit left partial receipt", err)
	}
	if f.calls.Load() != before {
		t.Fatal("machine rejection/audit failure contacted native source")
	}
}
