package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"wgpanel-api/internal/events"
	"wgpanel-api/internal/store"
)

// A sales bot holds only an API key, so it must be able to register where the panel
// pushes its accounts' events - and only those.
func TestAPIKeyManagesOwnWebhooks(t *testing.T) {
	e := newEvEnv(t)
	mk := func(name string, perms []string) string {
		t.Helper()
		id := name + e.sfx
		if _, err := e.st.CreateAPIKey(e.ctx, id, mustEnc(t, "sec-"+id), name, []string{"default"}, perms, nil); err != nil {
			t.Fatal(err)
		}
		return id
	}
	signed := func(keyID, method, path, body string) (int, map[string]any) {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
		req.Header.Set("X-API-Key", keyID)
		req.Header.Set("X-Timestamp", ts)
		req.Header.Set("X-Signature", hexHMAC("sec-"+keyID, method+"\n"+strings.SplitN(path, "?", 2)[0]+"\n"+ts+"\n"+body))
		rr := httptest.NewRecorder()
		e.h.ServeHTTP(rr, req)
		var out map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
		return rr.Code, out
	}
	noPerm := mk("evk-plain", []string{"read", "create"})
	bot := mk("evk-bot", []string{"read", "create", "webhooks"})
	other := mk("evk-other", []string{"read", "webhooks"})

	body := `{"url":"https://93.184.216.34/hook","events":["account.expiring_soon","account.quota_exhausted"]}`
	if code, out := signed(noPerm, "POST", "/api/v1/webhooks", body); code != 403 {
		t.Fatalf("key without the webhooks permission: %d %v", code, out)
	}
	code, out := signed(bot, "POST", "/api/v1/webhooks", body)
	if code != 201 || out["namespace"] != bot || !strings.HasPrefix(toJSON(out["secret"]), `"whsec_`) {
		t.Fatalf("create with webhooks permission: %d %v", code, out)
	}
	hookID := out["id"].(string)

	// Scoped like a reseller: no infrastructure events, no private targets.
	if code, out := signed(bot, "POST", "/api/v1/webhooks", `{"url":"https://93.184.216.34/h2","events":["node.down"]}`); code != 400 {
		t.Fatalf("node.down for a key: %d %v", code, out)
	}
	if code, out := signed(bot, "POST", "/api/v1/webhooks", `{"url":"http://127.0.0.1:9/h","events":[]}`); code != 400 || ecode(out) != "webhook_url_blocked" {
		t.Fatalf("loopback target for a key: %d %v", code, out)
	}

	// A staff (global) webhook is invisible to the key; another key can't touch the bot's.
	if code, _ := e.call(e.superT, "POST", "/api/v1/webhooks", `{"url":"https://93.184.216.34/staff","events":[]}`); code != 201 {
		t.Fatalf("staff create: %d", code)
	}
	if code, out := signed(bot, "GET", "/api/v1/webhooks", ""); code != 200 || len(items(out)) != 1 {
		t.Fatalf("key list should hold only its own webhook: %d %v", code, out)
	}
	if code, _ := signed(other, "DELETE", "/api/v1/webhooks/"+hookID, ""); code != 404 {
		t.Fatalf("another key deleting the bot's webhook: %d", code)
	}
	if code, out := signed(bot, "POST", "/api/v1/webhooks/"+hookID+"/rotate-secret", ""); code != 200 || !strings.HasPrefix(toJSON(out["secret"]), `"whsec_`) {
		t.Fatalf("rotate: %d %v", code, out)
	}
	if code, _ := signed(bot, "DELETE", "/api/v1/webhooks/"+hookID, ""); code != 204 && code != 200 {
		t.Fatalf("delete own: %d", code)
	}
}

// Account events carry the caller's external_ref so a bot can match them to its order.
func TestAccountEventsCarryExternalRef(t *testing.T) {
	ref := "tg:42:7"
	s := &Server{Events: events.NewBus(8, nil)}
	s.emitAccountEvent(events.AccountSuspended, store.Account{ID: "a1", Label: "l", Status: "suspended", ExternalRef: &ref})
	s.emitAccountEvent(events.AccountEnabled, store.Account{ID: "a2", Label: "m", Status: "active"})
	evs := drain(s.Events)
	if len(evs) != 2 {
		t.Fatalf("got %d events", len(evs))
	}
	if evs[0].Data["external_ref"] != ref {
		t.Fatalf("external_ref missing: %v", evs[0].Data)
	}
	if _, ok := evs[1].Data["external_ref"]; ok {
		t.Fatalf("no external_ref expected when the account has none: %v", evs[1].Data)
	}
}
