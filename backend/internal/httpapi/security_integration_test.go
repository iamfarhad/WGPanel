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
	"testing"
	"time"

	"wgpanel-api/internal/authcrypto"
	"wgpanel-api/internal/redisclient"
	"wgpanel-api/internal/store"
)

// TestAdminSecurityEndToEnd drives lockout, TOTP, recovery codes, the IP allowlist,
// the break-glass clear, admin MFA reset and passkey option generation through the
// real router. Needs WGPANEL_TEST_POSTGRES_DSN and WGPANEL_TEST_REDIS_URL.
func TestAdminSecurityEndToEnd(t *testing.T) {
	dsn := os.Getenv("WGPANEL_TEST_POSTGRES_DSN")
	redisURL := os.Getenv("WGPANEL_TEST_REDIS_URL")
	if dsn == "" || redisURL == "" {
		t.Skip("set WGPANEL_TEST_POSTGRES_DSN and WGPANEL_TEST_REDIS_URL to run integration tests")
	}
	ctx := context.Background()
	st, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	rdb, err := redisclient.Open(ctx, redisURL)
	if err != nil {
		t.Fatal(err)
	}
	defer rdb.Close()

	s := &Server{
		Store: st, Redis: rdb, JWTSecret: gateTestSecret, AccountKeyEncryptionKey: itHexKey,
		InternalAPIToken: "internal-tok", BootPanelDomain: "panel.example.test",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	h := s.Routes()
	sfx := strconv.FormatInt(time.Now().UnixNano(), 10)

	hash, err := authcrypto.HashPassword("correct-horse-1")
	if err != nil {
		t.Fatal(err)
	}
	boss, err := st.CreateAdmin(ctx, "sec-boss-"+sfx, hash, "super_admin")
	if err != nil {
		t.Fatal(err)
	}
	bossTok, _ := authcrypto.IssueAccessToken(gateTestSecret, boss.ID, boss.Username, "super_admin")
	user, err := st.CreateAdmin(ctx, "sec-user-"+sfx, hash, "operator")
	if err != nil {
		t.Fatal(err)
	}
	userTok, _ := authcrypto.IssueAccessToken(gateTestSecret, user.ID, user.Username, "operator")
	// Leave the global allowlist clean for other tests.
	t.Cleanup(func() { _ = st.ClearAdminIPAllowlist(context.Background()) })
	_ = st.ClearAdminIPAllowlist(ctx)

	call := func(method, path, token, ip string, body any) (int, map[string]any, http.Header) {
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, path, rd)
		req.RemoteAddr = ip + ":4000"
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		var out map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
		return rr.Code, out, rr.Header()
	}
	errCode := func(m map[string]any) string {
		e, _ := m["error"].(map[string]any)
		c, _ := e["code"].(string)
		return c
	}
	login := func(username, password, ip string) (int, map[string]any, http.Header) {
		return call("POST", "/api/v1/auth/login", "", ip, map[string]string{"username": username, "password": password})
	}

	// --- plain login still works without a second factor ---
	if code, out, _ := login(user.Username, "correct-horse-1", "198.51.100.10"); code != 200 || out["access_token"] == nil {
		t.Fatalf("plain login: %d %v", code, out)
	}

	// --- TOTP enrolment ---
	code, out, _ := call("POST", "/api/v1/me/security/totp/enroll", userTok, "198.51.100.10", nil)
	if code != 200 {
		t.Fatalf("enroll: %d %v", code, out)
	}
	secret := out["secret"].(string)
	if !strings.HasPrefix(out["otpauth_uri"].(string), "otpauth://totp/") {
		t.Fatalf("uri: %v", out["otpauth_uri"])
	}
	// The secret is encrypted at rest.
	if tt, _ := st.GetAdminTOTP(ctx, user.ID); tt.SecretEnc == secret || tt.SecretEnc == "" {
		t.Fatal("secret must be stored encrypted")
	}
	// Wrong code does not confirm; before confirmation login is unaffected.
	if code, out, _ := call("POST", "/api/v1/me/security/totp/confirm", userTok, "198.51.100.10", map[string]string{"code": "000000"}); code != 401 || errCode(out) != "invalid_mfa_code" {
		t.Fatalf("bad confirm: %d %v", code, out)
	}
	now := time.Now()
	step0 := now.Unix() / totpPeriod
	c0, _ := totpCodeAt(secret, step0)
	code, out, _ = call("POST", "/api/v1/me/security/totp/confirm", userTok, "198.51.100.10", map[string]string{"code": c0})
	if code != 200 {
		t.Fatalf("confirm: %d %v", code, out)
	}
	recov, _ := out["recovery_codes"].([]any)
	if len(recov) != 10 {
		t.Fatalf("want 10 recovery codes, got %d", len(recov))
	}
	if n, _ := st.CountRecoveryCodes(ctx, user.ID); n != 10 {
		t.Fatalf("stored recovery codes = %d", n)
	}
	if code, _, _ := call("POST", "/api/v1/me/security/totp/enroll", userTok, "198.51.100.10", nil); code != 409 {
		t.Fatalf("re-enrol while enabled: %d, want 409", code)
	}

	// --- login now needs the second factor ---
	code, out, _ = login(user.Username, "correct-horse-1", "198.51.100.11")
	if code != 200 || out["mfa_required"] != true || out["access_token"] != nil {
		t.Fatalf("login with 2FA: %d %v", code, out)
	}
	mfaTok := out["mfa_token"].(string)
	methods, _ := out["methods"].([]any)
	if len(methods) != 2 || methods[0] != "totp" || methods[1] != "recovery" {
		t.Fatalf("methods = %v", methods)
	}
	if code, out, _ := call("POST", "/api/v1/auth/mfa/totp", "", "198.51.100.11", map[string]string{"mfa_token": mfaTok, "code": "111111"}); code != 401 || errCode(out) != "invalid_mfa_code" {
		t.Fatalf("wrong totp: %d %v", code, out)
	}
	// The code used to confirm enrolment is replay-protected; the next step's code is fine.
	if code, _, _ := call("POST", "/api/v1/auth/mfa/totp", "", "198.51.100.11", map[string]string{"mfa_token": mfaTok, "code": c0}); code != 401 {
		t.Fatalf("replayed totp: %d, want 401", code)
	}
	c1, _ := totpCodeAt(secret, step0+1)
	code, out, _ = call("POST", "/api/v1/auth/mfa/totp", "", "198.51.100.11", map[string]string{"mfa_token": mfaTok, "code": c1})
	if code != 200 || out["access_token"] == nil || out["refresh_token"] == nil {
		t.Fatalf("totp exchange: %d %v", code, out)
	}
	// mfa_token is single use.
	if code, out, _ := call("POST", "/api/v1/auth/mfa/totp", "", "198.51.100.11", map[string]string{"mfa_token": mfaTok, "code": c1}); code != 401 || errCode(out) != "mfa_token_invalid" {
		t.Fatalf("token reuse: %d %v", code, out)
	}

	// --- recovery code: works once ---
	_, out, _ = login(user.Username, "correct-horse-1", "198.51.100.12")
	mfaTok = out["mfa_token"].(string)
	rc := recov[0].(string)
	if code, out, _ := call("POST", "/api/v1/auth/mfa/recovery", "", "198.51.100.12", map[string]string{"mfa_token": mfaTok, "recovery_code": rc}); code != 200 || out["access_token"] == nil {
		t.Fatalf("recovery login: %d %v", code, out)
	}
	_, out, _ = login(user.Username, "correct-horse-1", "198.51.100.12")
	mfaTok = out["mfa_token"].(string)
	if code, _, _ := call("POST", "/api/v1/auth/mfa/recovery", "", "198.51.100.12", map[string]string{"mfa_token": mfaTok, "recovery_code": rc}); code != 401 {
		t.Fatalf("reused recovery code: %d, want 401", code)
	}

	// --- disable needs password + code ---
	c2, _ := totpCodeAt(secret, step0+1) // already used above: must be refused as a replay
	if code, _, _ := call("POST", "/api/v1/me/security/totp/disable", userTok, "198.51.100.10", map[string]string{"password": "wrong", "code": c2}); code != 401 {
		t.Fatalf("disable with wrong password: %d", code)
	}
	if n, _ := st.CountRecoveryCodes(ctx, user.ID); n == 0 {
		t.Fatal("totp must still be enabled after a refused disable")
	}
	if code, _, _ := call("POST", "/api/v1/me/security/totp/disable", userTok, "198.51.100.10", map[string]string{"password": "correct-horse-1", "code": recov[1].(string)}); code != 204 {
		t.Fatalf("disable with recovery code: %d", code)
	}
	if code, out, _ := login(user.Username, "correct-horse-1", "198.51.100.13"); code != 200 || out["access_token"] == nil {
		t.Fatalf("login after disable: %d %v", code, out)
	}

	// --- super admin resets another admin's factors ---
	call("POST", "/api/v1/me/security/totp/enroll", userTok, "198.51.100.10", nil)
	if code, _, _ := call("DELETE", "/api/v1/admins/"+user.ID+"/mfa", userTok, "198.51.100.10", nil); code != 403 {
		t.Fatalf("non super admin reset: %d, want 403", code)
	}
	if code, _, _ := call("DELETE", "/api/v1/admins/"+user.ID+"/mfa", bossTok, "198.51.100.10", nil); code != 204 {
		t.Fatalf("reset: %d", code)
	}
	if _, err := st.GetAdminTOTP(ctx, user.ID); err == nil {
		t.Fatal("totp row should be gone after reset")
	}
	if code, out, _ := call("GET", "/api/v1/admins", bossTok, "198.51.100.10", nil); code != 200 {
		t.Fatalf("list admins: %d %v", code, out)
	}

	// --- passkey options (browser ceremony itself is not simulated) ---
	req := httptest.NewRequest("POST", "/api/v1/auth/passkey/begin", nil)
	req.RemoteAddr = "198.51.100.14:1"
	req.Header.Set("Origin", "https://panel.example.test")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"rpId":"panel.example.test"`) || !strings.Contains(rr.Body.String(), "session_id") {
		t.Fatalf("passkey begin: %d %s", rr.Code, rr.Body.String())
	}
	req = httptest.NewRequest("POST", "/api/v1/auth/passkey/begin", nil)
	req.RemoteAddr = "198.51.100.14:1"
	req.Header.Set("Origin", "https://evil.example")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 400 || !strings.Contains(rr.Body.String(), "passkeys_unavailable") {
		t.Fatalf("foreign origin must be refused: %d %s", rr.Code, rr.Body.String())
	}
	req = httptest.NewRequest("POST", "/api/v1/auth/passkey/begin", nil)
	req.RemoteAddr = "198.51.100.14:1"
	req.Header.Set("Origin", "http://localhost:5173")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"rpId":"localhost"`) {
		t.Fatalf("localhost dev origin: %d %s", rr.Code, rr.Body.String())
	}
	// Finish with an unknown session is refused.
	if code, out, _ := call("POST", "/api/v1/auth/passkey/finish?session=nope", "", "198.51.100.14", nil); code != 400 || errCode(out) != "passkey_session_expired" {
		t.Fatalf("finish unknown session: %d %v", code, out)
	}

	// --- IP allowlist ---
	if code, out, _ := call("PATCH", "/api/v1/security/settings", bossTok, "198.51.100.20", map[string]any{"admin_ip_allowlist": []string{"203.0.113.0/24"}}); code != 400 || errCode(out) != "allowlist_excludes_you" {
		t.Fatalf("self-excluding list: %d %v", code, out)
	}
	if code, out, _ := call("PATCH", "/api/v1/security/settings", bossTok, "198.51.100.20", map[string]any{"admin_ip_allowlist": []string{"nonsense"}}); code != 400 {
		t.Fatalf("invalid entry: %d %v", code, out)
	}
	code, out, _ = call("PATCH", "/api/v1/security/settings", bossTok, "198.51.100.20", map[string]any{"admin_ip_allowlist": []string{"198.51.100.20", "203.0.113.0/24"}})
	if code != 200 {
		t.Fatalf("set allowlist: %d %v", code, out)
	}
	if code, out, _ := call("GET", "/api/v1/me", bossTok, "198.51.100.99", nil); code != 403 || errCode(out) != "ip_not_allowed" {
		t.Fatalf("admin route from a foreign IP: %d %v", code, out)
	}
	if code, out, _ := login(user.Username, "correct-horse-1", "198.51.100.99"); code != 403 || errCode(out) != "ip_not_allowed" {
		t.Fatalf("login from a foreign IP: %d %v", code, out)
	}
	if code, _, _ := call("GET", "/api/v1/me", bossTok, "203.0.113.50", nil); code != 200 {
		t.Fatalf("admin route from an allowed CIDR: %d", code)
	}
	// Subscription and API-key routes are not subject to the allowlist.
	if code, out, _ := call("GET", "/api/v1/sub/doesnotexist", "", "198.51.100.99", nil); errCode(out) == "ip_not_allowed" {
		t.Fatalf("subscription route must ignore the allowlist (%d)", code)
	}
	if code, out, _ := call("GET", "/api/v1/accounts", "", "198.51.100.99", nil); errCode(out) == "ip_not_allowed" {
		t.Fatalf("API-key route must ignore the allowlist (%d)", code)
	}
	// Break-glass.
	if code, _, _ := call("POST", "/internal/security/clear-allowlist", "", "127.0.0.1", nil); code != 401 {
		t.Fatalf("clear without internal token: %d", code)
	}
	req = httptest.NewRequest("POST", "/internal/security/clear-allowlist", nil)
	req.Header.Set("X-Internal-Token", "internal-tok")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 204 {
		t.Fatalf("clear: %d", rr.Code)
	}
	if code, _, _ := call("GET", "/api/v1/me", bossTok, "198.51.100.99", nil); code != 200 {
		t.Fatalf("after clear: %d", code)
	}

	// --- progressive lockout ---
	lockUser := "sec-lock-" + sfx
	ip := "198.51.100.77"
	for i := 1; i <= 4; i++ {
		if code, out, _ := login(lockUser, "bad", ip); code != 401 || errCode(out) != "invalid_credentials" {
			t.Fatalf("attempt %d: %d %v", i, code, out)
		}
	}
	code, out, hdr := login(lockUser, "bad", ip) // 5th failure trips the lock
	if code != 429 || errCode(out) != "too_many_attempts" || hdr.Get("Retry-After") == "" {
		t.Fatalf("5th failure: %d %v retry-after=%q", code, out, hdr.Get("Retry-After"))
	}
	if secs, _ := strconv.Atoi(hdr.Get("Retry-After")); secs < 14*60 || secs > 15*60 {
		t.Fatalf("Retry-After = %d, want about 900", secs)
	}
	// Still locked even for a correct password on a real account from another IP using the locked name.
	if code, _, _ := login(lockUser, "bad", "198.51.100.78"); code != 429 {
		t.Fatalf("locked username from another IP: %d", code)
	}
	// A real account locks the same way, and the right password is refused while locked.
	for i := 0; i < 5; i++ {
		login(user.Username, "bad", "198.51.100.79")
	}
	if code, _, _ := login(user.Username, "correct-horse-1", "198.51.100.80"); code != 429 {
		t.Fatalf("valid password while locked: %d, want 429", code)
	}
	rdb.Del(ctx, loginSubject{"user", strings.ToLower(user.Username)}.lockKey())
	// The lockout is in the audit log.
	entries, err := st.ListAuditLog(ctx, 200)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if e.Action == "admin.login_locked" && e.Actor == lockUser {
			found = true
		}
	}
	if !found {
		t.Error("expected an admin.login_locked audit entry")
	}
}
