package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"wgpanel-api/internal/authcrypto"
	"wgpanel-api/internal/backupcrypto"
	"wgpanel-api/internal/events"
	"wgpanel-api/internal/store"
	"wgpanel-api/internal/webhook"
)

type evEnv struct {
	t      *testing.T
	ctx    context.Context
	st     *store.Store
	s      *Server
	h      http.Handler
	sfx    string
	dsn    string
	superT string
}

func newEvEnv(t *testing.T) *evEnv {
	dsn := os.Getenv("WGPANEL_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set WGPANEL_TEST_POSTGRES_DSN to run integration tests")
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
	s := &Server{
		Store: st, JWTSecret: gateTestSecret, APIHMACMasterKey: itHexKey, AccountKeyEncryptionKey: itHexKey,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Events: events.NewBus(256, nil),
	}
	e := &evEnv{t: t, ctx: ctx, st: st, s: s, h: s.Routes(), sfx: strconv.FormatInt(time.Now().UnixNano(), 10), dsn: dsn}
	admin, err := st.CreateAdmin(ctx, "evsuper"+e.sfx, "h", "super_admin")
	if err != nil {
		t.Fatal(err)
	}
	e.superT, _ = authcrypto.IssueAccessToken(gateTestSecret, admin.ID, admin.Username, "super_admin")
	return e
}

func (e *evEnv) call(tok, method, path, body string) (int, map[string]any) {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	e.h.ServeHTTP(rr, req)
	var out map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	return rr.Code, out
}

func (e *evEnv) exec(sql string, args ...any) {
	e.t.Helper()
	c, err := pgx.Connect(e.ctx, e.dsn)
	if err != nil {
		e.t.Fatal(err)
	}
	defer c.Close(e.ctx)
	if _, err := c.Exec(e.ctx, sql, args...); err != nil {
		e.t.Fatal(err)
	}
}

func (e *evEnv) reseller(name string) (store.Reseller, string) {
	r, err := e.st.CreateReseller(e.ctx, store.NewResellerParams{Username: name + e.sfx, PasswordHash: "h"})
	if err != nil {
		e.t.Fatal(err)
	}
	tok, _ := authcrypto.IssueAccessToken(gateTestSecret, r.AdminID, r.Username, "reseller")
	return r, tok
}

func items(out map[string]any) []any { v, _ := out["items"].([]any); return v }

// ---- notifications + reseller isolation ------------------------------------------

func TestNotificationCenterScopingAndReadState(t *testing.T) {
	e := newEvEnv(t)
	resA, tokA := e.reseller("nra")
	_, tokB := e.reseller("nrb")
	nsA := store.ResellerNamespace(resA.AdminID)

	e.s.dispatchEvent(e.ctx, events.Event{ID: "e1", Type: events.AccountExpired, Namespace: nsA, CreatedAt: time.Now(), Data: map[string]any{"label": "alice-" + e.sfx}})
	e.s.dispatchEvent(e.ctx, events.Event{ID: "e2", Type: events.NodeDown, Namespace: "", CreatedAt: time.Now(), Data: map[string]any{"node_name": "n-" + e.sfx}})
	e.s.dispatchEvent(e.ctx, events.Event{ID: "e3", Type: events.AccountCreated, Namespace: nsA, CreatedAt: time.Now()}) // not in-panel

	code, out := e.call(tokA, "GET", "/api/v1/notifications", "")
	if code != 200 || len(items(out)) != 1 || out["unread_count"].(float64) != 1 {
		t.Fatalf("reseller A sees %d (%v)", len(items(out)), out)
	}
	if items(out)[0].(map[string]any)["event"] != events.AccountExpired {
		t.Fatalf("wrong event: %v", items(out)[0])
	}
	if _, out = e.call(tokB, "GET", "/api/v1/notifications", ""); len(items(out)) != 0 {
		t.Fatalf("reseller B must see nothing, got %v", out)
	}
	_, out = e.call(e.superT, "GET", "/api/v1/notifications?limit=100", "")
	var sawNode, sawExpired bool
	for _, it := range items(out) {
		m := it.(map[string]any)
		sawNode = sawNode || strings.Contains(m["body"].(string), "n-"+e.sfx)
		sawExpired = sawExpired || strings.Contains(m["body"].(string), "alice-"+e.sfx)
	}
	if !sawNode || !sawExpired {
		t.Fatalf("staff should see global + reseller events: node=%v expired=%v", sawNode, sawExpired)
	}

	// B cannot mark A's notification read (404), A can; unread drops to 0.
	id := strconv.Itoa(int(items(func() map[string]any { _, o := e.call(tokA, "GET", "/api/v1/notifications", ""); return o }())[0].(map[string]any)["id"].(float64)))
	if code, _ = e.call(tokB, "POST", "/api/v1/notifications/"+id+"/read", ""); code != 404 {
		t.Fatalf("foreign mark-read = %d", code)
	}
	if code, _ = e.call(tokA, "POST", "/api/v1/notifications/"+id+"/read", ""); code != 204 {
		t.Fatalf("mark-read = %d", code)
	}
	if _, out = e.call(tokA, "GET", "/api/v1/notifications", ""); out["unread_count"].(float64) != 0 {
		t.Fatalf("unread after read: %v", out["unread_count"])
	}
	// read-all for staff clears everything visible.
	if code, _ = e.call(e.superT, "POST", "/api/v1/notifications/read-all", ""); code != 204 {
		t.Fatalf("read-all = %d", code)
	}
	if _, out = e.call(e.superT, "GET", "/api/v1/notifications", ""); out["unread_count"].(float64) != 0 {
		t.Fatalf("unread after read-all: %v", out["unread_count"])
	}
	// Channels are super_admin only; reseller is denied by the allowlist.
	if code, _ = e.call(tokA, "GET", "/api/v1/notification-channels", ""); code != 403 {
		t.Fatalf("reseller channels = %d", code)
	}
}

// ---- edge-triggered dedupe --------------------------------------------------------

func TestEventTransitionDedupe(t *testing.T) {
	e := newEvEnv(t)
	key := "test:" + e.sfx
	w := 200 * time.Millisecond
	rose, fell, err := e.st.EventTransition(e.ctx, key, true, w)
	if err != nil || !rose || fell {
		t.Fatalf("first crossing: %v %v %v", rose, fell, err)
	}
	if rose, _, _ = e.st.EventTransition(e.ctx, key, true, w); rose {
		t.Fatal("a standing condition must not re-fire")
	}
	if _, fell, _ = e.st.EventTransition(e.ctx, key, false, w); !fell {
		t.Fatal("recovery edge missing")
	}
	// Re-crossing inside the suppression window is swallowed ...
	if rose, _, _ = e.st.EventTransition(e.ctx, key, true, time.Hour); rose {
		t.Fatal("flap inside the window must be suppressed")
	}
	e.st.EventTransition(e.ctx, key, false, w) //nolint:errcheck
	time.Sleep(250 * time.Millisecond)
	// ... and fires again once the window has passed.
	if rose, _, _ = e.st.EventTransition(e.ctx, key, true, w); !rose {
		t.Fatal("crossing after the window must fire")
	}
}

// ---- threshold sweep --------------------------------------------------------------

func drain(b *events.Bus) []events.Event {
	var out []events.Event
	for {
		select {
		case ev := <-b.C():
			out = append(out, ev)
		default:
			return out
		}
	}
}

func TestThresholdSweepQuotaExpiryAndNodes(t *testing.T) {
	e := newEvEnv(t)
	pub := "tk-" + e.sfx
	node, err := e.st.CreateNode(e.ctx, "tsw-"+e.sfx, "g-"+e.sfx, "eu", "t.test:51820", "10.78.0.0/16", 50, &pub)
	if err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE nodes SET status = 'online' WHERE id = $1`, node.ID)
	t.Cleanup(func() { e.exec(`UPDATE nodes SET status = 'pending' WHERE id = $1`, node.ID) })

	quota := int64(1000)
	soon := time.Now().Add(48 * time.Hour)
	swRes, _ := e.reseller("sw")
	ns := store.ResellerNamespace(swRes.AdminID)
	acc, err := e.st.CreateAccount(e.ctx, store.CreateAccountParams{
		Label: "sw-" + e.sfx, NodeIDOrAuto: node.ID, PublicKey: "pk-" + e.sfx, PrivateKeyEncrypted: "x",
		DataQuotaBytes: &quota, ExpiryAt: &soon, SubscriptionToken: "tok-" + e.sfx, OwnerKeyNamespace: &ns,
	})
	if err != nil {
		t.Fatal(err)
	}
	sweep := func() []events.Event {
		drain(e.s.Events)
		if err := e.s.runThresholdSweep(e.ctx, time.Now()); err != nil {
			t.Fatal(err)
		}
		var mine []events.Event
		for _, ev := range drain(e.s.Events) {
			if ev.Data["account_id"] == acc.ID || ev.Data["node_id"] == node.ID {
				mine = append(mine, ev)
			}
		}
		return mine
	}
	types := func(evs []events.Event) string {
		var ts []string
		for _, ev := range evs {
			ts = append(ts, ev.Type)
		}
		sort.Strings(ts)
		return strings.Join(ts, ",")
	}

	// Under 80%: only the expiring-soon crossing fires, under the owner's namespace.
	evs := sweep()
	if types(evs) != events.AccountExpiringSoon || evs[0].Namespace != ns {
		t.Fatalf("first sweep: %s ns=%q", types(evs), evs[0].Namespace)
	}
	if evs = sweep(); len(evs) != 0 {
		t.Fatalf("second sweep must be silent (once per crossing), got %s", types(evs))
	}
	e.exec(`UPDATE accounts SET data_used_bytes = 850 WHERE id = $1`, acc.ID)
	if evs = sweep(); types(evs) != events.AccountQuota80 {
		t.Fatalf("80%% crossing: %s", types(evs))
	}
	e.exec(`UPDATE accounts SET data_used_bytes = 1000 WHERE id = $1`, acc.ID)
	if evs = sweep(); types(evs) != events.AccountQuotaGone {
		t.Fatalf("exhausted crossing: %s", types(evs))
	}
	e.exec(`UPDATE accounts SET expiry_at = now() - interval '1 minute' WHERE id = $1`, acc.ID)
	if evs = sweep(); types(evs) != events.AccountExpired {
		t.Fatalf("expired crossing: %s", types(evs))
	}

	// Node health: offline fires node.down once, back online fires node.recovered.
	e.exec(`UPDATE nodes SET status = 'offline' WHERE id = $1`, node.ID)
	if evs = sweep(); types(evs) != events.NodeDown {
		t.Fatalf("node down: %s", types(evs))
	}
	if evs = sweep(); len(evs) != 0 {
		t.Fatalf("node down must not repeat: %s", types(evs))
	}
	e.exec(`UPDATE nodes SET status = 'online' WHERE id = $1`, node.ID)
	if evs = sweep(); types(evs) != events.NodeRecovered {
		t.Fatalf("node recovered: %s", types(evs))
	}
}

// ---- webhooks end to end ------------------------------------------------------------

func TestWebhookDeliveryRetrySignatureAndScoping(t *testing.T) {
	e := newEvEnv(t)
	var mu sync.Mutex
	var got []struct {
		sig, event string
		body       []byte
	}
	status := http.StatusInternalServerError
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, struct {
			sig, event string
			body       []byte
		}{r.Header.Get(webhook.SignatureHeader), r.Header.Get("X-Jirjirak-Event"), b})
		st := status
		mu.Unlock()
		w.WriteHeader(st)
		_, _ = io.WriteString(w, "receiver says hi")
	}))
	defer recv.Close()

	// SSRF guard: loopback is refused until the staff override is on.
	body := `{"url":"` + recv.URL + `/hook","events":["node.down"]}`
	if code, out := e.call(e.superT, "POST", "/api/v1/webhooks", body); code != 400 || ecode(out) != "webhook_url_blocked" {
		t.Fatalf("loopback create: %d %v", code, out)
	}
	if code, _ := e.call(e.superT, "PATCH", "/api/v1/webhook-settings", `{"allow_private_targets":true}`); code != 200 {
		t.Fatalf("override: %d", code)
	}
	t.Cleanup(func() { e.exec(`UPDATE notify_settings SET allow_private_webhook_targets = false`) })

	code, out := e.call(e.superT, "POST", "/api/v1/webhooks", body)
	if code != 201 || !strings.HasPrefix(out["secret"].(string), "whsec_") {
		t.Fatalf("create: %d %v", code, out)
	}
	hookID, secret := out["id"].(string), out["secret"].(string)
	if _, out = e.call(e.superT, "GET", "/api/v1/webhooks", ""); strings.Contains(toJSON(out), "whsec_") {
		t.Fatal("secret must never be listed again")
	}

	// Dispatch: only subscribed events are queued.
	e.s.dispatchEvent(e.ctx, events.Event{ID: "evt_x1", Type: events.AccountExpired, CreatedAt: time.Now(), Data: map[string]any{"label": "z"}})
	e.s.dispatchEvent(e.ctx, events.Event{ID: "evt_x2", Type: events.NodeDown, CreatedAt: time.Now(), Data: map[string]any{"node_name": "n1"}})
	e.s.processDueDeliveries(e.ctx)

	mu.Lock()
	if len(got) != 1 || got[0].event != events.NodeDown {
		mu.Unlock()
		t.Fatalf("expected exactly the node.down delivery, got %d", len(got))
	}
	if !webhook.Verify(secret, got[0].sig, got[0].body, time.Now(), 5*time.Minute) {
		mu.Unlock()
		t.Fatalf("signature does not verify: %s", got[0].sig)
	}
	var payload map[string]any
	_ = json.Unmarshal(got[0].body, &payload)
	mu.Unlock()
	if payload["type"] != events.NodeDown || payload["id"] != "evt_x2" {
		t.Fatalf("payload: %v", payload)
	}

	// 500 -> retrying with a ~1 minute backoff, response logged.
	_, out = e.call(e.superT, "GET", "/api/v1/webhooks/"+hookID+"/deliveries", "")
	d := items(out)[0].(map[string]any)
	if d["status"] != "retrying" || d["attempts"].(float64) != 1 || d["response_status"].(float64) != 500 || d["response_body"] != "receiver says hi" {
		t.Fatalf("delivery after 500: %v", d)
	}
	next, _ := time.Parse(time.RFC3339, d["next_attempt_at"].(string))
	if wait := time.Until(next); wait < 50*time.Second || wait > 70*time.Second {
		t.Fatalf("first backoff = %v, want ~1m", wait)
	}
	// Not due yet -> nothing is sent; once made due and the endpoint heals it succeeds.
	mu.Lock()
	n := len(got)
	status = http.StatusOK
	mu.Unlock()
	e.s.processDueDeliveries(e.ctx)
	mu.Lock()
	if len(got) != n {
		mu.Unlock()
		t.Fatal("a delivery must not be retried before its backoff elapsed")
	}
	mu.Unlock()
	e.exec(`UPDATE webhook_deliveries SET next_attempt_at = now() WHERE id = $1`, d["id"])
	e.s.processDueDeliveries(e.ctx)
	_, out = e.call(e.superT, "GET", "/api/v1/webhooks/"+hookID+"/deliveries", "")
	if d = items(out)[0].(map[string]any); d["status"] != "success" || d["attempts"].(float64) != 2 {
		t.Fatalf("delivery after heal: %v", d)
	}

	// Redeliver queues a new row with the same payload; ping answers inline.
	if code, _ = e.call(e.superT, "POST", "/api/v1/webhooks/"+hookID+"/deliveries/"+d["id"].(string)+"/redeliver", ""); code != 202 {
		t.Fatalf("redeliver = %d", code)
	}
	e.s.processDueDeliveries(e.ctx)
	if _, out = e.call(e.superT, "GET", "/api/v1/webhooks/"+hookID+"/deliveries", ""); len(items(out)) != 2 {
		t.Fatalf("expected 2 deliveries, got %d", len(items(out)))
	}
	code, out = e.call(e.superT, "POST", "/api/v1/webhooks/"+hookID+"/test", "")
	if code != 200 || out["event"] != "ping" || out["status"] != "success" {
		t.Fatalf("test ping: %d %v", code, out)
	}

	// Permanent failure after the last backoff: 6th attempt marks it failed.
	mu.Lock()
	status = http.StatusBadGateway
	mu.Unlock()
	e.s.dispatchEvent(e.ctx, events.Event{ID: "evt_x3", Type: events.NodeDown, CreatedAt: time.Now()})
	for i := 0; i < webhook.MaxAttempts; i++ {
		e.exec(`UPDATE webhook_deliveries SET next_attempt_at = now() WHERE status IN ('pending','retrying')`)
		e.s.processDueDeliveries(e.ctx)
	}
	_, out = e.call(e.superT, "GET", "/api/v1/webhooks/"+hookID+"/deliveries?limit=200", "")
	failed := 0
	for _, it := range items(out) {
		if m := it.(map[string]any); m["event_id"] == "evt_x3" && m["status"] == "failed" && m["attempts"].(float64) == float64(webhook.MaxAttempts) {
			failed++
		}
	}
	if failed != 1 {
		t.Fatalf("exhausted delivery should be failed after %d attempts: %v", webhook.MaxAttempts, out)
	}

	// Reseller scoping: each reseller manages only its own, loopback never allowed
	// even with the staff override, and events only reach the owning namespace.
	resA, tokA := e.reseller("wha")
	_, tokB := e.reseller("whb")
	if code, out = e.call(tokA, "POST", "/api/v1/webhooks", `{"url":"`+recv.URL+`/x"}`); code != 400 || ecode(out) != "webhook_url_blocked" {
		t.Fatalf("reseller loopback must stay blocked: %d %v", code, out)
	}
	if code, out = e.call(tokA, "POST", "/api/v1/webhooks", `{"url":"https://93.184.216.34/h","events":["node.down"]}`); code != 400 {
		t.Fatalf("reseller may not subscribe to node events: %d", code)
	}
	code, out = e.call(tokA, "POST", "/api/v1/webhooks", `{"url":"https://93.184.216.34/h","events":["account.expired"]}`)
	if code != 201 {
		t.Fatalf("reseller create: %d %v", code, out)
	}
	aHook := out["id"].(string)
	if code, _ = e.call(tokB, "GET", "/api/v1/webhooks/"+aHook+"/deliveries", ""); code != 404 {
		t.Fatalf("B reading A's deliveries = %d", code)
	}
	if code, _ = e.call(tokB, "DELETE", "/api/v1/webhooks/"+aHook, ""); code != 404 {
		t.Fatalf("B deleting A's webhook = %d", code)
	}
	if _, out = e.call(tokB, "GET", "/api/v1/webhooks", ""); len(items(out)) != 0 {
		t.Fatalf("B sees %d webhooks", len(items(out)))
	}
	if _, out = e.call(tokA, "GET", "/api/v1/webhooks", ""); len(items(out)) != 1 {
		t.Fatalf("A should see only its own, got %d", len(items(out)))
	}
	e.s.dispatchEvent(e.ctx, events.Event{ID: "evt_ra", Type: events.AccountExpired, Namespace: store.ResellerNamespace(resA.AdminID), CreatedAt: time.Now()})
	e.s.dispatchEvent(e.ctx, events.Event{ID: "evt_rb", Type: events.AccountExpired, Namespace: "reseller:someone-else", CreatedAt: time.Now()})
	_, out = e.call(tokA, "GET", "/api/v1/webhooks/"+aHook+"/deliveries", "")
	if len(items(out)) != 1 || items(out)[0].(map[string]any)["event_id"] != "evt_ra" {
		t.Fatalf("A's hook must only queue A's namespace events: %v", out)
	}

	// Secret rotation shows a new secret once and the old one stops verifying.
	code, out = e.call(e.superT, "POST", "/api/v1/webhooks/"+hookID+"/rotate-secret", "")
	if code != 200 || out["secret"] == secret || out["secret"] == "" {
		t.Fatalf("rotate: %d %v", code, out)
	}
	mu.Lock()
	status = http.StatusOK
	got = nil
	mu.Unlock()
	e.s.dispatchEvent(e.ctx, events.Event{ID: "evt_x4", Type: events.NodeDown, CreatedAt: time.Now()})
	e.s.processDueDeliveries(e.ctx)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || webhook.Verify(secret, got[0].sig, got[0].body, time.Now(), time.Minute) || !webhook.Verify(out["secret"].(string), got[0].sig, got[0].body, time.Now(), time.Minute) {
		t.Fatal("after rotation deliveries must be signed with the new secret only")
	}
}

// ---- notification channels ----------------------------------------------------------

func TestChannelsTelegramEmailFallbackAndBrokenBanner(t *testing.T) {
	e := newEvEnv(t)
	tgOK := true
	var tgBodies []string
	var tgMu sync.Mutex
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		tgMu.Lock()
		tgBodies = append(tgBodies, r.URL.Path+" "+string(b))
		ok := tgOK
		tgMu.Unlock()
		if !ok {
			w.WriteHeader(401)
			_, _ = io.WriteString(w, `{"ok":false,"description":"Unauthorized"}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer tg.Close()
	e.s.NotifyRT.TelegramAPIBase = tg.URL
	var mails []string
	e.s.NotifyRT.SendEmail = func(cfg emailConfig, pw, subject, body string) error {
		mails = append(mails, subject+"|"+body)
		return nil
	}

	code, out := e.call(e.superT, "POST", "/api/v1/notification-channels",
		`{"kind":"telegram","name":"ops","lang":"fa","events":["node.down","backup.failed"],"config":{"chat_ids":["-100123"]},"secret":"123:SECRETTOKEN"}`)
	if code != 201 || out["has_secret"] != true || strings.Contains(toJSON(out), "SECRETTOKEN") {
		t.Fatalf("create telegram: %d %v", code, out)
	}
	tgID := out["id"].(string)
	var enc string
	e.queryRow(`SELECT secret_encrypted FROM notification_channels WHERE id::text = $1`, []any{tgID}, &enc)
	if enc == "" || strings.Contains(enc, "SECRETTOKEN") {
		t.Fatal("token must be stored encrypted")
	}
	code, out = e.call(e.superT, "POST", "/api/v1/notification-channels",
		`{"kind":"email","name":"mail","events":["node.down"],"config":{"host":"smtp.example.com","port":587,"from":"ops@example.com","to":["a@example.com"],"tls":"starttls"},"secret":"pw"}`)
	if code != 201 {
		t.Fatalf("create email: %d %v", code, out)
	}
	if code, out = e.call(e.superT, "POST", "/api/v1/notification-channels", `{"kind":"email","config":{"host":"x","port":25,"from":"nope","to":["a@b.co"]}}`); code != 400 {
		t.Fatalf("bad email accepted: %d %v", code, out)
	}

	if code, out = e.call(e.superT, "POST", "/api/v1/notification-channels/"+tgID+"/test", ""); code != 200 {
		t.Fatalf("test send: %d %v", code, out)
	}
	tgMu.Lock()
	last := tgBodies[len(tgBodies)-1]
	tgMu.Unlock()
	if !strings.Contains(last, "/bot123:SECRETTOKEN/sendMessage") || !strings.Contains(last, "آزمایشی") {
		t.Fatalf("Persian test message not sent: %s", last)
	}

	// A broken bot: critical node.down still reaches email, the error never leaks the
	// token, and after 3 failures the banner count shows up.
	tgMu.Lock()
	tgOK = false
	tgMu.Unlock()
	for i := 0; i < 3; i++ {
		e.s.dispatchEvent(e.ctx, events.Event{ID: "evt_c" + strconv.Itoa(i), Type: events.NodeDown, CreatedAt: time.Now(), Data: map[string]any{"node_name": "n"}})
	}
	if len(mails) != 3 { // email is subscribed to node.down itself, so exactly once per event
		t.Fatalf("email deliveries = %d (%v)", len(mails), mails)
	}
	_, out = e.call(e.superT, "GET", "/api/v1/notifications", "")
	if out["broken_channels"].(float64) < 1 {
		t.Fatalf("broken banner missing: %v", out)
	}
	_, out = e.call(e.superT, "GET", "/api/v1/notification-channels", "")
	for _, it := range items(out) {
		m := it.(map[string]any)
		if m["id"] == tgID {
			if m["broken"] != true || strings.Contains(m["last_error"].(string), "SECRETTOKEN") || m["last_error"] == "" {
				t.Fatalf("channel state: %v", m)
			}
		}
	}
	// Repair: editing the channel clears the streak.
	tgMu.Lock()
	tgOK = true
	tgMu.Unlock()
	if code, out = e.call(e.superT, "PATCH", "/api/v1/notification-channels/"+tgID, `{"name":"ops2"}`); code != 200 || out["broken"] != false {
		t.Fatalf("repair: %d %v", code, out)
	}
	for _, c := range []string{tgID} {
		e.call(e.superT, "DELETE", "/api/v1/notification-channels/"+c, "")
	}
	e.exec(`DELETE FROM notification_channels`)
}

func (e *evEnv) queryRow(sql string, args []any, dest ...any) {
	e.t.Helper()
	c, err := pgx.Connect(e.ctx, e.dsn)
	if err != nil {
		e.t.Fatal(err)
	}
	defer c.Close(e.ctx)
	if err := c.QueryRow(e.ctx, sql, args...).Scan(dest...); err != nil {
		e.t.Fatal(err)
	}
}

// ---- scheduled backups --------------------------------------------------------------

func TestScheduledBackupToTelegramAndS3(t *testing.T) {
	e := newEvEnv(t)

	var mu sync.Mutex
	objects := map[string][]byte{}
	s3srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AKIDTEST/") {
			w.WriteHeader(403)
			return
		}
		key := strings.TrimPrefix(r.URL.Path, "/bkt/")
		switch {
		case r.Method == "PUT":
			objects[key] = b
		case r.Method == "DELETE":
			delete(objects, key)
			w.WriteHeader(204)
		case r.Method == "GET":
			var sb strings.Builder
			sb.WriteString(`<ListBucketResult><IsTruncated>false</IsTruncated>`)
			for k, v := range objects {
				if strings.HasPrefix(k, r.URL.Query().Get("prefix")) {
					sb.WriteString("<Contents><Key>" + k + "</Key><Size>" + strconv.Itoa(len(v)) + "</Size><LastModified>2024-01-01T00:00:00Z</LastModified></Contents>")
				}
			}
			sb.WriteString(`</ListBucketResult>`)
			_, _ = io.WriteString(w, sb.String())
		}
	}))
	defer s3srv.Close()

	var tgFile []byte
	var tgName, tgChat string
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			data, _ := io.ReadAll(p)
			mu.Lock()
			switch p.FormName() {
			case "document":
				tgFile, tgName = data, p.FileName()
			case "chat_id":
				tgChat = string(data)
			}
			mu.Unlock()
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer tg.Close()
	e.s.NotifyRT.TelegramAPIBase = tg.URL

	// Old backups already in the bucket: retention 2 must leave the new one + 1 old.
	for _, k := range []string{"panel/wgpanel-backup-20200101T000000Z.json", "panel/wgpanel-backup-20200102T000000Z.json", "panel/wgpanel-backup-20200103T000000Z.json", "panel/unrelated.txt"} {
		objects[k] = []byte("old")
	}

	cfg := `{"enabled":true,"interval_hours":6,"retention_count":2,"password":"correct horse battery",
	  "telegram":{"enabled":true,"bot_token":"9:TOK","chat_id":"-1001"},
	  "s3":{"enabled":true,"endpoint":"` + s3srv.URL + `","region":"ir-thr-at1","bucket":"bkt","prefix":"/panel/","access_key":"AKIDTEST","secret_key":"s3secret","path_style":true}}`
	code, out := e.call(e.superT, "PUT", "/api/v1/backup-schedule", cfg)
	if code != 200 || out["has_password"] != true || strings.Contains(toJSON(out), "s3secret") || strings.Contains(toJSON(out), "correct horse") {
		t.Fatalf("save schedule: %d %v", code, out)
	}
	if code, _ = e.call(e.superT, "PUT", "/api/v1/backup-schedule", `{"enabled":true,"interval_hours":0,"retention_count":2}`); code != 400 {
		t.Fatalf("interval 0 accepted: %d", code)
	}
	if code, _ = e.call(e.superT, "GET", "/api/v1/backup-schedule", ""); code != 200 {
		t.Fatal("get schedule")
	}
	if code, _ = e.call(e.superT, "POST", "/api/v1/backup-schedule/run", ""); code != 202 {
		t.Fatalf("run now = %d", code)
	}
	waitFor(t, func() bool {
		_, o := e.call(e.superT, "GET", "/api/v1/backup-schedule", "")
		last, _ := o["last"].(map[string]any)
		return o["running"] == false && last["s3"] != nil && last["telegram"] != nil
	})
	_, out = e.call(e.superT, "GET", "/api/v1/backup-schedule", "")
	last := out["last"].(map[string]any)
	for _, dest := range []string{"s3", "telegram"} {
		if m := last[dest].(map[string]any); m["status"] != "success" || m["size_bytes"].(float64) <= 0 {
			t.Fatalf("%s result: %v", dest, m)
		}
	}

	mu.Lock()
	var keys []string
	for k := range objects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var newKey string
	for _, k := range keys {
		if strings.HasPrefix(k, "panel/wgpanel-backup-2") && !strings.Contains(k, "2020") {
			newKey = k
		}
	}
	retained := 0
	for _, k := range keys {
		if strings.HasPrefix(k, "panel/wgpanel-backup-") {
			retained++
		}
	}
	file := objects[newKey]
	telegramFile, name, chat := tgFile, tgName, tgChat
	mu.Unlock()
	if newKey == "" || retained != 2 {
		t.Fatalf("retention: keys=%v", keys)
	}
	if _, still := objects["panel/unrelated.txt"]; !still {
		t.Fatal("retention must not touch unrelated objects")
	}
	if chat != "-1001" || !strings.HasPrefix(name, "wgpanel-backup-") || string(telegramFile) != string(file) {
		t.Fatalf("telegram upload: chat=%q name=%q same=%v", chat, name, string(telegramFile) == string(file))
	}

	// The uploaded file is a normal backup envelope: the right password opens it, a
	// wrong one does not, and its contents are the standard backup payload.
	var env backupcrypto.Envelope
	if err := json.Unmarshal(file, &env); err != nil {
		t.Fatal(err)
	}
	plain, err := backupcrypto.Open("correct horse battery", env)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	var payload backupPayload
	if err := json.Unmarshal(plain, &payload); err != nil || len(payload.Migrations) == 0 || payload.Secrets.AccountKeyEncryptionKey != itHexKey || payload.Tables["admins"] == nil {
		t.Fatalf("payload: %v %+v", err, payload.Migrations)
	}
	if _, err := backupcrypto.Open("wrong password", env); !errors.Is(err, backupcrypto.ErrWrongPassword) {
		t.Fatalf("wrong password: %v", err)
	}

	// backup.succeeded was emitted per destination, namespace global.
	var ok int
	for _, ev := range drain(e.s.Events) {
		if ev.Type == events.BackupSucceeded && ev.Namespace == "" {
			ok++
		}
	}
	if ok != 2 {
		t.Fatalf("backup.succeeded events = %d", ok)
	}

	// Failure path: bad S3 credentials -> failed row + backup.failed with the reason.
	e.s.NotifyRT.HTTP = &http.Client{Transport: failTransport{}}
	res := e.s.runBackup(e.ctx, "manual")
	if res["s3"] == nil || res["telegram"] == nil {
		t.Fatalf("expected failures: %v", res)
	}
	var failed int
	for _, ev := range drain(e.s.Events) {
		if ev.Type == events.BackupFailed && ev.Data["error"] != nil {
			failed++
		}
	}
	if failed != 2 {
		t.Fatalf("backup.failed events = %d", failed)
	}
	// The recorded error never contains secrets.
	_, out = e.call(e.superT, "GET", "/api/v1/backup-schedule", "")
	if strings.Contains(toJSON(out), ":TOK") || strings.Contains(toJSON(out), "s3secret") {
		t.Fatal("secret leaked through the run log")
	}
	e.exec(`UPDATE backup_schedule SET enabled = false, telegram_enabled = false, s3_enabled = false`)
}

type failTransport struct{}

func (failTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("connection refused")
}

func TestTelegramDocumentTooLarge(t *testing.T) {
	s := &Server{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	err := s.sendTelegramDocument(context.Background(), "t", "1", "f.json", "", make([]byte, telegramMaxDocumentBytes+1))
	if !errors.Is(err, errTelegramTooLarge) {
		t.Fatalf("err = %v", err)
	}
}

func TestScrubSecretRemovesTokenFromURLErrors(t *testing.T) {
	s := &Server{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), NotifyRT: NotifyRuntime{HTTP: &http.Client{Transport: failTransport{}}, TelegramAPIBase: "http://tg.invalid"}}
	err := s.sendTelegramMessage(context.Background(), "123:ABCSECRET", []string{"1"}, "hi")
	if err == nil || strings.Contains(err.Error(), "ABCSECRET") {
		t.Fatalf("token leaked or no error: %v", err)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("condition not reached in time")
}

func toJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
