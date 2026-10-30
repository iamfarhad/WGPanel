package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"wgpanel-api/internal/authcrypto"
	"wgpanel-api/internal/events"
	"wgpanel-api/internal/redisclient"
	"wgpanel-api/internal/store"
)

func opsPeerNodes(t *testing.T, e *evEnv, accountID string) map[string]bool {
	t.Helper()
	peers, err := e.st.ListAccountPeersWithNode(e.ctx, accountID, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, p := range peers {
		out[p.NodeID] = true
	}
	return out
}

func TestMaintenanceExcludesSteeringAndPlacement(t *testing.T) {
	e := newEvEnv(t)
	group := "ops-" + e.sfx
	mk := func(name, subnet string) store.Node {
		pub := "pk-" + name + e.sfx
		n, err := e.st.CreateNode(e.ctx, name+e.sfx, group, "eu", name+".test:51820", subnet, 500, &pub)
		if err != nil {
			t.Fatal(err)
		}
		e.exec(`UPDATE nodes SET status = 'online' WHERE id = $1`, n.ID)
		t.Cleanup(func() { e.exec(`UPDATE nodes SET status = 'pending', maintenance_since = NULL WHERE id = $1`, n.ID) })
		return n
	}
	a, b := mk("opsa", "10.91.0.0/16"), mk("opsb", "10.92.0.0/16")
	newAcct := func(label, nodeID string) (store.Account, error) {
		return e.st.CreateAccount(e.ctx, store.CreateAccountParams{
			Label: label + e.sfx, NodeIDOrAuto: nodeID, PublicKey: label + "pk" + e.sfx, PrivateKeyEncrypted: "x",
			SubscriptionToken: label + "tok" + e.sfx, AllowedNodeGroups: []string{group},
		})
	}
	before, err := newAcct("before", "auto")
	if err != nil {
		t.Fatal(err)
	}
	if p := opsPeerNodes(t, e, before.ID); !p[a.ID] || !p[b.ID] {
		t.Fatalf("auto placement should cover both nodes: %v", p)
	}

	// RBAC: support cannot toggle, operator can.
	support, _ := e.st.CreateAdmin(e.ctx, "opssup"+e.sfx, "h", "support")
	supTok, _ := authcrypto.IssueAccessToken(gateTestSecret, support.ID, support.Username, "support")
	if code, _ := e.call(supTok, "PUT", "/api/v1/nodes/"+b.ID+"/maintenance", `{"enabled":true}`); code != 403 {
		t.Fatalf("support toggle = %d", code)
	}
	if code, _ := e.call(e.superT, "PUT", "/api/v1/nodes/"+b.ID+"/maintenance", `{}`); code != 400 {
		t.Fatalf("missing enabled = %d", code)
	}

	drain(e.s.Events)
	code, out := e.call(e.superT, "PUT", "/api/v1/nodes/"+b.ID+"/maintenance", `{"enabled":true}`)
	if code != 200 || out["maintenance"] != true || out["maintenance_since"] == nil {
		t.Fatalf("enable: %d %v", code, out)
	}
	var started int
	for _, ev := range drain(e.s.Events) {
		if ev.Type == events.NodeMaintStarted && ev.Data["node_id"] == b.ID {
			started++
		}
	}
	if started != 1 {
		t.Fatalf("expected one node.maintenance_started, got %d", started)
	}
	// Repeating the same state emits nothing more.
	e.call(e.superT, "PUT", "/api/v1/nodes/"+b.ID+"/maintenance", `{"enabled":true}`)
	for _, ev := range drain(e.s.Events) {
		if ev.Type == events.NodeMaintStarted {
			t.Fatal("duplicate maintenance_started")
		}
	}
	_, list := e.call(e.superT, "GET", "/api/v1/nodes", "")
	for _, n := range list["nodes"].([]any) {
		m := n.(map[string]any)
		if m["id"] == b.ID && m["maintenance"] != true {
			t.Fatalf("list must show maintenance: %v", m)
		}
		if m["id"] == a.ID && m["maintenance"] == true {
			t.Fatal("node A must not be in maintenance")
		}
	}

	// Steering skips B although the account still has a peer there (existing peers stay).
	cands, err := e.st.SteerCandidatesForAccount(e.ctx, before.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].NodeID != a.ID {
		t.Fatalf("steering candidates = %+v, want only A", cands)
	}
	if p := opsPeerNodes(t, e, before.ID); !p[b.ID] {
		t.Fatal("the existing peer on the maintenance node must remain")
	}

	// New placement: auto skips B, pinning B is refused.
	during, err := newAcct("during", "auto")
	if err != nil {
		t.Fatal(err)
	}
	if p := opsPeerNodes(t, e, during.ID); !p[a.ID] || p[b.ID] {
		t.Fatalf("auto placement during maintenance: %v", p)
	}
	if _, err := newAcct("pinned", b.ID); err != store.ErrNodeInMaintenance {
		t.Fatalf("pinning a maintenance node: %v", err)
	}

	// Node-down alerts are suppressed in maintenance, and a clear is not a "recovery".
	e.exec(`UPDATE nodes SET status = 'offline' WHERE id = $1`, b.ID)
	drain(e.s.Events)
	if err := e.s.runThresholdSweep(e.ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, ev := range drain(e.s.Events) {
		if ev.Data["node_id"] == b.ID {
			t.Fatalf("no node alert expected during maintenance, got %s", ev.Type)
		}
	}

	// Ending maintenance: event, audit, and the missing peer is backfilled.
	e.exec(`UPDATE nodes SET status = 'online' WHERE id = $1`, b.ID)
	code, out = e.call(e.superT, "PUT", "/api/v1/nodes/"+b.ID+"/maintenance", `{"enabled":false}`)
	if code != 200 || out["maintenance"] != false {
		t.Fatalf("disable: %d %v", code, out)
	}
	var ended int
	for _, ev := range drain(e.s.Events) {
		if ev.Type == events.NodeMaintEnded && ev.Data["node_id"] == b.ID {
			ended++
		}
	}
	if ended != 1 {
		t.Fatalf("expected one node.maintenance_ended, got %d", ended)
	}
	if p := opsPeerNodes(t, e, during.ID); !p[b.ID] {
		t.Fatalf("backfill after maintenance should add the missed peer: %v", p)
	}
	entries, _ := e.st.ListAuditLog(e.ctx, 100)
	var sawStart, sawEnd bool
	for _, en := range entries {
		if en.Target != nil && *en.Target == b.Name {
			sawStart = sawStart || en.Action == "node.maintenance_started"
			sawEnd = sawEnd || en.Action == "node.maintenance_ended"
		}
	}
	if !sawStart || !sawEnd {
		t.Fatalf("audit entries missing: start=%v end=%v", sawStart, sawEnd)
	}
}

// ---- refresh rotation -----------------------------------------------------------

func newRotationServer(t *testing.T) (*Server, http.Handler, store.Admin) {
	dsn, redisURL := os.Getenv("WGPANEL_TEST_POSTGRES_DSN"), os.Getenv("WGPANEL_TEST_REDIS_URL")
	if dsn == "" || redisURL == "" {
		t.Skip("set WGPANEL_TEST_POSTGRES_DSN and WGPANEL_TEST_REDIS_URL")
	}
	ctx := context.Background()
	st, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	rdb, err := redisclient.Open(ctx, redisURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rdb.Close() })
	s := &Server{Store: st, Redis: rdb, JWTSecret: gateTestSecret, AccountKeyEncryptionKey: itHexKey,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Events: events.NewBus(64, nil)}
	admin, err := st.CreateAdmin(ctx, "rot"+strconv.FormatInt(time.Now().UnixNano(), 10), "h", "operator")
	if err != nil {
		t.Fatal(err)
	}
	return s, s.Routes(), admin
}

func refreshCall(h http.Handler, token string) (int, map[string]any) {
	req := httptest.NewRequest("POST", "/api/v1/auth/refresh", bytes.NewBufferString(`{"refresh_token":"`+token+`"}`))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	var out map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	return rr.Code, out
}

func TestRefreshTokenRotationAndReuseDetection(t *testing.T) {
	s, h, admin := newRotationServer(t)
	ctx := context.Background()
	t1, err := authcrypto.IssueRefreshToken(ctx, s.Redis, admin.ID)
	if err != nil {
		t.Fatal(err)
	}

	code, out := refreshCall(h, t1)
	t2, _ := out["refresh_token"].(string)
	if code != 200 || t2 == "" || t2 == t1 || out["access_token"] == nil {
		t.Fatalf("first refresh: %d %v", code, out)
	}
	code, out = refreshCall(h, t2)
	t3, _ := out["refresh_token"].(string)
	if code != 200 || t3 == "" || t3 == t2 {
		t.Fatalf("second refresh: %d %v", code, out)
	}

	// Age t1's "used" record past the grace window, as if the replay came much later.
	key := "rt_used:" + t1
	v, err := s.Redis.Get(ctx, key).Result()
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(v, "|")
	parts[3] = strconv.FormatInt(time.Now().Add(-time.Minute).Unix(), 10)
	s.Redis.Set(ctx, key, strings.Join(parts, "|"), time.Hour)

	drain(s.Events)
	if code, _ = refreshCall(h, t1); code != 401 {
		t.Fatalf("reuse must be rejected, got %d", code)
	}
	// The whole family is dead, including the newest token.
	if code, _ = refreshCall(h, t3); code != 401 {
		t.Fatalf("family must be revoked, newest token got %d", code)
	}
	var sawEvent bool
	for _, ev := range drain(s.Events) {
		if ev.Type == events.RefreshReuse && ev.Data["admin"] == admin.Username {
			sawEvent = true
		}
	}
	if !sawEvent {
		t.Fatal("expected a security.refresh_reuse event")
	}
	entries, _ := s.Store.ListAuditLog(ctx, 50)
	var audited bool
	for _, en := range entries {
		audited = audited || (en.Action == "admin.refresh_token_reuse" && en.Actor == admin.Username)
	}
	if !audited {
		t.Fatal("expected an audit entry for the reuse")
	}

	// A different login of the same admin is a different family and is unaffected.
	other, _ := authcrypto.IssueRefreshToken(ctx, s.Redis, admin.ID)
	if code, _ = refreshCall(h, other); code != 200 {
		t.Fatalf("other family got %d", code)
	}
}

func TestRefreshConcurrentTabsDoNotLogOut(t *testing.T) {
	s, h, admin := newRotationServer(t)
	ctx := context.Background()
	t1, _ := authcrypto.IssueRefreshToken(ctx, s.Redis, admin.ID)

	var wg sync.WaitGroup
	var mu sync.Mutex
	tokens := map[string]int{}
	codes := map[int]int{}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, out := refreshCall(h, t1)
			mu.Lock()
			codes[code]++
			if tok, _ := out["refresh_token"].(string); tok != "" {
				tokens[tok]++
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	if codes[200] != 8 {
		t.Fatalf("every concurrent refresh within the grace window must succeed, got %v", codes)
	}
	if len(tokens) != 1 {
		t.Fatalf("duplicates must share the same successor, got %d distinct tokens", len(tokens))
	}
	var shared string
	for k := range tokens {
		shared = k
	}
	if code, _ := refreshCall(h, shared); code != 200 {
		t.Fatalf("the shared successor must work, got %d", code)
	}
	// A late duplicate of t1 (still inside grace) is handed the family's current token,
	// not a reuse verdict, even after the successor has itself been rotated.
	if code, _ := refreshCall(h, t1); code != 200 {
		t.Fatalf("duplicate after successor rotation, still in grace: %d", code)
	}
}

func TestRefreshLegacyTokenIsAdopted(t *testing.T) {
	s, h, admin := newRotationServer(t)
	ctx := context.Background()
	// A token issued before rotation existed: the value is the bare admin id.
	s.Redis.Set(ctx, "refresh_token:legacy"+admin.ID, admin.ID, time.Hour)
	code, out := refreshCall(h, "legacy"+admin.ID)
	if code != 200 || out["refresh_token"] == nil {
		t.Fatalf("legacy refresh: %d %v", code, out)
	}
	if code, _ := refreshCall(h, out["refresh_token"].(string)); code != 200 {
		t.Fatalf("rotated legacy successor: %d", code)
	}
}

// ---- SSH provisioning API -------------------------------------------------------

func TestSSHProvisioningAPI(t *testing.T) {
	e := newEvEnv(t)
	e.s.BootPanelDomain, e.s.NodeAgentPort = "panel.example.test", "48443"
	opr, _ := e.st.CreateAdmin(e.ctx, "opsopr"+e.sfx, "h", "operator")
	oprTok, _ := authcrypto.IssueAccessToken(gateTestSecret, opr.ID, opr.Username, "operator")
	base := "/api/v1/node-provisioning/ssh"

	for _, p := range []string{"/probe", "/jobs"} {
		if code, _ := e.call(oprTok, "POST", base+p, `{}`); code != 403 {
			t.Fatalf("operator %s = %d, want 403 (super_admin only)", p, code)
		}
	}
	if code, _ := e.call(e.superT, "POST", base+"/probe", `{"host":"bad host","username":"root"}`); code != 400 {
		t.Fatalf("bad host = %d", code)
	}
	// No confirmed fingerprint -> refused before anything is created.
	body := `{"host":"127.0.0.1","port":1,"username":"root","password":"s3cretpw","node":{"name":"ssh-` + e.sfx + `","public_endpoint":"1.2.3.4:51820","wg_subnet":"10.93.0.0/16"}}`
	if code, _ := e.call(e.superT, "POST", base+"/jobs", body); code != 400 {
		t.Fatalf("missing fingerprint = %d", code)
	}

	body = strings.Replace(body, `"password"`, `"fingerprint":"SHA256:abc","password"`, 1)
	code, out := e.call(e.superT, "POST", base+"/jobs", body)
	if code != 202 || out["job_id"] == nil || out["node_id"] == nil {
		t.Fatalf("start job: %d %v", code, out)
	}
	jobID := out["job_id"].(string)
	var final map[string]any
	for i := 0; i < 100; i++ {
		_, final = e.call(e.superT, "GET", base+"/jobs/"+jobID, "")
		if final["status"] != "running" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if final["status"] != "failed" || final["error"].(map[string]any)["code"] != "connect_failed" {
		t.Fatalf("job against a closed port: %v", final)
	}
	// Persisted record exists and carries no credentials.
	j, err := e.st.GetSSHJob(e.ctx, jobID)
	if err != nil || j.Status != "failed" {
		t.Fatalf("persisted job: %+v %v", j, err)
	}
	if strings.Contains(j.Log+j.ErrorMessage, "s3cretpw") {
		t.Fatal("password leaked into the stored job")
	}
	// A node and join token were created for the attempt, and a retry can reuse the node.
	retry := `{"host":"127.0.0.1","port":1,"username":"root","password":"s3cretpw","fingerprint":"SHA256:abc","node_id":"` + out["node_id"].(string) + `"}`
	if code, _ = e.call(e.superT, "POST", base+"/jobs", retry); code != 202 {
		t.Fatalf("retry with node_id: %d", code)
	}
	if code, _ = e.call(e.superT, "POST", base+"/jobs/"+jobID+"/cancel", ""); code != 404 && code != 202 {
		t.Fatalf("cancel finished job: %d", code)
	}
}
