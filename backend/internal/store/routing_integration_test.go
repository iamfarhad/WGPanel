package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"wgpanel-api/internal/wgkeys"
)

const routingTestKey = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"

// TestPSKBackfillAndRoutingStore covers the SQL of migration 0029: the Go-side PSK
// backfill (idempotent, never overwrites), the latch that decides which peers require
// their PSK, the routing columns on accounts, and the node IPv6 state transitions.
// Skipped without WGPANEL_TEST_POSTGRES_DSN.
func TestPSKBackfillAndRoutingStore(t *testing.T) {
	dsn := os.Getenv("WGPANEL_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set WGPANEL_TEST_POSTGRES_DSN to run store integration tests")
	}
	ctx := context.Background()
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	sfx := fmt.Sprint(time.Now().UnixNano())
	ns := "rtstore-" + sfx
	key := "pk-rtstore-" + sfx
	node, err := s.CreateNode(ctx, "rtstore-"+sfx, "g-"+sfx, "eu", "rtstore.test:51820", "10.78.0.0/24", 50, &key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = s.pool.Exec(bg, `DELETE FROM accounts WHERE owner_key_namespace = $1`, ns)
		_, _ = s.pool.Exec(bg, `DELETE FROM nodes WHERE id = $1`, node.ID)
	})
	if _, err := s.pool.Exec(ctx, `UPDATE nodes SET status = 'online' WHERE id = $1`, node.ID); err != nil {
		t.Fatal(err)
	}

	mk := func(label string, mode *string, cidrs []string, psk *string) Account {
		t.Helper()
		kp, _ := wgkeys.GenerateKeyPair()
		enc, _ := wgkeys.Encrypt(routingTestKey, kp.PrivateKey)
		a, err := s.CreateAccount(ctx, CreateAccountParams{
			Label: label, NodeIDOrAuto: node.ID, PublicKey: kp.PublicKey, PrivateKeyEncrypted: enc,
			SubscriptionToken: label + sfx, OwnerKeyNamespace: &ns,
			RoutingMode: mode, RoutingExcludedCIDRs: cidrs, PresharedKeyEncrypted: psk,
		})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}

	// A pre-0029 account: no PSK, no routing.
	legacy := mk("legacy", nil, nil, nil)
	if enc, err := s.GetAccountPSK(ctx, legacy.ID); err != nil || enc != nil {
		t.Fatalf("legacy account PSK = %v, %v; want NULL", enc, err)
	}
	r, err := s.GetAccountRouting(ctx, legacy.ID)
	if err != nil || r.Mode != nil || len(r.ExcludedCIDRs) != 0 || r.Precision != nil {
		t.Fatalf("legacy routing = %+v, %v; want defaults", r, err)
	}

	// An account that already has a PSK must keep it through a backfill.
	keep := "keep-this-ciphertext"
	custom := "custom"
	withPSK := mk("withpsk", &custom, []string{"203.0.113.0/24"}, &keep)

	n, err := s.BackfillPresharedKeys(ctx, routingTestKey)
	if err != nil || n < 1 {
		t.Fatalf("backfill = %d, %v; want at least the legacy account", n, err)
	}
	enc, _ := s.GetAccountPSK(ctx, legacy.ID)
	if enc == nil {
		t.Fatal("legacy account still has no PSK after backfill")
	}
	plain, err := wgkeys.Decrypt(routingTestKey, *enc)
	if err != nil || len(plain) != 44 {
		t.Fatalf("backfilled PSK decrypts to %q, %v; want a 44-char base64 key", plain, err)
	}
	if kept, _ := s.GetAccountPSK(ctx, withPSK.ID); kept == nil || *kept != keep {
		t.Fatal("backfill overwrote an existing PSK")
	}
	if again, err := s.BackfillPresharedKeys(ctx, routingTestKey); err != nil || again != 0 {
		t.Fatalf("second backfill = %d, %v; want 0 (idempotent)", again, err)
	}
	// A late writer cannot clobber a key a client may already hold.
	if ok, err := s.SetAccountPSKIfMissing(ctx, legacy.ID, "other"); err != nil || ok {
		t.Fatalf("SetAccountPSKIfMissing over an existing key = %v, %v", ok, err)
	}

	// Routing columns set at creation and read back (single and batched).
	r, err = s.GetAccountRouting(ctx, withPSK.ID)
	if err != nil || r.Mode == nil || *r.Mode != "custom" || len(r.ExcludedCIDRs) != 1 {
		t.Fatalf("routing = %+v, %v", r, err)
	}
	m, err := s.ListAccountRouting(ctx, []string{legacy.ID, withPSK.ID})
	if err != nil || len(m) != 2 || m[withPSK.ID].Mode == nil {
		t.Fatalf("ListAccountRouting = %+v, %v", m, err)
	}

	// UpdateAccountRouting: namespace-scoped, clear flags, stale list cleanup.
	bypass := "bypass_iran"
	empty := []string{}
	if err := s.UpdateAccountRouting(ctx, withPSK.ID, &ns, RoutingUpdate{Mode: &bypass, ExcludedCIDRs: &empty}); err != nil {
		t.Fatal(err)
	}
	r, _ = s.GetAccountRouting(ctx, withPSK.ID)
	if *r.Mode != "bypass_iran" || len(r.ExcludedCIDRs) != 0 {
		t.Fatalf("after update: %+v", r)
	}
	other := "someone-else"
	if err := s.UpdateAccountRouting(ctx, withPSK.ID, &other, RoutingUpdate{Mode: &bypass}); err != ErrAccountNotFound {
		t.Fatalf("foreign namespace update = %v, want ErrAccountNotFound", err)
	}
	if err := s.UpdateAccountRouting(ctx, withPSK.ID, nil, RoutingUpdate{ClearMode: true}); err != nil {
		t.Fatal(err)
	}
	if r, _ = s.GetAccountRouting(ctx, withPSK.ID); r.Mode != nil {
		t.Fatalf("mode not cleared: %+v", r)
	}

	// The PSK latch: nothing issued -> nothing listed; issuing is idempotent.
	issued, err := s.ListIssuedPSKsForNode(ctx, node.ID)
	if err != nil || len(issued) != 0 {
		t.Fatalf("issued before any config was served = %v, %v", issued, err)
	}
	for i := 0; i < 2; i++ {
		if err := s.MarkPeerPSKIssued(ctx, legacy.ID, node.ID); err != nil {
			t.Fatal(err)
		}
	}
	issued, _ = s.ListIssuedPSKsForNode(ctx, node.ID)
	if len(issued) != 1 || issued[legacy.PublicKey] != *enc {
		t.Fatalf("issued = %v", issued)
	}
	// A suspended account's peer is not desired, so its PSK is not listed either.
	if _, err := s.SuspendAccount(ctx, legacy.ID, &ns, "manual"); err != nil {
		t.Fatal(err)
	}
	if issued, _ = s.ListIssuedPSKsForNode(ctx, node.ID); len(issued) != 0 {
		t.Fatalf("suspended account's PSK listed: %v", issued)
	}

	// Node IPv6 state machine.
	nr, err := s.GetNodeRouting(ctx, node.ID)
	if err != nil || nr.IPv6Enabled || nr.PSKSupported || nr.IPv6Active() {
		t.Fatalf("fresh node routing = %+v, %v", nr, err)
	}
	prefix := "fd12:3456:789a:1::/64"
	nr, err = s.SetNodeIPv6(ctx, node.ID, true, &prefix)
	if err != nil || !nr.IPv6Enabled || nr.IPv6Active() {
		t.Fatalf("enabled node must not be active before the agent reports: %+v, %v", nr, err)
	}
	if err := s.RecordNodeRoutingState(ctx, node.ID, true, true, true); err != nil {
		t.Fatal(err)
	}
	if nr, _ = s.GetNodeRouting(ctx, node.ID); !nr.IPv6Active() || !nr.PSKSupported {
		t.Fatalf("after report: %+v", nr)
	}
	// Disabling clears ready, so re-enabling cannot render v6 on a stale report.
	if _, err := s.SetNodeIPv6(ctx, node.ID, false, nil); err != nil {
		t.Fatal(err)
	}
	if nr, _ = s.GetNodeRouting(ctx, node.ID); nr.IPv6Ready || nr.IPv6Active() || nr.IPv6ULAPrefix == nil {
		t.Fatalf("after disable: %+v (prefix must be kept, ready cleared)", nr)
	}
	nr, _ = s.SetNodeIPv6(ctx, node.ID, true, nil)
	if nr.IPv6Active() {
		t.Fatal("re-enable must wait for a fresh agent report")
	}
	if _, err := s.GetNodeRouting(ctx, "00000000-0000-0000-0000-000000000000"); err != ErrNodeNotFound {
		t.Fatalf("missing node = %v", err)
	}
}
