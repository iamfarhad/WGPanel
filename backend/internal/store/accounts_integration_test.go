package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync/atomic"
	"testing"
	"time"
)

// TestAccountAPIAdditionsIntegration covers the SQL behind the bot-facing additions:
// find by external_ref (deleted rows included), stable offset paging, reset-usage,
// the clear_* flags of UpdateAccount and the last_handshake_at aggregate. Skipped
// without WGPANEL_TEST_POSTGRES_DSN, like the other store integration tests.
//
// It removes everything it created (accounts, nodes, the reseller logins) on exit, so
// it neither depends on nor disturbs the other integration tests that share the
// database - several of them assume a particular set of online nodes.
func TestAccountAPIAdditionsIntegration(t *testing.T) {
	dsn := os.Getenv("WGPANEL_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set WGPANEL_TEST_POSTGRES_DSN to run store integration tests")
	}
	ctx := context.Background()
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close) // registered first, so it runs after the row cleanup below (LIFO)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	sfx := fmt.Sprint(time.Now().UnixNano())
	group := "aapi-" + sfx
	var namespaces, adminIDs, nodeIDs []string
	t.Cleanup(func() {
		bg := context.Background()
		for _, ns := range namespaces {
			if _, err := s.pool.Exec(bg, `DELETE FROM accounts WHERE owner_key_namespace = $1`, ns); err != nil {
				t.Logf("cleanup accounts %s: %v", ns, err)
			}
		}
		for _, id := range adminIDs {
			if _, err := s.pool.Exec(bg, `DELETE FROM admins WHERE id = $1`, id); err != nil {
				t.Logf("cleanup admin %s: %v", id, err)
			}
		}
		for _, id := range nodeIDs {
			if _, err := s.pool.Exec(bg, `DELETE FROM nodes WHERE id = $1`, id); err != nil {
				t.Logf("cleanup node %s: %v", id, err)
			}
		}
	})

	mkNode := func(name, subnet string) Node {
		t.Helper()
		key := "pk-node-" + name + sfx
		n, err := s.CreateNode(ctx, name+"-"+sfx, group, "eu", name+".test:51820", subnet, 500, &key)
		if err != nil {
			t.Fatal(err)
		}
		nodeIDs = append(nodeIDs, n.ID)
		// Fresh nodes are 'pending'; account creation needs registered/online.
		if _, err := s.pool.Exec(ctx, `UPDATE nodes SET status = 'online' WHERE id = $1`, n.ID); err != nil {
			t.Fatal(err)
		}
		return n
	}
	node1 := mkNode("n1", "10.66.0.0/16")
	node2 := mkNode("n2", "10.67.0.0/16")

	var seq atomic.Int64
	// newNS returns a fresh API-key-style namespace, so each subtest lists only its own
	// accounts however many other accounts the database holds.
	newNS := func() *string {
		ns := fmt.Sprintf("aapi-ns-%s-%d", sfx, seq.Add(1))
		namespaces = append(namespaces, ns)
		return &ns
	}
	mk := func(t *testing.T, ns *string, ref *string, quota *int64, expiry *time.Time) Account {
		t.Helper()
		n := seq.Add(1)
		a, err := s.CreateAccount(ctx, CreateAccountParams{
			ExternalRef: ref, Label: fmt.Sprintf("acct-%d", n), PublicKey: fmt.Sprintf("pk-%s-%d", sfx, n),
			PrivateKeyEncrypted: "x", DataQuotaBytes: quota, ExpiryAt: expiry,
			SubscriptionToken: fmt.Sprintf("tok-%s-%d", sfx, n), OwnerKeyNamespace: ns, AllowedNodeGroups: []string{group},
		})
		if err != nil {
			t.Fatalf("create account: %v", err)
		}
		return a
	}
	strp := func(v string) *string { return &v }
	// setState forces an account into an arbitrary status/suspend_reason/expiry/usage,
	// including combinations only reachable through enforcement code the panel lacks.
	setState := func(t *testing.T, id, status string, reason *string, expiry *time.Time, used int64) {
		t.Helper()
		if _, err := s.pool.Exec(ctx,
			`UPDATE accounts SET status = $2, suspend_reason = $3, expiry_at = $4, data_used_bytes = $5 WHERE id = $1`,
			id, status, reason, expiry, used); err != nil {
			t.Fatal(err)
		}
	}
	future := time.Now().Add(48 * time.Hour)
	past := time.Now().Add(-48 * time.Hour)
	desired := func(t *testing.T, nodeID, publicKey string) bool {
		t.Helper()
		peers, err := s.ListDesiredPeersForNode(ctx, nodeID)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range peers {
			if p.PublicKey == publicKey {
				return true
			}
		}
		return false
	}

	t.Run("find by external_ref includes deleted and respects the namespace", func(t *testing.T) {
		ns, otherNS := newNS(), newNS()
		ref := "ref-" + sfx
		a := mk(t, ns, &ref, nil, nil)

		got, err := s.FindAccountsByExternalRef(ctx, ref, ns)
		if err != nil || len(got) != 1 || got[0].ID != a.ID {
			t.Fatalf("find: %v %+v", err, got)
		}
		if got, err := s.FindAccountsByExternalRef(ctx, ref, nil); err != nil || len(got) != 1 {
			t.Fatalf("admin (nil namespace) find: %v %+v", err, got)
		}
		if got, err := s.FindAccountsByExternalRef(ctx, ref, otherNS); err != nil || len(got) != 0 {
			t.Fatalf("foreign namespace must see nothing: %v %+v", err, got)
		}
		if got, err := s.FindAccountsByExternalRef(ctx, ref+"x", ns); err != nil || len(got) != 0 {
			t.Fatalf("a different ref must not match (exact, not prefix): %v %+v", err, got)
		}

		if _, err := s.SoftDeleteAccount(ctx, a.ID, ns); err != nil {
			t.Fatal(err)
		}
		got, err = s.FindAccountsByExternalRef(ctx, ref, ns)
		if err != nil || len(got) != 1 || got[0].Status != "deleted" {
			t.Fatalf("a deleted account must still be found, status deleted: %v %+v", err, got)
		}
		listed, err := s.ListAccounts(ctx, 50, 0, ns, "")
		if err != nil || len(listed) != 0 {
			t.Fatalf("the plain list must still exclude deleted accounts: %v %+v", err, listed)
		}
		// This is the reason the lookup exists: the ref stays taken after a soft delete.
		_, err = s.CreateAccount(ctx, CreateAccountParams{
			ExternalRef: &ref, Label: "again", PublicKey: "pk-again-" + sfx, PrivateKeyEncrypted: "x",
			SubscriptionToken: "tok-again-" + sfx, OwnerKeyNamespace: ns, AllowedNodeGroups: []string{group},
		})
		if !errors.Is(err, ErrExternalRefTaken) {
			t.Fatalf("re-using a deleted account's ref: got %v, want ErrExternalRefTaken", err)
		}
	})

	t.Run("offset paging is stable even when created_at ties", func(t *testing.T) {
		ns := newNS()
		const total = 5
		var want []string
		for i := 0; i < total; i++ {
			want = append(want, mk(t, ns, nil, nil, nil).ID)
		}
		// One shared created_at makes id the only thing separating the rows.
		if _, err := s.pool.Exec(ctx, `UPDATE accounts SET created_at = $2 WHERE owner_key_namespace = $1`, *ns, time.Now().Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
		sort.Strings(want)

		var paged []string
		for offset := 0; offset < total+2; offset += 2 {
			page, err := s.ListAccounts(ctx, 2, offset, ns, "")
			if err != nil {
				t.Fatal(err)
			}
			for _, a := range page {
				paged = append(paged, a.ID)
			}
		}
		if fmt.Sprint(paged) != fmt.Sprint(want) {
			t.Fatalf("paged ids %v, want every id exactly once in (created_at, id) order %v", paged, want)
		}
		if page, err := s.ListAccounts(ctx, 10, total, ns, ""); err != nil || len(page) != 0 {
			t.Fatalf("offset past the end: %v %+v", err, page)
		}

		// A deleted account vanishes from the window rather than leaving a hole.
		if _, err := s.SoftDeleteAccount(ctx, want[0], ns); err != nil {
			t.Fatal(err)
		}
		page, err := s.ListAccounts(ctx, 2, 0, ns, "")
		if err != nil || len(page) != 2 || page[0].ID != want[1] {
			t.Fatalf("after deleting the first account: %v %+v", err, page)
		}
	})

	t.Run("reset usage lifts only a quota_exceeded suspension that has not expired", func(t *testing.T) {
		quota := int64(1000)
		cases := []struct {
			name       string
			status     string
			reason     *string
			expiry     *time.Time
			wantStatus string
			wantReason *string
		}{
			{"quota_exceeded, no expiry", "suspended", strp("quota_exceeded"), nil, "active", nil},
			{"quota_exceeded, future expiry", "suspended", strp("quota_exceeded"), &future, "active", nil},
			{"quota_exceeded but already expired", "suspended", strp("quota_exceeded"), &past, "suspended", strp("quota_exceeded")},
			{"manual", "suspended", strp("manual"), nil, "suspended", strp("manual")},
			{"abuse_flag", "suspended", strp("abuse_flag"), nil, "suspended", strp("abuse_flag")},
			{"expired", "suspended", strp("expired"), &past, "suspended", strp("expired")},
			{"device_limit", "suspended", strp("device_limit"), nil, "suspended", strp("device_limit")},
			{"reseller_suspended", "suspended", strp(SuspendReasonReseller), nil, "suspended", strp(SuspendReasonReseller)},
			{"already active", "active", nil, nil, "active", nil},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				ns := newNS()
				a := mk(t, ns, nil, &quota, nil)
				setState(t, a.ID, c.status, c.reason, c.expiry, 2500)

				got, err := s.ResetAccountUsage(ctx, a.ID, ns)
				if err != nil {
					t.Fatal(err)
				}
				if got.DataUsedBytes != 0 {
					t.Fatalf("data_used_bytes = %d, want 0", got.DataUsedBytes)
				}
				if got.DataQuotaBytes == nil || *got.DataQuotaBytes != quota {
					t.Fatalf("data_quota_bytes must not change: %v", got.DataQuotaBytes)
				}
				if got.Status != c.wantStatus || fmt.Sprint(deref(got.SuspendReason)) != fmt.Sprint(deref(c.wantReason)) {
					t.Fatalf("status/reason = %s/%v, want %s/%v", got.Status, deref(got.SuspendReason), c.wantStatus, deref(c.wantReason))
				}
				// The lift reaches the node the same way EnableAccount's does: the account is
				// in the desired-peer feed the agent reconciles from on its next heartbeat.
				if in := desired(t, node1.ID, a.PublicKey); in != (c.wantStatus == "active") {
					t.Fatalf("in node's desired peers = %v, want %v", in, c.wantStatus == "active")
				}
				// What the response says is what was committed.
				stored, err := s.GetAccount(ctx, a.ID, ns)
				if err != nil || stored.DataUsedBytes != 0 || stored.Status != c.wantStatus {
					t.Fatalf("stored account: %v %+v", err, stored)
				}
			})
		}
	})

	t.Run("reset usage 404s for deleted and foreign accounts", func(t *testing.T) {
		ns, other := newNS(), newNS()
		a := mk(t, ns, nil, nil, nil)
		if _, err := s.ResetAccountUsage(ctx, a.ID, other); !errors.Is(err, ErrAccountNotFound) {
			t.Fatalf("foreign namespace: %v", err)
		}
		if _, err := s.ResetAccountUsage(ctx, "00000000-0000-0000-0000-000000000000", ns); !errors.Is(err, ErrAccountNotFound) {
			t.Fatalf("unknown id: %v", err)
		}
		if _, err := s.SoftDeleteAccount(ctx, a.ID, ns); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ResetAccountUsage(ctx, a.ID, ns); !errors.Is(err, ErrAccountNotFound) {
			t.Fatalf("deleted account: %v", err)
		}
		if _, err := s.ResetAccountUsage(ctx, a.ID, nil); !errors.Is(err, ErrAccountNotFound) {
			t.Fatalf("deleted account as admin: %v", err)
		}
	})

	t.Run("a sample after a reset adds only the new delta", func(t *testing.T) {
		ns := newNS()
		quota := int64(1_000_000)
		a := mk(t, ns, nil, &quota, nil)
		ingest := func(rx, tx int64) {
			t.Helper()
			now := time.Now()
			if err := s.IngestHeartbeatTelemetry(ctx, node1.ID, []PeerTrafficReport{
				{PublicKey: a.PublicKey, ReceiveBytes: rx, TransmitBytes: tx, LastHandshake: &now},
			}, nil); err != nil {
				t.Fatalf("ingest: %v", err)
			}
		}
		used := func() int64 {
			t.Helper()
			got, err := s.GetAccount(ctx, a.ID, ns)
			if err != nil {
				t.Fatal(err)
			}
			return got.DataUsedBytes
		}
		baselines := func() (int64, int64) {
			t.Helper()
			var rx, tx int64
			if err := s.pool.QueryRow(ctx,
				`SELECT last_receive_bytes, last_transmit_bytes FROM account_peers WHERE account_id = $1 AND node_id = $2`,
				a.ID, node1.ID).Scan(&rx, &tx); err != nil {
				t.Fatal(err)
			}
			return rx, tx
		}

		ingest(1000, 1000) // first observation only seeds the baselines
		if got := used(); got != 0 {
			t.Fatalf("seed must not count: %d", got)
		}
		ingest(1500, 1200) // +500 rx, +200 tx
		if got := used(); got != 700 {
			t.Fatalf("before reset: %d, want 700", got)
		}

		got, err := s.ResetAccountUsage(ctx, a.ID, ns)
		if err != nil || got.DataUsedBytes != 0 {
			t.Fatalf("reset: %v %+v", err, got)
		}
		// The per-peer counters are what sampling diffs against; reset must leave them.
		if rx, tx := baselines(); rx != 1500 || tx != 1200 {
			t.Fatalf("peer baselines changed by reset: rx=%d tx=%d, want 1500/1200", rx, tx)
		}

		ingest(1600, 1250) // +100 rx, +50 tx since the pre-reset heartbeat
		if got := used(); got != 150 {
			t.Fatalf("after reset: %d, want 150 (just the new delta, not 850 or the absolute counters)", got)
		}
		if rx, tx := baselines(); rx != 1600 || tx != 1250 {
			t.Fatalf("baselines after sampling: rx=%d tx=%d", rx, tx)
		}
		// History is not rewritten: the charts keep both samples.
		var sampled int64
		if err := s.pool.QueryRow(ctx,
			`SELECT COALESCE(sum(rx_delta + tx_delta), 0) FROM peer_traffic_samples WHERE account_id = $1`, a.ID).Scan(&sampled); err != nil {
			t.Fatal(err)
		}
		if sampled != 850 {
			t.Fatalf("usage history = %d, want 850 (700 before + 150 after)", sampled)
		}
	})

	t.Run("clear flags", func(t *testing.T) {
		quota := int64(5000)
		limit := 3

		t.Run("clear quota and device limit; absent flags change nothing", func(t *testing.T) {
			ns := newNS()
			a := mk(t, ns, nil, &quota, nil)
			if _, err := s.pool.Exec(ctx, `UPDATE accounts SET device_limit = 3, device_limit_exceeded_at = now() WHERE id = $1`, a.ID); err != nil {
				t.Fatal(err)
			}

			label := "renamed"
			got, err := s.UpdateAccount(ctx, a.ID, ns, UpdateAccountParams{Label: &label})
			if err != nil || got.DataQuotaBytes == nil || *got.DataQuotaBytes != quota || got.DeviceLimit == nil || *got.DeviceLimit != limit {
				t.Fatalf("no flags: limits must be untouched: %v %+v", err, got)
			}
			if got.DeviceLimitExceededAt == nil {
				t.Fatal("no flags: the exceeded flag must be untouched")
			}

			got, err = s.UpdateAccount(ctx, a.ID, ns, UpdateAccountParams{ClearDataQuota: true})
			if err != nil || got.DataQuotaBytes != nil || got.DeviceLimit == nil {
				t.Fatalf("clear quota only: %v quota=%v device=%v", err, got.DataQuotaBytes, got.DeviceLimit)
			}
			got, err = s.UpdateAccount(ctx, a.ID, ns, UpdateAccountParams{ClearDeviceLimit: true})
			if err != nil || got.DeviceLimit != nil {
				t.Fatalf("clear device limit: %v %v", err, got.DeviceLimit)
			}
			if got.DeviceLimitExceededAt != nil {
				t.Fatal("a cleared device limit must also clear the standing exceeded flag (reconcile skips NULL limits, so nothing else would)")
			}
			if got.Label != "renamed" {
				t.Fatalf("label = %q", got.Label)
			}
		})

		t.Run("clearing the quota lifts only a non-expired quota_exceeded suspension", func(t *testing.T) {
			cases := []struct {
				name       string
				reason     *string
				expiry     *time.Time
				patchEnd   *time.Time // expiry_at sent in the same PATCH
				wantStatus string
			}{
				{"quota_exceeded", strp("quota_exceeded"), nil, nil, "active"},
				{"quota_exceeded, future expiry", strp("quota_exceeded"), &future, nil, "active"},
				{"quota_exceeded, expired", strp("quota_exceeded"), &past, nil, "suspended"},
				{"quota_exceeded, expired but the same PATCH extends it", strp("quota_exceeded"), &past, &future, "active"},
				{"manual", strp("manual"), nil, nil, "suspended"},
				{"device_limit", strp("device_limit"), nil, nil, "suspended"},
			}
			for _, c := range cases {
				t.Run(c.name, func(t *testing.T) {
					ns := newNS()
					a := mk(t, ns, nil, &quota, nil)
					setState(t, a.ID, "suspended", c.reason, c.expiry, 9000)
					got, err := s.UpdateAccount(ctx, a.ID, ns, UpdateAccountParams{ClearDataQuota: true, ExpiryAt: c.patchEnd})
					if err != nil {
						t.Fatal(err)
					}
					if got.DataQuotaBytes != nil {
						t.Fatalf("quota not cleared: %v", *got.DataQuotaBytes)
					}
					if got.Status != c.wantStatus {
						t.Fatalf("status = %s, want %s", got.Status, c.wantStatus)
					}
					if c.wantStatus == "active" && got.SuspendReason != nil {
						t.Fatalf("suspend_reason = %v, want NULL", *got.SuspendReason)
					}
					if c.wantStatus == "suspended" && (got.SuspendReason == nil || *got.SuspendReason != *c.reason) {
						t.Fatalf("a suspension that must stay put changed reason: %v", got.SuspendReason)
					}
					if got.DataUsedBytes != 9000 {
						t.Fatalf("clearing the quota must not touch usage: %d", got.DataUsedBytes)
					}
				})
			}
		})

		t.Run("raising a quota still does not lift a suspension (unchanged behaviour)", func(t *testing.T) {
			ns := newNS()
			a := mk(t, ns, nil, &quota, nil)
			setState(t, a.ID, "suspended", strp("quota_exceeded"), nil, 9000)
			bigger := int64(50_000)
			got, err := s.UpdateAccount(ctx, a.ID, ns, UpdateAccountParams{DataQuotaBytes: &bigger})
			if err != nil || got.Status != "suspended" || got.DataQuotaBytes == nil || *got.DataQuotaBytes != bigger {
				t.Fatalf("raise: %v %+v", err, got)
			}
		})

		t.Run("reseller with a data credit may not go unlimited", func(t *testing.T) {
			withCredit, err := s.CreateReseller(ctx, NewResellerParams{Username: "aapi-credit-" + sfx, PasswordHash: "h", DataLimitBytes: i64(100_000)})
			if err != nil {
				t.Fatal(err)
			}
			adminIDs = append(adminIDs, withCredit.AdminID)
			noCredit, err := s.CreateReseller(ctx, NewResellerParams{Username: "aapi-nocredit-" + sfx, PasswordHash: "h", MaxAccounts: ip(50)})
			if err != nil {
				t.Fatal(err)
			}
			adminIDs = append(adminIDs, noCredit.AdminID)

			creditNS := ResellerNamespace(withCredit.AdminID)
			namespaces = append(namespaces, creditNS)
			a := mk(t, &creditNS, nil, i64(10_000), nil)
			if _, err := s.UpdateAccount(ctx, a.ID, &creditNS, UpdateAccountParams{ClearDataQuota: true}); !errors.Is(err, ErrResellerUnlimitedNotAllowed) {
				t.Fatalf("clear under a credit: got %v, want ErrResellerUnlimitedNotAllowed", err)
			}
			if got, err := s.GetAccount(ctx, a.ID, &creditNS); err != nil || got.DataQuotaBytes == nil || *got.DataQuotaBytes != 10_000 {
				t.Fatalf("a refused clear must leave the quota alone: %v %+v", err, got)
			}
			// Clearing the device limit is not a credit matter.
			if _, err := s.UpdateAccount(ctx, a.ID, &creditNS, UpdateAccountParams{ClearDeviceLimit: true}); err != nil {
				t.Fatalf("clear device limit under a credit: %v", err)
			}
			// Staff are not bound by a reseller's credit.
			if got, err := s.UpdateAccount(ctx, a.ID, nil, UpdateAccountParams{ClearDataQuota: true}); err != nil || got.DataQuotaBytes != nil {
				t.Fatalf("admin clear: %v %+v", err, got)
			}

			// A reseller with no credit has nothing the rule protects.
			freeNS := ResellerNamespace(noCredit.AdminID)
			namespaces = append(namespaces, freeNS)
			b := mk(t, &freeNS, nil, i64(10_000), nil)
			if got, err := s.UpdateAccount(ctx, b.ID, &freeNS, UpdateAccountParams{ClearDataQuota: true}); err != nil || got.DataQuotaBytes != nil {
				t.Fatalf("clear without a credit: %v %+v", err, got)
			}
		})
	})

	t.Run("reset usage goes through the reseller gate", func(t *testing.T) {
		r, err := s.CreateReseller(ctx, NewResellerParams{Username: "aapi-reset-" + sfx, PasswordHash: "h", DataLimitBytes: i64(100_000)})
		if err != nil {
			t.Fatal(err)
		}
		adminIDs = append(adminIDs, r.AdminID)
		ns := ResellerNamespace(r.AdminID)
		namespaces = append(namespaces, ns)
		a := mk(t, &ns, nil, i64(10_000), nil)
		setState(t, a.ID, "active", nil, nil, 4000)

		// The credit counts quota handed out, so a reset by a credit-limited reseller would
		// re-sell the same quota for nothing: refused, usage untouched.
		if _, err := s.ResetAccountUsage(ctx, a.ID, &ns); !errors.Is(err, ErrResellerResetNotAllowed) {
			t.Fatalf("credit-limited reseller reset: got %v, want ErrResellerResetNotAllowed", err)
		}
		if got, err := s.GetAccount(ctx, a.ID, &ns); err != nil || got.DataUsedBytes != 4000 {
			t.Fatalf("a refused reset must leave usage: %v %+v", err, got)
		}

		// A reseller without a credit has nothing to re-sell: allowed.
		free, err := s.CreateReseller(ctx, NewResellerParams{Username: "aapi-reset-free-" + sfx, PasswordHash: "h"})
		if err != nil {
			t.Fatal(err)
		}
		adminIDs = append(adminIDs, free.AdminID)
		freeNS := ResellerNamespace(free.AdminID)
		namespaces = append(namespaces, freeNS)
		b := mk(t, &freeNS, nil, i64(10_000), nil)
		setState(t, b.ID, "active", nil, nil, 4000)
		if got, err := s.ResetAccountUsage(ctx, b.ID, &freeNS); err != nil || got.DataUsedBytes != 0 {
			t.Fatalf("reseller without a credit reset: %v %+v", err, got)
		}

		off := true
		if err := s.UpdateResellerLimits(ctx, r.AdminID, ResellerLimitsPatch{Disabled: &off}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ResetAccountUsage(ctx, a.ID, &ns); !errors.Is(err, ErrResellerDisabled) {
			t.Fatalf("disabled reseller: got %v, want ErrResellerDisabled", err)
		}
	})

	t.Run("last_handshake_at is the newest handshake across the account's peers", func(t *testing.T) {
		ns := newNS()
		a := mk(t, ns, nil, nil, nil)
		neverConnected := mk(t, ns, nil, nil, nil)

		got, err := s.GetAccount(ctx, a.ID, ns)
		if err != nil || got.LastHandshakeAt != nil {
			t.Fatalf("no handshake yet: %v %v", err, got.LastHandshakeAt)
		}

		older := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
		newer := time.Now().Add(-10 * time.Minute).Truncate(time.Second)
		set := func(nodeID string, at *time.Time) {
			t.Helper()
			if _, err := s.pool.Exec(ctx, `UPDATE account_peers SET last_handshake_at = $3 WHERE account_id = $1 AND node_id = $2`, a.ID, nodeID, at); err != nil {
				t.Fatal(err)
			}
		}
		check := func(label string, want *time.Time, got *time.Time) {
			t.Helper()
			switch {
			case want == nil && got != nil:
				t.Fatalf("%s: got %v, want nil", label, got)
			case want != nil && (got == nil || !got.Equal(*want)):
				t.Fatalf("%s: got %v, want %v", label, got, want)
			}
		}

		// One peer with a handshake and one without: the NULL must not win.
		set(node1.ID, &older)
		got, _ = s.GetAccount(ctx, a.ID, ns)
		check("single handshake", &older, got.LastHandshakeAt)

		set(node2.ID, &newer)
		got, _ = s.GetAccount(ctx, a.ID, ns)
		check("max of two", &newer, got.LastHandshakeAt)

		list, err := s.ListAccounts(ctx, 50, 0, ns, "")
		if err != nil || len(list) != 2 {
			t.Fatalf("list: %v %+v", err, list)
		}
		for _, l := range list {
			switch l.ID {
			case a.ID:
				check("list, connected account", &newer, l.LastHandshakeAt)
			case neverConnected.ID:
				check("list, never-connected account", nil, l.LastHandshakeAt)
			}
		}

		// Every statement that returns an Account carries it, not just the reads.
		suspended, err := s.SuspendAccount(ctx, a.ID, ns, "manual")
		if err != nil {
			t.Fatal(err)
		}
		check("suspend (UPDATE ... RETURNING)", &newer, suspended.LastHandshakeAt)
		renewed, err := s.RenewAccount(ctx, a.ID, ns, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		check("renew", &newer, renewed.LastHandshakeAt)

		// A real heartbeat moves it forward.
		now := time.Now().Truncate(time.Second)
		if err := s.IngestHeartbeatTelemetry(ctx, node1.ID, []PeerTrafficReport{
			{PublicKey: a.PublicKey, ReceiveBytes: 10, TransmitBytes: 10, LastHandshake: &now},
		}, nil); err != nil {
			t.Fatal(err)
		}
		got, _ = s.GetAccount(ctx, a.ID, ns)
		check("after a heartbeat", &now, got.LastHandshakeAt)
	})
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}
