package httpapi

import (
	"bytes"
	"crypto/hmac"
	crand "crypto/rand"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"wgpanel-api/internal/authcrypto"
	"wgpanel-api/internal/events"
	"wgpanel-api/internal/store"
)

// ---- fixtures ---------------------------------------------------------------------

const salesCallbackBase = "https://pay.example.test"

type fakeZarinpal struct {
	srv        *httptest.Server
	mu         sync.Mutex
	requests   []map[string]any
	verifies   []map[string]any
	verifyMode string // ok | already | mismatch
	prefix     string // authorities are globally unique in the orders table, across test runs
	n          int
}

func newFakeZarinpal(t *testing.T) *fakeZarinpal {
	f := &fakeZarinpal{verifyMode: "ok", prefix: fmt.Sprintf("%08x", rand.Uint32())}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case "/pg/v4/payment/request.json":
			f.requests = append(f.requests, body)
			f.n++
			fmt.Fprintf(w, `{"data":{"code":100,"message":"Success","authority":"A%s%027d","fee_type":"Merchant","fee":100},"errors":[]}`, f.prefix, f.n)
		case "/pg/v4/payment/verify.json":
			f.verifies = append(f.verifies, body)
			switch f.verifyMode {
			case "ok":
				fmt.Fprint(w, `{"data":{"code":100,"message":"Verified","card_pan":"603799******1234","ref_id":201},"errors":[]}`)
			case "already":
				fmt.Fprint(w, `{"data":{"code":101,"message":"Verified","ref_id":201},"errors":[]}`)
			default:
				w.WriteHeader(400)
				fmt.Fprint(w, `{"data":[],"errors":{"code":-50,"message":"amount mismatch","validations":[]}}`)
			}
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

type fakeNow struct {
	base     int
	srv      *httptest.Server
	mu       sync.Mutex
	invoices []map[string]any
	apiKeys  []string
}

func newFakeNow(t *testing.T) *fakeNow {
	f := &fakeNow{base: 1000 + rand.Intn(1<<30)}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.URL.Path != "/v1/invoice" {
			w.WriteHeader(404)
			return
		}
		f.invoices = append(f.invoices, body)
		f.apiKeys = append(f.apiKeys, r.Header.Get("x-api-key"))
		id := f.base + len(f.invoices)
		fmt.Fprintf(w, `{"id":"%d","invoice_url":"%s/pay?iid=%d","order_id":%q}`, id, f.srv.URL, id, body["order_id"])
	}))
	t.Cleanup(f.srv.Close)
	return f
}

type salesEnv struct {
	*evEnv
	zp     *fakeZarinpal
	now    *fakeNow
	nodeID string
	ipSeq  atomic.Int64
	ipBase int
}

func newSalesEnv(t *testing.T) *salesEnv {
	e := newEvEnv(t)
	relaxAPIRate(t, e.st)
	se := &salesEnv{evEnv: e, zp: newFakeZarinpal(t), now: newFakeNow(t), ipBase: rand.Intn(200)}

	old := gateways
	gateways = gatewayURLs{ZarinpalAPI: se.zp.srv.URL, ZarinpalWeb: se.zp.srv.URL, ZarinpalSandboxAPI: se.zp.srv.URL, ZarinpalSandboxWeb: se.zp.srv.URL,
		NowAPI: se.now.srv.URL, NowSandboxAPI: se.now.srv.URL}
	t.Cleanup(func() { gateways = old })

	pub := "sk-" + e.sfx
	node, err := e.st.CreateNode(e.ctx, "sales-"+e.sfx, "sales-"+e.sfx, "eu", "s.test:51820", fmt.Sprintf("10.%d.0.0/16", 100+rand.Intn(100)), 500, &pub)
	if err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE nodes SET status = 'online' WHERE id = $1`, node.ID)
	t.Cleanup(func() { e.exec(`UPDATE nodes SET status = 'pending' WHERE id = $1`, node.ID) })
	se.nodeID = node.ID

	code, out := e.call(e.superT, "PUT", "/api/v1/payments/settings", `{
		"card":{"enabled":true,"cards":[{"number":"۶۰۳۷-9970 1234 5678","holder":"Test Holder","bank":"Melli","enabled":true}]},
		"zarinpal":{"enabled":true,"sandbox":true,"merchant_id":"11111111-2222-3333-4444-555555555555"},
		"nowpayments":{"enabled":true,"api_key":"np-key-secret","ipn_secret":"ipn-secret-xyz","toman_per_usd":600000},
		"callback_base_url":"`+salesCallbackBase+`","public_buy_enabled":true}`)
	if code != 200 {
		t.Fatalf("payment settings: %d %v", code, out)
	}
	return se
}

func (se *salesEnv) ip() string {
	return fmt.Sprintf("203.0.113.%d", (se.ipBase+int(se.ipSeq.Add(1)))%250+1)
}

// anon issues an unauthenticated request from a fresh client address (each public
// endpoint has a per-IP rate limit and open-order cap that tests must not trip by accident).
func (se *salesEnv) anon(method, path, body string) (int, map[string]any) {
	return se.anonFrom(se.ip(), method, path, body)
}

func (se *salesEnv) anonFrom(ip, method, path, body string) (int, map[string]any) {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = ip + ":4000"
	rr := httptest.NewRecorder()
	se.h.ServeHTTP(rr, req)
	var out map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	if rr.Code == http.StatusSeeOther {
		out = map[string]any{"location": rr.Header().Get("Location")}
	}
	return rr.Code, out
}

// pngBytes returns a distinct tiny "PNG" each call: receipt images are de-duplicated
// across live orders, and the test database outlives a single test.
func pngBytes() []byte {
	b := make([]byte, 64)
	_, _ = crand.Read(b)
	return append([]byte("\x89PNG\r\n\x1a\n"), b...)
}

func (se *salesEnv) upload(path, token, tracking string, data []byte, ip string) (int, map[string]any) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("tracking_code", tracking)
	fw, _ := mw.CreateFormFile("receipt", "r.png")
	_, _ = fw.Write(data)
	_ = mw.Close()
	req := httptest.NewRequest("POST", path, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.RemoteAddr = ip + ":4000"
	rr := httptest.NewRecorder()
	se.h.ServeHTTP(rr, req)
	var out map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	return rr.Code, out
}

func (se *salesEnv) template(tok string, quotaGB, days int, global bool, price int64, purchasable bool) string {
	se.t.Helper()
	code, out := se.call(tok, "POST", "/api/v1/templates", fmt.Sprintf(`{"name":"plan-%s-%d","available_to_resellers":%v,"data_quota_gb":%d,"duration_days":%d}`,
		se.sfx, rand.Intn(1e6), global, quotaGB, days))
	if code != 201 {
		se.t.Fatalf("create template: %d %v", code, out)
	}
	id := out["id"].(string)
	if price > 0 {
		code, out = se.call(tok, "PUT", "/api/v1/templates/"+id+"/price", fmt.Sprintf(`{"price_toman":%d,"purchasable":%v}`, price, purchasable))
		if code != 200 {
			se.t.Fatalf("set price: %d %v", code, out)
		}
	}
	return id
}

// account creates an account (as the given session) and returns its id and sub token.
func (se *salesEnv) account(tok, label, templateID string) (id, token string) {
	se.t.Helper()
	body := fmt.Sprintf(`{"label":"%s-%s","data_quota_gb":1`, label, se.sfx)
	if templateID != "" {
		body += `,"template_id":"` + templateID + `"`
	}
	code, out := se.call(tok, "POST", "/api/v1/accounts", body+"}")
	if code != 201 {
		se.t.Fatalf("create account: %d %v", code, out)
	}
	path := out["subscription_path"].(string)
	return out["id"].(string), path[strings.LastIndex(path, "/")+1:]
}

func (se *salesEnv) accountQuota(tok, id string) float64 {
	_, out := se.call(tok, "GET", "/api/v1/accounts/"+id, "")
	q, _ := out["data_quota_bytes"].(float64)
	return q
}

func (se *salesEnv) drainEvents() []events.Event {
	var out []events.Event
	for {
		select {
		case ev := <-se.s.Events.C():
			out = append(out, ev)
		default:
			return out
		}
	}
}

func countEvents(evs []events.Event, typ string) int {
	n := 0
	for _, e := range evs {
		if e.Type == typ {
			n++
		}
	}
	return n
}

// buyRenewal creates a card-to-card renewal order for the account token; returns ref.
func (se *salesEnv) buyRenewal(token, templateID, method string) (string, map[string]any) {
	se.t.Helper()
	code, out := se.anon("POST", "/api/v1/sub/"+token+"/orders", fmt.Sprintf(`{"template_id":%q,"method":%q}`, templateID, method))
	if code != 201 {
		se.t.Fatalf("create order: %d %v", code, out)
	}
	return out["ref"].(string), out
}

func (se *salesEnv) orderByRef(ref string) store.Order {
	se.t.Helper()
	o, err := se.st.GetOrderByToken(se.ctx, ref)
	if err != nil {
		se.t.Fatal(err)
	}
	return o
}

// ---- pure unit tests --------------------------------------------------------------

func TestOrderStateMachineEdges(t *testing.T) {
	legal := [][2]string{
		{"pending", "awaiting_review"}, {"pending", "paid"}, {"pending", "failed"}, {"pending", "expired"}, {"pending", "cancelled"},
		{"awaiting_review", "paid"}, {"awaiting_review", "rejected"}, {"awaiting_review", "failed"}, {"awaiting_review", "cancelled"},
		{"paid", "fulfilled"}, {"expired", "paid"}, {"expired", "awaiting_review"},
	}
	for _, e := range legal {
		if !store.CanTransition(e[0], e[1]) {
			t.Errorf("%s -> %s should be legal", e[0], e[1])
		}
	}
	illegal := [][2]string{
		{"pending", "fulfilled"}, {"pending", "rejected"}, {"awaiting_review", "fulfilled"}, {"awaiting_review", "pending"},
		{"paid", "rejected"}, {"paid", "pending"}, {"paid", "cancelled"}, {"fulfilled", "paid"}, {"fulfilled", "rejected"},
		{"rejected", "paid"}, {"failed", "paid"}, {"cancelled", "paid"}, {"expired", "fulfilled"}, {"expired", "rejected"},
	}
	for _, e := range illegal {
		if store.CanTransition(e[0], e[1]) {
			t.Errorf("%s -> %s should be illegal", e[0], e[1])
		}
	}
}

func TestMoneyHelpers(t *testing.T) {
	if got := formatToman(1234567); got != "1,234,567" {
		t.Errorf("formatToman = %q", got)
	}
	if got := formatToman(999); got != "999" {
		t.Errorf("formatToman = %q", got)
	}
	if got := store.DiscountedPrice(100_000, 10); got != 90_000 {
		t.Errorf("discount = %d", got)
	}
	if got := store.DiscountedPrice(999, 50); got != 500 { // 499.5 rounds half up
		t.Errorf("discount rounding = %d", got)
	}
	if got := usdAmount(150_000, 600_000); got != 0.25 {
		t.Errorf("usdAmount = %v", got)
	}
	if got := usdAmount(100_001, 600_000); got != 0.17 { // rounds UP so the invoice never under-collects
		t.Errorf("usdAmount round up = %v", got)
	}
	if asciiDigits("۰۹۱۲٣") != "09123" {
		t.Errorf("asciiDigits failed")
	}
}

func TestCanonicalJSONAndSignature(t *testing.T) {
	// Keys sorted at every depth, no HTML escaping (like JSON.stringify of a sorted object).
	got, _ := canonicalJSON(map[string]any{"b": 1, "a": map[string]any{"z": "<x>&", "c": []any{2, 1}}})
	if string(got) != `{"a":{"c":[2,1],"z":"<x>&"},"b":1}` {
		t.Fatalf("canonical = %s", got)
	}
	// Independent HMAC-SHA512 of the same text.
	mac := hmac.New(sha512.New, []byte("secret"))
	mac.Write([]byte(`{"a":1,"b":2}`))
	want := hex.EncodeToString(mac.Sum(nil))
	if sig, _ := nowSignature("secret", map[string]any{"b": 2, "a": 1}); sig != want {
		t.Fatalf("signature %s != %s", sig, want)
	}
}

func TestRedactOrderRef(t *testing.T) {
	if got := redactPath("/api/v1/sub/abc123/orders/deadbeef/receipt"); got != "/api/v1/sub/[redacted]/orders/[redacted]/receipt" {
		t.Errorf("redactPath = %q", got)
	}
	if got := redactPath("/api/v1/sub/buy/orders/deadbeef"); got != "/api/v1/sub/[redacted]/orders/[redacted]" {
		t.Errorf("redactPath = %q", got)
	}
}

// ---- card to card: state machine, receipts, idempotent fulfilment -----------------

func TestCardToCardRenewalLifecycle(t *testing.T) {
	se := newSalesEnv(t)
	tpl := se.template(se.superT, 10, 30, true, 150_000, true)
	accID, token := se.account(se.superT, "cc", "")
	before := se.accountQuota(se.superT, accID)
	se.drainEvents()

	// The shop offers the plan with its server-side price and the enabled methods.
	code, shop := se.anon("GET", "/api/v1/sub/"+token+"/shop", "")
	if code != 200 || shop["enabled"] != true {
		t.Fatalf("shop: %d %v", code, shop)
	}
	found := false
	for _, p := range shop["plans"].([]any) {
		if p.(map[string]any)["id"] == tpl {
			found = p.(map[string]any)["price_toman"].(float64) == 150_000
		}
	}
	if !found {
		t.Fatalf("plan missing or mispriced in shop: %v", shop["plans"])
	}

	// A client-sent amount is ignored: the order is priced from the database.
	code, out := se.anon("POST", "/api/v1/sub/"+token+"/orders", fmt.Sprintf(`{"template_id":%q,"method":"card_to_card","amount_toman":1}`, tpl))
	if code != 201 || out["amount_toman"].(float64) != 150_000 || out["card"] == nil {
		t.Fatalf("create order: %d %v", code, out)
	}
	ref := out["ref"].(string)
	card := out["card"].(map[string]any)
	if card["number"] != "6037997012345678" { // Persian digits and separators normalised
		t.Fatalf("card number = %v", card["number"])
	}
	o := se.orderByRef(ref)

	// Approving before a receipt exists is illegal (pending -> paid is not an admin edge).
	if code, out := se.call(se.superT, "POST", "/api/v1/orders/"+o.ID+"/approve", ""); code != 409 {
		t.Fatalf("approve pending: %d %v", code, out)
	}

	// Receipt validation: bad tracking, wrong type, oversize, then a good upload.
	rpath := "/api/v1/sub/" + token + "/orders/" + ref + "/receipt"
	ip := se.ip()
	if code, out := se.upload(rpath, "", "x", pngBytes(), ip); code != 400 || ecode(out) != "invalid_tracking_code" {
		t.Fatalf("short tracking: %d %v", code, out)
	}
	if code, out := se.upload(rpath, "", "TRK-"+se.sfx[:10], []byte("<svg xmlns='http://www.w3.org/2000/svg'><script>1</script></svg>"), ip); code != 415 {
		t.Fatalf("svg upload: %d %v", code, out)
	}
	if code, out := se.upload(rpath, "", "TRK-"+se.sfx[:10], append(pngBytes(), bytes.Repeat([]byte{1}, 3<<20)...), ip); code != 413 {
		t.Fatalf("oversize upload: %d %v", code, out)
	}
	trk := "TRK" + se.sfx[:12]
	if code, out := se.upload(rpath, "", trk, pngBytes(), ip); code != 200 || out["status"] != "awaiting_review" {
		t.Fatalf("receipt: %d %v", code, out)
	}
	if code, out := se.upload(rpath, "", "OTHER"+se.sfx[:8], pngBytes(), ip); code != 409 {
		t.Fatalf("second receipt on the same order: %d %v", code, out)
	}
	evs := se.drainEvents()
	if countEvents(evs, events.PaymentAwaitingReview) != 1 {
		t.Fatalf("expected one payment.awaiting_review, got %d", countEvents(evs, events.PaymentAwaitingReview))
	}
	for _, ev := range evs {
		if ev.Type == events.PaymentAwaitingReview && ev.Namespace != "" {
			t.Fatalf("awaiting_review must be global, namespace %q", ev.Namespace)
		}
	}
	// A different order cannot reuse the same tracking code.
	ref2, _ := se.buyRenewal(token, tpl, "card_to_card")
	if code, out := se.upload("/api/v1/sub/"+token+"/orders/"+ref2+"/receipt", "", trk, pngBytes(), se.ip()); code != 409 || ecode(out) != "receipt_already_used" {
		t.Fatalf("reused tracking: %d %v", code, out)
	}

	// The receipt is viewable by staff, as a locked-down image.
	req := httptest.NewRequest("GET", "/api/v1/orders/"+o.ID+"/receipt", nil)
	req.Header.Set("Authorization", "Bearer "+se.superT)
	rr := httptest.NewRecorder()
	se.h.ServeHTTP(rr, req)
	if rr.Code != 200 || rr.Header().Get("Content-Type") != "image/png" || rr.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("receipt view: %d %v", rr.Code, rr.Header())
	}

	// Double approve, racing: exactly one wins, the renewal happens once.
	var wg sync.WaitGroup
	codes := make([]int, 6)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i], _ = se.call(se.superT, "POST", "/api/v1/orders/"+o.ID+"/approve", "")
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, c := range codes {
		if c == 200 {
			ok++
		} else if c != 409 {
			t.Fatalf("unexpected approve status %d", c)
		}
	}
	if ok != 1 {
		t.Fatalf("exactly one approve must win, got %d (%v)", ok, codes)
	}
	if got := se.accountQuota(se.superT, accID); got != before+10e9 {
		t.Fatalf("quota after approve = %v, want %v (renewed exactly once)", got, before+10e9)
	}
	// Re-running fulfilment (callback replay, retry sweep) is a no-op.
	for i := 0; i < 3; i++ {
		_ = se.s.fulfilOrder(se.ctx, o.ID)
	}
	if got := se.accountQuota(se.superT, accID); got != before+10e9 {
		t.Fatalf("quota after re-fulfil = %v", got)
	}
	if final := se.orderByRef(ref); final.Status != "fulfilled" || final.FulfilledAt == nil {
		t.Fatalf("final order: %+v", final)
	}
	evs = se.drainEvents()
	if countEvents(evs, events.PaymentApproved) != 1 || countEvents(evs, events.OrderFulfilled) != 1 {
		t.Fatalf("events approved=%d fulfilled=%d", countEvents(evs, events.PaymentApproved), countEvents(evs, events.OrderFulfilled))
	}
	// Terminal: cannot reject or approve a fulfilled order.
	if code, _ := se.call(se.superT, "POST", "/api/v1/orders/"+o.ID+"/reject", ""); code != 409 {
		t.Fatalf("reject fulfilled: %d", code)
	}
	var audits int
	se.queryRow(`SELECT count(*) FROM audit_log WHERE target = $1 AND action IN ('order.paid','order.fulfilled')`, []any{o.ID}, &audits)
	if audits != 2 {
		t.Fatalf("audit entries = %d, want 2", audits)
	}
}

func TestRejectAndRoleGates(t *testing.T) {
	se := newSalesEnv(t)
	tpl := se.template(se.superT, 5, 30, true, 90_000, true)
	_, token := se.account(se.superT, "rej", "")
	ref, _ := se.buyRenewal(token, tpl, "card_to_card")
	o := se.orderByRef(ref)
	if code, _ := se.upload("/api/v1/sub/"+token+"/orders/"+ref+"/receipt", "", "REJ"+se.sfx[:10], pngBytes(), se.ip()); code != 200 {
		t.Fatal("receipt")
	}
	se.drainEvents()

	res, resTok := se.reseller("rolegate")
	_ = res
	if code, _ := se.call(resTok, "POST", "/api/v1/orders/"+o.ID+"/approve", ""); code != 403 {
		t.Fatalf("reseller approve: %d", code)
	}
	adm, err := se.st.CreateAdmin(se.ctx, "support"+se.sfx, "h", "support")
	if err != nil {
		t.Fatal(err)
	}
	supTok, _ := authcrypto.IssueAccessToken(gateTestSecret, adm.ID, adm.Username, "support")
	if code, _ := se.call(supTok, "POST", "/api/v1/orders/"+o.ID+"/approve", ""); code != 403 {
		t.Fatalf("support approve: %d", code)
	}
	if code, out := se.call(se.superT, "POST", "/api/v1/orders/"+o.ID+"/reject", `{"note":"no such transfer"}`); code != 200 || out["status"] != "rejected" {
		t.Fatalf("reject: %d %v", code, out)
	}
	if countEvents(se.drainEvents(), events.PaymentRejected) != 1 {
		t.Fatal("payment.rejected not emitted")
	}
	// Rejected is terminal, and the buyer sees the note.
	if code, _ := se.call(se.superT, "POST", "/api/v1/orders/"+o.ID+"/approve", ""); code != 409 {
		t.Fatalf("approve rejected: %d", code)
	}
	if code, out := se.anon("GET", "/api/v1/sub/"+token+"/orders/"+ref, ""); code != 200 || out["review_note"] != "no such transfer" {
		t.Fatalf("buyer view: %d %v", code, out)
	}
	// The same tracking code is free again once its order is rejected.
	ref2, _ := se.buyRenewal(token, tpl, "card_to_card")
	// (the identical image hash is free again too)
	if code, out := se.upload("/api/v1/sub/"+token+"/orders/"+ref2+"/receipt", "", "REJ"+se.sfx[:10], pngBytes(), se.ip()); code != 200 {
		t.Fatalf("reuse after reject: %d %v", code, out)
	}
}

// ---- public scoping ---------------------------------------------------------------

func TestPublicEndpointScoping(t *testing.T) {
	se := newSalesEnv(t)
	tpl := se.template(se.superT, 5, 30, true, 90_000, true)
	idA, tokA := se.account(se.superT, "scopeA", "")
	_, tokB := se.account(se.superT, "scopeB", "")
	refA, _ := se.buyRenewal(tokA, tpl, "card_to_card")

	// B's token cannot see, pay or upload for A's order - indistinguishable from "unknown".
	if code, _ := se.anon("GET", "/api/v1/sub/"+tokB+"/orders/"+refA, ""); code != 404 {
		t.Fatalf("B reading A's order: %d", code)
	}
	if code, _ := se.upload("/api/v1/sub/"+tokB+"/orders/"+refA+"/receipt", "", "SCOPE"+se.sfx[:8], pngBytes(), se.ip()); code != 404 {
		t.Fatalf("B uploading to A's order: %d", code)
	}
	// A's order ref is not a /buy capability either (only purchase orders are).
	if code, _ := se.anon("GET", "/api/v1/sub/buy/orders/"+refA, ""); code != 404 {
		t.Fatalf("buy-flow lookup of a renewal order: %d", code)
	}
	// An order created through B's token renews B, never A.
	refB, _ := se.buyRenewal(tokB, tpl, "card_to_card")
	oB := se.orderByRef(refB)
	if oB.AccountID == nil || *oB.AccountID == idA {
		t.Fatalf("order created via B's token targets %v", oB.AccountID)
	}
	// Unknown token / unknown template / unsellable template.
	if code, _ := se.anon("POST", "/api/v1/sub/"+strings.Repeat("0", 48)+"/orders", fmt.Sprintf(`{"template_id":%q,"method":"card_to_card"}`, tpl)); code != 404 {
		t.Fatalf("unknown token: %d", code)
	}
	unpriced := se.template(se.superT, 5, 30, true, 0, false)
	if code, out := se.anon("POST", "/api/v1/sub/"+tokA+"/orders", fmt.Sprintf(`{"template_id":%q,"method":"card_to_card"}`, unpriced)); code != 404 {
		t.Fatalf("unpriced template: %d %v", code, out)
	}
	// Disabled method.
	se.call(se.superT, "PUT", "/api/v1/payments/settings", `{"card":{"enabled":false,"cards":[]},"callback_base_url":"`+salesCallbackBase+`","public_buy_enabled":true}`)
	if code, out := se.anon("POST", "/api/v1/sub/"+tokA+"/orders", fmt.Sprintf(`{"template_id":%q,"method":"card_to_card"}`, tpl)); code != 409 || ecode(out) != "payment_method_unavailable" {
		t.Fatalf("disabled method: %d %v", code, out)
	}
}

func TestResellerSelfServiceScoping(t *testing.T) {
	se := newSalesEnv(t)
	res, resTok := se.reseller("selfsvc")
	global := se.template(se.superT, 5, 30, true, 90_000, true)
	own := se.template(resTok, 20, 30, false, 40_000, true)
	_, tok := se.account(resTok, "cust", "")

	shopPlans := func() map[string]bool {
		code, shop := se.anon("GET", "/api/v1/sub/"+tok+"/shop", "")
		if code != 200 {
			t.Fatalf("shop: %d", code)
		}
		ids := map[string]bool{}
		for _, p := range shop["plans"].([]any) {
			ids[p.(map[string]any)["id"].(string)] = true
		}
		return ids
	}
	// Self-service is off by default: a reseller's customers can buy nothing.
	if ids := shopPlans(); len(ids) != 0 {
		t.Fatalf("self-service off but shop lists %v", ids)
	}
	if code, _ := se.call(se.superT, "PUT", "/api/v1/resellers/"+res.AdminID+"/sales", `{"self_service":true}`); code != 200 {
		t.Fatal("enable self-service")
	}
	ids := shopPlans()
	if !ids[own] || ids[global] || len(ids) != 1 {
		t.Fatalf("reseller customer must see only the reseller's own plan, got %v", ids)
	}
	if code, _ := se.anon("POST", "/api/v1/sub/"+tok+"/orders", fmt.Sprintf(`{"template_id":%q,"method":"card_to_card"}`, global)); code != 404 {
		t.Fatalf("buying a global plan on a reseller account: %d", code)
	}
	// Money goes to the admin: the order is the reseller's namespace, no wallet movement.
	ref, _ := se.buyRenewal(tok, own, "card_to_card")
	o := se.orderByRef(ref)
	if o.OwnerNamespace != store.ResellerNamespace(res.AdminID) {
		t.Fatalf("owner namespace %q", o.OwnerNamespace)
	}
	se.upload("/api/v1/sub/"+tok+"/orders/"+ref+"/receipt", "", "SELF"+se.sfx[:10], pngBytes(), se.ip())
	if code, out := se.call(se.superT, "POST", "/api/v1/orders/"+o.ID+"/approve", ""); code != 200 || out["status"] != "fulfilled" {
		t.Fatalf("approve: %d %v", code, out)
	}
	if bal, _ := se.st.WalletBalance(se.ctx, res.AdminID); bal != 0 {
		t.Fatalf("reseller wallet must not move on a customer's payment, balance %d", bal)
	}
	// The reseller sees its order but cannot act on it; another reseller sees nothing.
	if code, out := se.call(resTok, "GET", "/api/v1/orders", ""); code != 200 || len(items(out)) != 1 {
		t.Fatalf("reseller order list: %d %v", code, out)
	}
	_, otherTok := se.reseller("selfsvc-other")
	if code, out := se.call(otherTok, "GET", "/api/v1/orders", ""); code != 200 || len(items(out)) != 0 {
		t.Fatalf("other reseller sees orders: %v", out)
	}
	if code, _ := se.call(otherTok, "GET", "/api/v1/orders/"+o.ID, ""); code != 404 {
		t.Fatalf("foreign order get: %d", code)
	}
	// A reseller cannot price a global template.
	if code, _ := se.call(resTok, "PUT", "/api/v1/templates/"+global+"/price", `{"price_toman":1,"purchasable":true}`); code != 404 {
		t.Fatalf("reseller pricing a global template: %d", code)
	}
}

// ---- public purchase (/buy) -------------------------------------------------------

func TestPublicPurchaseCreatesAccountOnce(t *testing.T) {
	se := newSalesEnv(t)
	tpl := se.template(se.superT, 7, 30, true, 120_000, true)

	// Contact is required and validated.
	if code, out := se.anon("POST", "/api/v1/sub/buy/orders", fmt.Sprintf(`{"template_id":%q,"method":"card_to_card"}`, tpl)); code != 400 || ecode(out) != "invalid_contact" {
		t.Fatalf("missing contact: %d %v", code, out)
	}
	code, out := se.anon("POST", "/api/v1/sub/buy/orders", fmt.Sprintf(`{"template_id":%q,"method":"card_to_card","contact":"@buyer_%s"}`, tpl, se.sfx[:6]))
	if code != 201 {
		t.Fatalf("buy order: %d %v", code, out)
	}
	ref := out["ref"].(string)
	o := se.orderByRef(ref)
	if code, out := se.upload("/api/v1/sub/buy/orders/"+ref+"/receipt", "", "BUY"+se.sfx[:12], pngBytes(), se.ip()); code != 200 {
		t.Fatalf("receipt: %d %v", code, out)
	}
	if _, out := se.anon("GET", "/api/v1/sub/buy/orders/"+ref, ""); out["subscription_path"] != nil {
		t.Fatal("subscription link must not appear before fulfilment")
	}
	if code, out := se.call(se.superT, "POST", "/api/v1/orders/"+o.ID+"/approve", ""); code != 200 || out["status"] != "fulfilled" {
		t.Fatalf("approve: %d %v", code, out)
	}
	_, view := se.anon("GET", "/api/v1/sub/buy/orders/"+ref, "")
	path, _ := view["subscription_path"].(string)
	if !strings.HasPrefix(path, "/s/") {
		t.Fatalf("no subscription link after fulfilment: %v", view)
	}
	// Exactly one account with the order's external_ref exists, even after re-fulfilling.
	_ = se.s.fulfilOrder(se.ctx, o.ID)
	var n int
	se.queryRow(`SELECT count(*) FROM accounts WHERE external_ref = $1`, []any{"order:" + o.ID}, &n)
	if n != 1 {
		t.Fatalf("accounts for the order = %d", n)
	}
	// Idempotent across a crash: a second delivery of the same order finds the account.
	id1, err1 := se.s.deliverOrder(se.ctx, o)
	if err1 != nil || id1 == nil {
		t.Fatalf("redeliver: %v", err1)
	}
	se.queryRow(`SELECT count(*) FROM accounts WHERE external_ref = $1`, []any{"order:" + o.ID}, &n)
	if n != 1 {
		t.Fatalf("redelivery duplicated the account: %d", n)
	}

	// Switched off -> 404 and an empty disabled shop.
	se.call(se.superT, "PUT", "/api/v1/payments/settings", `{"card":{"enabled":true,"cards":[{"number":"6037997012345678","enabled":true}]},"callback_base_url":"`+salesCallbackBase+`","public_buy_enabled":false}`)
	if code, shop := se.anon("GET", "/api/v1/sub/buy/shop", ""); code != 200 || shop["enabled"] != false {
		t.Fatalf("closed shop: %d %v", code, shop)
	}
	if code, _ := se.anon("POST", "/api/v1/sub/buy/orders", fmt.Sprintf(`{"template_id":%q,"method":"card_to_card","contact":"@someone"}`, tpl)); code != 404 {
		t.Fatalf("buy while closed: %d", code)
	}
}

// ---- ZarinPal ---------------------------------------------------------------------

func TestZarinpalFlow(t *testing.T) {
	se := newSalesEnv(t)
	tpl := se.template(se.superT, 10, 30, true, 150_000, true)
	accID, token := se.account(se.superT, "zp", "")
	before := se.accountQuota(se.superT, accID)

	ref, out := se.buyRenewal(token, tpl, "zarinpal")
	redirect, _ := out["redirect_url"].(string)
	o := se.orderByRef(ref)
	if o.GatewayRef == nil || !strings.HasSuffix(redirect, "/pg/StartPay/"+*o.GatewayRef) {
		t.Fatalf("redirect %q / authority %v", redirect, o.GatewayRef)
	}
	// Request: Rial amount (x10) with the currency stated, our merchant id, our callback.
	se.zp.mu.Lock()
	rq := se.zp.requests[len(se.zp.requests)-1]
	se.zp.mu.Unlock()
	if rq["amount"].(float64) != 1_500_000 || rq["currency"] != "IRR" || rq["merchant_id"] != "11111111-2222-3333-4444-555555555555" {
		t.Fatalf("request.json body = %v", rq)
	}
	if cb := rq["callback_url"].(string); cb != salesCallbackBase+"/api/v1/sub/pay/zarinpal?order="+o.ID {
		t.Fatalf("callback_url = %s", cb)
	}

	cbPath := "/api/v1/sub/pay/zarinpal?order=" + o.ID
	// A callback for another authority is rejected without a verify call or state change.
	if code, out := se.anon("GET", cbPath+"&Authority=A-forged&Status=OK", ""); code != 400 || ecode(out) != "authority_mismatch" {
		t.Fatalf("forged authority: %d %v", code, out)
	}
	if len(se.zp.verifies) != 0 || se.orderByRef(ref).Status != "pending" {
		t.Fatal("forged callback changed state or called verify")
	}
	// The real callback: query-string amounts are irrelevant, verify uses the stored amount.
	code, loc := se.anon("GET", cbPath+"&Authority="+*o.GatewayRef+"&Status=OK&amount=1", "")
	if code != 303 || !strings.Contains(loc["location"].(string), "/s/"+token+"?order="+ref) {
		t.Fatalf("callback: %d %v", code, loc)
	}
	se.zp.mu.Lock()
	vf := se.zp.verifies[len(se.zp.verifies)-1]
	nv := len(se.zp.verifies)
	se.zp.mu.Unlock()
	if vf["amount"].(float64) != 1_500_000 || vf["authority"] != *o.GatewayRef || vf["merchant_id"] != "11111111-2222-3333-4444-555555555555" {
		t.Fatalf("verify.json body = %v", vf)
	}
	if got := se.orderByRef(ref); got.Status != "fulfilled" {
		t.Fatalf("status after verified callback = %s", got.Status)
	}
	// Replayed callback: no second verify, no second renewal.
	se.anon("GET", cbPath+"&Authority="+*o.GatewayRef+"&Status=OK", "")
	se.zp.mu.Lock()
	nv2 := len(se.zp.verifies)
	se.zp.mu.Unlock()
	if nv2 != nv {
		t.Fatalf("replayed callback verified again (%d -> %d)", nv, nv2)
	}
	if got := se.accountQuota(se.superT, accID); got != before+10e9 {
		t.Fatalf("quota = %v, want one renewal", got)
	}

	// Gateway refuses verification (amount mismatch -50): failed, nothing fulfilled.
	se.zp.mu.Lock()
	se.zp.verifyMode = "mismatch"
	se.zp.mu.Unlock()
	ref2, _ := se.buyRenewal(token, tpl, "zarinpal")
	o2 := se.orderByRef(ref2)
	se.anon("GET", "/api/v1/sub/pay/zarinpal?order="+o2.ID+"&Authority="+*o2.GatewayRef+"&Status=OK", "")
	if got := se.orderByRef(ref2); got.Status != "failed" {
		t.Fatalf("mismatch verify -> %s", got.Status)
	}
	// Buyer cancelled at the gateway (Status=NOK): failed, verify not called.
	se.zp.mu.Lock()
	se.zp.verifyMode = "ok"
	nv3 := len(se.zp.verifies)
	se.zp.mu.Unlock()
	ref3, _ := se.buyRenewal(token, tpl, "zarinpal")
	o3 := se.orderByRef(ref3)
	se.anon("GET", "/api/v1/sub/pay/zarinpal?order="+o3.ID+"&Authority="+*o3.GatewayRef+"&Status=NOK", "")
	se.zp.mu.Lock()
	nv4 := len(se.zp.verifies)
	se.zp.mu.Unlock()
	if got := se.orderByRef(ref3); got.Status != "failed" || nv4 != nv3 {
		t.Fatalf("NOK callback -> %s (verifies %d -> %d)", got.Status, nv3, nv4)
	}
	// Code 101 (already verified) counts as paid.
	se.zp.mu.Lock()
	se.zp.verifyMode = "already"
	se.zp.mu.Unlock()
	ref4, _ := se.buyRenewal(token, tpl, "zarinpal")
	o4 := se.orderByRef(ref4)
	se.anon("GET", "/api/v1/sub/pay/zarinpal?order="+o4.ID+"&Authority="+*o4.GatewayRef+"&Status=OK", "")
	if got := se.orderByRef(ref4); got.Status != "fulfilled" {
		t.Fatalf("101 verify -> %s", got.Status)
	}
	// Unknown order id.
	if code, _ := se.anon("GET", "/api/v1/sub/pay/zarinpal?order=00000000-0000-4000-8000-000000000000&Authority=A&Status=OK", ""); code != 404 {
		t.Fatalf("unknown order callback: %d", code)
	}
}

// ---- NowPayments ------------------------------------------------------------------

func (se *salesEnv) ipn(payload map[string]any, secret string) (int, map[string]any) {
	body, _ := json.Marshal(payload) // map keys come out sorted: the signed text
	mac := hmac.New(sha512.New, []byte(secret))
	mac.Write(body)
	req := httptest.NewRequest("POST", "/api/v1/sub/pay/nowpayments", bytes.NewReader(body))
	req.Header.Set("x-nowpayments-sig", hex.EncodeToString(mac.Sum(nil)))
	req.RemoteAddr = se.ip() + ":4000"
	rr := httptest.NewRecorder()
	se.h.ServeHTTP(rr, req)
	var out map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	return rr.Code, out
}

func TestNowPaymentsFlow(t *testing.T) {
	se := newSalesEnv(t)
	tpl := se.template(se.superT, 10, 30, true, 150_000, true)
	accID, token := se.account(se.superT, "np", "")
	before := se.accountQuota(se.superT, accID)

	ref, out := se.buyRenewal(token, tpl, "nowpayments")
	if !strings.Contains(out["redirect_url"].(string), "/pay?iid=") {
		t.Fatalf("invoice url: %v", out)
	}
	o := se.orderByRef(ref)
	se.now.mu.Lock()
	inv := se.now.invoices[len(se.now.invoices)-1]
	key := se.now.apiKeys[len(se.now.apiKeys)-1]
	se.now.mu.Unlock()
	if key != "np-key-secret" || inv["price_amount"].(float64) != 0.25 || inv["price_currency"] != "usd" || inv["order_id"] != o.ID ||
		inv["ipn_callback_url"] != salesCallbackBase+"/api/v1/sub/pay/nowpayments" {
		t.Fatalf("invoice body = %v (key %s)", inv, key)
	}

	invID, _ := strconv.Atoi(*o.GatewayRef)
	payload := func(status string, mut func(map[string]any)) map[string]any {
		p := map[string]any{"payment_id": 5077125051, "payment_status": status, "order_id": o.ID, "price_amount": 0.25, "price_currency": "usd",
			"pay_amount": 0.0004, "actually_paid": 0.0004, "pay_currency": "btc", "invoice_id": invID,
			"fee": map[string]any{"currency": "btc", "depositFee": 0.00001}}
		if mut != nil {
			mut(p)
		}
		return p
	}

	// Bad signature: 401, nothing changes (and nothing about the order leaks).
	if code, out := se.ipn(payload("finished", nil), "wrong-secret"); code != 401 || ecode(out) != "invalid_signature" {
		t.Fatalf("bad signature: %d %v", code, out)
	}
	// Missing signature header.
	req := httptest.NewRequest("POST", "/api/v1/sub/pay/nowpayments", strings.NewReader(`{"order_id":"x"}`))
	req.RemoteAddr = se.ip() + ":4000"
	rr := httptest.NewRecorder()
	se.h.ServeHTTP(rr, req)
	if rr.Code != 401 {
		t.Fatalf("unsigned: %d", rr.Code)
	}
	// Correctly signed but for a different amount: rejected, order untouched.
	if code, out := se.ipn(payload("finished", func(p map[string]any) { p["price_amount"] = 0.01 }), "ipn-secret-xyz"); code != 400 || ecode(out) != "amount_mismatch" {
		t.Fatalf("amount mismatch: %d %v", code, out)
	}
	if code, out := se.ipn(payload("finished", func(p map[string]any) { p["price_currency"] = "eur" }), "ipn-secret-xyz"); code != 400 || ecode(out) != "amount_mismatch" {
		t.Fatalf("currency mismatch: %d %v", code, out)
	}
	if se.orderByRef(ref).Status != "pending" {
		t.Fatal("rejected callbacks must not move the order")
	}
	// Non-final statuses are accepted and ignored.
	for _, st := range []string{"waiting", "confirming", "confirmed", "sending"} {
		if code, _ := se.ipn(payload(st, nil), "ipn-secret-xyz"); code != 200 {
			t.Fatalf("%s: %d", st, code)
		}
	}
	if se.orderByRef(ref).Status != "pending" {
		t.Fatal("non-final status moved the order")
	}
	// partially_paid goes to a human, never to fulfilment.
	se.drainEvents()
	if code, _ := se.ipn(payload("partially_paid", func(p map[string]any) { p["actually_paid"] = 0.0002 }), "ipn-secret-xyz"); code != 200 {
		t.Fatalf("partially_paid: %d", code)
	}
	if got := se.orderByRef(ref); got.Status != "awaiting_review" {
		t.Fatalf("partially_paid -> %s", got.Status)
	}
	if countEvents(se.drainEvents(), events.PaymentAwaitingReview) != 1 {
		t.Fatal("partial payment must raise payment.awaiting_review")
	}
	if se.accountQuota(se.superT, accID) != before {
		t.Fatal("partial payment fulfilled the order")
	}
	// finished -> paid -> fulfilled; replays fulfil nothing more.
	for i := 0; i < 3; i++ {
		if code, out := se.ipn(payload("finished", nil), "ipn-secret-xyz"); code != 200 {
			t.Fatalf("finished #%d: %d %v", i, code, out)
		}
	}
	if got := se.orderByRef(ref); got.Status != "fulfilled" {
		t.Fatalf("finished -> %s", got.Status)
	}
	if got := se.accountQuota(se.superT, accID); got != before+10e9 {
		t.Fatalf("quota = %v, want exactly one renewal", got)
	}

	// failed / expired map to terminal states on a fresh order.
	ref2, _ := se.buyRenewal(token, tpl, "nowpayments")
	o2 := se.orderByRef(ref2)
	inv2, _ := strconv.Atoi(*o2.GatewayRef)
	failed := payload("failed", func(p map[string]any) { p["order_id"], p["invoice_id"] = o2.ID, inv2 })
	// An IPN naming another order's invoice is refused (invoice_id binds it to its order).
	if code, out := se.ipn(payload("failed", func(p map[string]any) { p["order_id"] = o2.ID }), "ipn-secret-xyz"); code != 400 || ecode(out) != "invoice_mismatch" {
		t.Fatalf("invoice mismatch: %d %v", code, out)
	}
	if code, _ := se.ipn(failed, "ipn-secret-xyz"); code != 200 || se.orderByRef(ref2).Status != "failed" {
		t.Fatalf("failed -> %s", se.orderByRef(ref2).Status)
	}
	// A late "finished" for a failed order must not resurrect it.
	late := payload("finished", func(p map[string]any) { p["order_id"], p["invoice_id"] = o2.ID, inv2 })
	se.ipn(late, "ipn-secret-xyz")
	if se.orderByRef(ref2).Status != "failed" {
		t.Fatal("failed order was resurrected")
	}
}

func TestExpiredOrderStillAcceptsVerifiedPayment(t *testing.T) {
	se := newSalesEnv(t)
	tpl := se.template(se.superT, 10, 30, true, 150_000, true)
	accID, token := se.account(se.superT, "late", "")
	before := se.accountQuota(se.superT, accID)
	ref, _ := se.buyRenewal(token, tpl, "zarinpal")
	o := se.orderByRef(ref)
	se.exec(`UPDATE orders SET expires_at = now() - interval '1 minute' WHERE id = $1`, o.ID)
	se.s.salesTick(se.ctx)
	if got := se.orderByRef(ref); got.Status != "expired" {
		t.Fatalf("status = %s", got.Status)
	}
	se.anon("GET", "/api/v1/sub/pay/zarinpal?order="+o.ID+"&Authority="+*o.GatewayRef+"&Status=OK", "")
	if got := se.orderByRef(ref); got.Status != "fulfilled" {
		t.Fatalf("late verified payment -> %s", got.Status)
	}
	if se.accountQuota(se.superT, accID) != before+10e9 {
		t.Fatal("late payment not delivered")
	}
}

// ---- wallet -----------------------------------------------------------------------

func TestWalletChargingAndRaces(t *testing.T) {
	se := newSalesEnv(t)
	res, resTok := se.reseller("wallet")
	global := se.template(se.superT, 5, 30, true, 100_000, false)
	ownTpl := se.template(resTok, 5, 30, false, 0, false)
	se.call(se.superT, "PUT", "/api/v1/resellers/"+res.AdminID+"/sales", `{"discount_percent":10,"charge_wallet":true}`)

	create := func(label string) (int, map[string]any) {
		return se.call(resTok, "POST", "/api/v1/accounts", fmt.Sprintf(`{"label":"%s","template_id":%q}`, label, global))
	}
	if code, out := create("w-poor-" + se.sfx); code != 402 || ecode(out) != "insufficient_balance" {
		t.Fatalf("empty wallet: %d %v", code, out)
	}
	// An unpriced / own template is never charged.
	if code, out := se.call(resTok, "POST", "/api/v1/accounts", fmt.Sprintf(`{"label":"w-own-%s","template_id":%q}`, se.sfx, ownTpl)); code != 201 {
		t.Fatalf("own template: %d %v", code, out)
	}

	// Manual credit of exactly three discounted prices (3 x 90,000).
	if code, out := se.call(se.superT, "POST", "/api/v1/resellers/"+res.AdminID+"/wallet/adjust", `{"amount_toman":270000,"reason":"initial"}`); code != 200 || out["balance_toman"].(float64) != 270000 {
		t.Fatalf("credit: %d %v", code, out)
	}
	if code, _ := se.call(se.superT, "POST", "/api/v1/resellers/"+res.AdminID+"/wallet/adjust", `{"amount_toman":5}`); code != 400 {
		t.Fatalf("adjust without reason: %d", code)
	}

	// Ten concurrent creates against a balance for three: never overdrawn.
	var wg sync.WaitGroup
	var created, refused atomic.Int32
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			code, out := create(fmt.Sprintf("w-race-%s-%d", se.sfx, i))
			switch {
			case code == 201:
				created.Add(1)
			case code == 402 && ecode(out) == "insufficient_balance":
				refused.Add(1)
			default:
				t.Errorf("unexpected %d %v", code, out)
			}
		}(i)
	}
	wg.Wait()
	if created.Load() != 3 || refused.Load() != 7 {
		t.Fatalf("created %d refused %d, want 3/7", created.Load(), refused.Load())
	}
	if bal, _ := se.st.WalletBalance(se.ctx, res.AdminID); bal != 0 {
		t.Fatalf("balance = %d, want 0", bal)
	}
	var sum int64
	se.queryRow(`SELECT COALESCE(sum(amount_toman),0) FROM wallet_ledger WHERE admin_id = $1`, []any{res.AdminID}, &sum)
	if sum != 0 {
		t.Fatalf("ledger sums to %d, the balance is 0", sum)
	}

	// The ledger is append-only.
	se.mustFail(`UPDATE wallet_ledger SET amount_toman = 1 WHERE admin_id = $1`, res.AdminID)
	se.mustFail(`DELETE FROM wallet_ledger WHERE admin_id = $1`, res.AdminID)

	// Manual debit beyond the balance is refused too; within it, it works.
	if code, out := se.call(se.superT, "POST", "/api/v1/resellers/"+res.AdminID+"/wallet/adjust", `{"amount_toman":-1,"reason":"x"}`); code != 402 {
		t.Fatalf("overdraw debit: %d %v", code, out)
	}

	// Bulk create stops at the money: balance for 2, ask 5 -> 2 ok, 3 insufficient_balance.
	se.call(se.superT, "POST", "/api/v1/resellers/"+res.AdminID+"/wallet/adjust", `{"amount_toman":180000,"reason":"bulk"}`)
	code, out := se.call(resTok, "POST", "/api/v1/accounts/bulk", fmt.Sprintf(`{"action":"create","count":5,"label_prefix":"wb%s","template_id":%q}`, se.sfx[:6], global))
	if code != 200 || out["succeeded"].(float64) != 2 || out["failed"].(float64) != 3 {
		t.Fatalf("bulk: %d %v", code, out)
	}
	for _, r := range out["results"].([]any)[2:] {
		if ecode(map[string]any{"error": r.(map[string]any)["error"]}) != "insufficient_balance" {
			t.Fatalf("bulk failure code: %v", r)
		}
	}

	// Renewal with a template is charged as well (and 402s when the wallet is empty).
	accID, _ := se.account(resTok, "renewme", ownTpl)
	if code, out := se.call(resTok, "POST", "/api/v1/accounts/"+accID+"/renew", fmt.Sprintf(`{"template_id":%q}`, global)); code != 402 {
		t.Fatalf("renew empty wallet: %d %v", code, out)
	}
	se.call(se.superT, "POST", "/api/v1/resellers/"+res.AdminID+"/wallet/adjust", `{"amount_toman":100000,"reason":"renew"}`)
	before := se.accountQuota(resTok, accID)
	if code, out := se.call(resTok, "POST", "/api/v1/accounts/"+accID+"/renew", fmt.Sprintf(`{"template_id":%q}`, global)); code != 200 {
		t.Fatalf("renew: %d %v", code, out)
	}
	if se.accountQuota(resTok, accID) != before+5e9 {
		t.Fatal("renew did not add the template quota")
	}
	if bal, _ := se.st.WalletBalance(se.ctx, res.AdminID); bal != 10_000 {
		t.Fatalf("balance after renew = %d, want 10000", bal)
	}
	// Per-template override beats the discount.
	se.call(se.superT, "PUT", "/api/v1/resellers/"+res.AdminID+"/sales", fmt.Sprintf(`{"discount_percent":10,"charge_wallet":true,"overrides":[{"template_id":%q,"price_toman":5000}]}`, global))
	if code, _ := se.call(resTok, "POST", "/api/v1/accounts", fmt.Sprintf(`{"label":"w-ovr-%s","template_id":%q}`, se.sfx, global)); code != 201 {
		t.Fatal("override price create")
	}
	if bal, _ := se.st.WalletBalance(se.ctx, res.AdminID); bal != 5_000 {
		t.Fatalf("balance after override charge = %d, want 5000", bal)
	}
}

// mustFail runs a statement that the database has to refuse.
func (se *salesEnv) mustFail(sql string, args ...any) {
	se.t.Helper()
	c, err := pgx.Connect(se.ctx, se.dsn)
	if err != nil {
		se.t.Fatal(err)
	}
	defer c.Close(se.ctx)
	if _, err := c.Exec(se.ctx, sql, args...); err == nil {
		se.t.Fatalf("expected %q to fail", sql)
	}
}

func TestWalletRefundWhenCreateFails(t *testing.T) {
	se := newSalesEnv(t)
	one := 1
	r, err := se.st.CreateReseller(se.ctx, store.NewResellerParams{Username: "refund" + se.sfx, PasswordHash: "h", MaxAccounts: &one})
	if err != nil {
		t.Fatal(err)
	}
	tok, _ := authcrypto.IssueAccessToken(gateTestSecret, r.AdminID, r.Username, "reseller")
	global := se.template(se.superT, 1, 30, true, 50_000, false)
	se.call(se.superT, "PUT", "/api/v1/resellers/"+r.AdminID+"/sales", `{"charge_wallet":true}`)
	se.call(se.superT, "POST", "/api/v1/resellers/"+r.AdminID+"/wallet/adjust", `{"amount_toman":200000,"reason":"fund"}`)
	body := fmt.Sprintf(`{"label":"rf-%s-%%d","template_id":%q}`, se.sfx, global)
	if code, out := se.call(tok, "POST", "/api/v1/accounts", fmt.Sprintf(body, 1)); code != 201 {
		t.Fatalf("first: %d %v", code, out)
	}
	// Second hits max_accounts: the debit is refunded, balance unchanged.
	code, out := se.call(tok, "POST", "/api/v1/accounts", fmt.Sprintf(body, 2))
	if code == 201 {
		t.Fatalf("second create should be refused: %v", out)
	}
	if bal, _ := se.st.WalletBalance(se.ctx, r.AdminID); bal != 150_000 {
		t.Fatalf("balance = %d, want 150000 (one charge, one refunded)", bal)
	}
	_, led := se.call(se.superT, "GET", "/api/v1/resellers/"+r.AdminID+"/wallet", "")
	kinds := map[string]int{}
	for _, it := range items(led) {
		kinds[it.(map[string]any)["kind"].(string)]++
	}
	if kinds["purchase"] != 2 || kinds["refund"] != 1 || kinds["manual_credit"] != 1 {
		t.Fatalf("ledger kinds = %v", kinds)
	}
}

func TestWalletTopupViaCard(t *testing.T) {
	se := newSalesEnv(t)
	res, resTok := se.reseller("topup")
	se.drainEvents()
	if code, out := se.call(resTok, "POST", "/api/v1/wallet/topups", `{"amount_toman":5,"method":"card_to_card"}`); code != 400 {
		t.Fatalf("tiny topup: %d %v", code, out)
	}
	code, out := se.call(resTok, "POST", "/api/v1/wallet/topups", `{"amount_toman":300000,"method":"card_to_card"}`)
	if code != 201 {
		t.Fatalf("topup: %d %v", code, out)
	}
	id := out["order"].(map[string]any)["id"].(string)
	if code, out := se.upload("/api/v1/orders/"+id+"/receipt", resTok, "TOP"+se.sfx[:12], pngBytes(), se.ip()); code != 200 {
		t.Fatalf("receipt: %d %v", code, out)
	}
	// A different reseller cannot upload to it; the owner cannot approve it.
	_, otherTok := se.reseller("topup-other")
	if code, _ := se.upload("/api/v1/orders/"+id+"/receipt", otherTok, "TOQ"+se.sfx[:12], pngBytes(), se.ip()); code != 404 {
		t.Fatalf("foreign receipt upload: %d", code)
	}
	if code, _ := se.call(resTok, "POST", "/api/v1/orders/"+id+"/approve", ""); code != 403 {
		t.Fatalf("reseller self-approve: %d", code)
	}
	se.drainEvents()

	var wg sync.WaitGroup
	codes := make([]int, 5)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i], _ = se.call(se.superT, "POST", "/api/v1/orders/"+id+"/approve", "")
		}(i)
	}
	wg.Wait()
	wins := 0
	for _, c := range codes {
		if c == 200 {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("approve winners = %d (%v)", wins, codes)
	}
	if bal, _ := se.st.WalletBalance(se.ctx, res.AdminID); bal != 300_000 {
		t.Fatalf("balance = %d, want exactly one credit", bal)
	}
	// Fulfilment replays do not credit again.
	_ = se.s.fulfilOrder(se.ctx, id)
	if _, _, changed, err := se.st.FulfilTopup(se.ctx, id); err != nil || changed {
		t.Fatalf("FulfilTopup replay: changed=%v err=%v", changed, err)
	}
	if bal, _ := se.st.WalletBalance(se.ctx, res.AdminID); bal != 300_000 {
		t.Fatalf("balance after replays = %d", bal)
	}
	evs := se.drainEvents()
	var credited *events.Event
	for i := range evs {
		if evs[i].Type == events.WalletCredited {
			credited = &evs[i]
		}
	}
	if countEvents(evs, events.WalletCredited) != 1 || credited.Namespace != store.ResellerNamespace(res.AdminID) {
		t.Fatalf("wallet.credited events: %d", countEvents(evs, events.WalletCredited))
	}
	// Wallet and ledger endpoints for the reseller (and not for staff / plain keys).
	if code, out := se.call(resTok, "GET", "/api/v1/wallet", ""); code != 200 || out["balance_toman"].(float64) != 300_000 {
		t.Fatalf("GET wallet: %d %v", code, out)
	}
	if code, out := se.call(resTok, "GET", "/api/v1/wallet/ledger", ""); code != 200 || len(items(out)) != 1 {
		t.Fatalf("ledger: %d %v", code, out)
	}
	if code, _ := se.call(se.superT, "GET", "/api/v1/wallet", ""); code != 404 {
		t.Fatalf("staff wallet: %d", code)
	}
}

func TestTopupViaZarinpalCreditsOnce(t *testing.T) {
	se := newSalesEnv(t)
	res, resTok := se.reseller("topupzp")
	code, out := se.call(resTok, "POST", "/api/v1/wallet/topups", `{"amount_toman":200000,"method":"zarinpal"}`)
	if code != 201 {
		t.Fatalf("topup: %d %v", code, out)
	}
	id := out["order"].(map[string]any)["id"].(string)
	o, _ := se.st.GetOrder(se.ctx, id)
	cb := "/api/v1/sub/pay/zarinpal?order=" + id + "&Authority=" + *o.GatewayRef + "&Status=OK"
	for i := 0; i < 3; i++ {
		se.anon("GET", cb, "")
	}
	if bal, _ := se.st.WalletBalance(se.ctx, res.AdminID); bal != 200_000 {
		t.Fatalf("balance = %d, want one credit", bal)
	}
}

// ---- settings, secrets, prices, bot API -------------------------------------------

func TestPaymentSettingsSecretsAndValidation(t *testing.T) {
	se := newSalesEnv(t)
	_, out := se.call(se.superT, "GET", "/api/v1/payments/settings", "")
	b, _ := json.Marshal(out)
	for _, secret := range []string{"np-key-secret", "ipn-secret-xyz", "11111111-2222-3333-4444-555555555555"} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("settings response leaks %q", secret)
		}
	}
	z := out["zarinpal"].(map[string]any)
	n := out["nowpayments"].(map[string]any)
	if z["has_merchant_id"] != true || n["has_api_key"] != true || n["has_ipn_secret"] != true {
		t.Fatalf("has_* flags: %v %v", z, n)
	}
	// Encrypted at rest.
	var enc string
	se.queryRow(`SELECT nowpayments_ipn_secret_encrypted FROM payment_settings WHERE id = 1`, nil, &enc)
	if enc == "" || strings.Contains(enc, "ipn-secret-xyz") {
		t.Fatalf("ipn secret not encrypted at rest: %q", enc)
	}
	// Omitted secrets are kept on update.
	se.call(se.superT, "PUT", "/api/v1/payments/settings", `{"card":{"enabled":true,"cards":[{"number":"6037997012345678","enabled":true}]},"zarinpal":{"enabled":true},"nowpayments":{"enabled":true,"toman_per_usd":600000},"callback_base_url":"`+salesCallbackBase+`"}`)
	cfg, err := se.s.loadPaymentConfig(se.ctx)
	if err != nil || cfg.NowIPNSecret != "ipn-secret-xyz" || cfg.ZarinpalMerchant == "" {
		t.Fatalf("secrets not kept: %v %+v", err, cfg)
	}
	for name, body := range map[string]string{
		"enable zarinpal without id": `{"zarinpal":{"enabled":true}}`,
		"bad merchant":               `{"zarinpal":{"merchant_id":"nope"}}`,
		"card without cards":         `{"card":{"enabled":true,"cards":[]}}`,
		"bad card number":            `{"card":{"cards":[{"number":"12","enabled":true}]}}`,
		"nowpayments without rate":   `{"nowpayments":{"enabled":true,"api_key":"k","ipn_secret":"s"}}`,
		"http callback":              `{"callback_base_url":"http://pay.example.test"}`,
		"callback with path":         `{"callback_base_url":"https://pay.example.test/x"}`,
		"negative rate":              `{"nowpayments":{"toman_per_usd":-1}}`,
	} {
		// Start from a clean slate for the "enable without X" cases.
		if strings.Contains(name, "without id") || strings.Contains(name, "without rate") {
			se.exec(`UPDATE payment_settings SET zarinpal_merchant_encrypted = '', nowpayments_api_key_encrypted = '', nowpayments_ipn_secret_encrypted = '', nowpayments_toman_per_usd = 0 WHERE id = 1`)
		}
		if code, out := se.call(se.superT, "PUT", "/api/v1/payments/settings", body); code != 400 {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
	// Only super admins.
	adm, _ := se.st.CreateAdmin(se.ctx, "opr"+se.sfx, "h", "operator")
	opTok, _ := authcrypto.IssueAccessToken(gateTestSecret, adm.ID, adm.Username, "operator")
	if code, _ := se.call(opTok, "GET", "/api/v1/payments/settings", ""); code != 403 {
		t.Fatalf("operator settings: %d", code)
	}
	_, resTok := se.reseller("settings")
	if code, _ := se.call(resTok, "GET", "/api/v1/payments/settings", ""); code != 403 {
		t.Fatalf("reseller settings: %d", code)
	}
}

func TestTemplatePriceValidation(t *testing.T) {
	se := newSalesEnv(t)
	tpl := se.template(se.superT, 1, 30, true, 0, false)
	for _, body := range []string{`{"price_toman":-1}`, `{"price_toman":0,"purchasable":true}`, `{"price_toman":99999999999}`} {
		if code, _ := se.call(se.superT, "PUT", "/api/v1/templates/"+tpl+"/price", body); code != 400 {
			t.Errorf("%s: %d", body, code)
		}
	}
	if code, _ := se.call(se.superT, "PUT", "/api/v1/templates/00000000-0000-4000-8000-000000000000/price", `{"price_toman":5}`); code != 404 {
		t.Fatal("unknown template")
	}
	if code, out := se.call(se.superT, "PUT", "/api/v1/templates/"+tpl+"/price", `{"price_toman":42000,"purchasable":true}`); code != 200 || out["price_toman"].(float64) != 42000 {
		t.Fatalf("set: %d %v", code, out)
	}
	_, list := se.call(se.superT, "GET", "/api/v1/template-prices", "")
	found := false
	for _, it := range items(list) {
		if it.(map[string]any)["template_id"] == tpl {
			found = true
		}
	}
	if !found {
		t.Fatal("price not listed")
	}
}

func TestBotAPIScopedOrdersAndWallet(t *testing.T) {
	se := newSalesEnv(t)
	res, _ := se.reseller("botres")

	mk := func(id string) {
		if _, err := se.st.CreateAPIKey(se.ctx, id, mustEnc(t, "sec-"+id), id, []string{"sales-" + se.sfx}, []string{"read", "create", "update"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	keyA, keyB := "salesA"+se.sfx, "salesB"+se.sfx
	mk(keyA)
	mk(keyB)
	signed := func(keyID, method, path, body string) (int, map[string]any) {
		ts := fmt.Sprint(time.Now().Unix())
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("X-API-Key", keyID)
		req.Header.Set("X-Timestamp", ts)
		req.Header.Set("X-Signature", hexHMAC("sec-"+keyID, method+"\n"+strings.SplitN(path, "?", 2)[0]+"\n"+ts+"\n"+body))
		rr := httptest.NewRecorder()
		se.h.ServeHTTP(rr, req)
		var out map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
		return rr.Code, out
	}
	// Key A owns an account; an order for it belongs to namespace A only.
	code, out := signed(keyA, "POST", "/api/v1/accounts", `{"label":"bota-`+se.sfx+`"}`)
	if code != 201 {
		t.Fatalf("key account: %d %v", code, out)
	}
	_ = out
	// Plain key namespaces sell nothing publicly (only staff-owned accounts and
	// self-service resellers do), so seed an order for the key's namespace directly.
	o, err := se.st.CreateOrder(se.ctx, store.NewOrder{PublicToken: "tok" + se.sfx, Kind: store.KindAccountRenew, Method: store.MethodCard, AmountToman: 1000,
		OwnerNamespace: keyA, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if code, out := signed(keyA, "GET", "/api/v1/orders", ""); code != 200 || len(items(out)) != 1 {
		t.Fatalf("key A orders: %d %v", code, out)
	}
	if code, out := signed(keyB, "GET", "/api/v1/orders", ""); code != 200 || len(items(out)) != 0 {
		t.Fatalf("key B orders: %d %v", code, out)
	}
	if code, _ := signed(keyB, "GET", "/api/v1/orders/"+o.ID, ""); code != 404 {
		t.Fatalf("key B foreign order: %d", code)
	}
	if code, _ := signed(keyA, "GET", "/api/v1/orders/"+o.ID, ""); code != 200 {
		t.Fatalf("key A own order: %d", code)
	}
	// Keys cannot approve, and a plain key has no wallet.
	if code, _ := signed(keyA, "POST", "/api/v1/orders/"+o.ID+"/approve", ""); code != 401 && code != 403 {
		t.Fatalf("key approve: %d", code)
	}
	if code, out := signed(keyA, "GET", "/api/v1/wallet", ""); code != 404 || ecode(out) != "no_wallet" {
		t.Fatalf("plain key wallet: %d %v", code, out)
	}
	// A reseller-owned key reads its reseller's wallet and ledger.
	rk := "resk" + se.sfx
	if _, err := se.st.CreateAPIKey(se.ctx, rk, mustEnc(t, "sec-"+rk), rk, []string{}, []string{"read"}, &res.AdminID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := func() (int64, int, error) {
		b, err := se.st.WalletApply(se.ctx, res.AdminID, 12345, store.LedgerManualCredit, "seed", nil, "test")
		return b, 0, err
	}(); err != nil {
		t.Fatal(err)
	}
	if code, out := signed(rk, "GET", "/api/v1/wallet", ""); code != 200 || out["balance_toman"].(float64) != 12345 {
		t.Fatalf("reseller key wallet: %d %v", code, out)
	}
	if code, out := signed(rk, "GET", "/api/v1/wallet/ledger", ""); code != 200 || len(items(out)) != 1 {
		t.Fatalf("reseller key ledger: %d %v", code, out)
	}
}

// ---- store-level state machine ----------------------------------------------------

func TestStoreTransitionsAreLockedAndIdempotent(t *testing.T) {
	se := newSalesEnv(t)
	o, err := se.st.CreateOrder(se.ctx, store.NewOrder{PublicToken: "st" + se.sfx, Kind: store.KindAccountRenew, Method: store.MethodZarinpal, AmountToman: 1000, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	// Illegal edge.
	if _, _, err := se.st.TransitionOrder(se.ctx, o.ID, store.OrderFulfilled, store.TransitionParams{}); !errors.Is(err, store.ErrIllegalTransition) {
		t.Fatalf("pending -> fulfilled: %v", err)
	}
	// Racing pending -> paid: one change, the rest idempotent no-ops.
	var wg sync.WaitGroup
	var changedN atomic.Int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, changed, err := se.st.TransitionOrder(se.ctx, o.ID, store.OrderPaid, store.TransitionParams{})
			if err != nil {
				t.Errorf("transition: %v", err)
			}
			if changed {
				changedN.Add(1)
			}
		}()
	}
	wg.Wait()
	if changedN.Load() != 1 {
		t.Fatalf("changed %d times", changedN.Load())
	}
	// Claim is exclusive.
	var claims atomic.Int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok, _ := se.st.ClaimFulfilment(se.ctx, o.ID); ok {
				claims.Add(1)
			}
		}()
	}
	wg.Wait()
	if claims.Load() != 1 {
		t.Fatalf("claims = %d", claims.Load())
	}
	// paid -> rejected is illegal.
	if _, _, err := se.st.TransitionOrder(se.ctx, o.ID, store.OrderRejected, store.TransitionParams{}); !errors.Is(err, store.ErrIllegalTransition) {
		t.Fatalf("paid -> rejected: %v", err)
	}
}

func TestSalesTablesInBackupButNotReceipts(t *testing.T) {
	se := newSalesEnv(t)
	dump, err := se.st.DumpTables(se.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []string{"payment_settings", "payment_cards", "template_prices", "reseller_pricing", "reseller_template_prices", "reseller_wallets", "wallet_ledger", "orders"} {
		if _, ok := dump[tbl]; !ok {
			t.Errorf("backup is missing %s", tbl)
		}
	}
	if _, ok := dump["order_receipts"]; ok {
		t.Error("receipt blobs must stay out of backups")
	}
}
