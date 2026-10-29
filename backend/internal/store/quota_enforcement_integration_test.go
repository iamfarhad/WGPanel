package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// TestQuotaEnforcementIntegration covers data-cap enforcement (limits.go): suspension
// when the traffic path or a read finds an active account at/over its quota, lifting
// when the cap moves above usage, and the refusal to enable an account that is still
// over its cap. Skipped without WGPANEL_TEST_POSTGRES_DSN; removes what it creates.
func TestQuotaEnforcementIntegration(t *testing.T) {
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
	group := "qenf-" + sfx
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
		if _, err := s.pool.Exec(ctx, `UPDATE nodes SET status = 'online' WHERE id = $1`, n.ID); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// Two nodes: every account gets a peer on both, like a real fleet, and the cap has to
	// take the account off every one of them.
	node1 := mkNode("q1", "10.64.0.0/16")
	node2 := mkNode("q2", "10.65.0.0/16")

	var seq atomic.Int64
	newNS := func() *string {
		ns := fmt.Sprintf("qenf-ns-%s-%d", sfx, seq.Add(1))
		namespaces = append(namespaces, ns)
		return &ns
	}
	strp := func(v string) *string { return &v }
	mk := func(t *testing.T, ns *string, quota *int64) Account {
		t.Helper()
		n := seq.Add(1)
		a, err := s.CreateAccount(ctx, CreateAccountParams{
			Label: fmt.Sprintf("q-%d", n), PublicKey: fmt.Sprintf("pk-%s-%d", sfx, n), PrivateKeyEncrypted: "x",
			DataQuotaBytes: quota, SubscriptionToken: fmt.Sprintf("tok-%s-%d", sfx, n),
			OwnerKeyNamespace: ns, AllowedNodeGroups: []string{group},
		})
		if err != nil {
			t.Fatalf("create account: %v", err)
		}
		return a
	}
	setState := func(t *testing.T, id, status string, reason *string, expiry *time.Time, used int64) {
		t.Helper()
		if _, err := s.pool.Exec(ctx,
			`UPDATE accounts SET status = $2, suspend_reason = $3, expiry_at = $4, data_used_bytes = $5 WHERE id = $1`,
			id, status, reason, expiry, used); err != nil {
			t.Fatal(err)
		}
	}
	// raw reads the row without going through GetAccount, which would itself reconcile.
	raw := func(t *testing.T, id string) (status string, reason *string, used int64) {
		t.Helper()
		if err := s.pool.QueryRow(ctx, `SELECT status, suspend_reason, data_used_bytes FROM accounts WHERE id = $1`, id).
			Scan(&status, &reason, &used); err != nil {
			t.Fatal(err)
		}
		return
	}
	inFeed := func(t *testing.T, nodeID, publicKey string) bool {
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
	// sample feeds one heartbeat's cumulative WireGuard counters for the account's peer
	// on node1; the first call per account only seeds the baselines.
	sample := func(t *testing.T, a Account, rx, tx int64) {
		t.Helper()
		now := time.Now()
		if err := s.IngestHeartbeatTelemetry(ctx, node1.ID, []PeerTrafficReport{
			{PublicKey: a.PublicKey, ReceiveBytes: rx, TransmitBytes: tx, LastHandshake: &now},
		}, nil); err != nil {
			t.Fatalf("ingest: %v", err)
		}
	}
	reasonIs := func(got *string, want string) bool {
		if want == "" {
			return got == nil
		}
		return got != nil && *got == want
	}
	show := func(r *string) string {
		if r == nil {
			return "<nil>"
		}
		return *r
	}
	future := time.Now().Add(48 * time.Hour)
	past := time.Now().Add(-48 * time.Hour)

	t.Run("a sample reaching the cap suspends the account and drops it from every node's feed", func(t *testing.T) {
		ns := newNS()
		quota := int64(1000)
		a := mk(t, ns, &quota)
		sample(t, a, 0, 0) // seed

		sample(t, a, 600, 300) // 900 < 1000
		if st, reason, used := raw(t, a.ID); st != "active" || reason != nil || used != 900 {
			t.Fatalf("under the cap: %s/%s used=%d", st, show(reason), used)
		}
		if !inFeed(t, node1.ID, a.PublicKey) || !inFeed(t, node2.ID, a.PublicKey) {
			t.Fatal("an account under its cap must be in both nodes' desired peers")
		}

		sample(t, a, 700, 400) // +200 -> 1100 >= 1000
		st, reason, used := raw(t, a.ID)
		if st != "suspended" || !reasonIs(reason, "quota_exceeded") || used != 1100 {
			t.Fatalf("over the cap: %s/%s used=%d, want suspended/quota_exceeded/1100", st, show(reason), used)
		}
		if inFeed(t, node1.ID, a.PublicKey) || inFeed(t, node2.ID, a.PublicKey) {
			t.Fatal("a suspended account must be gone from the desired peers of every node (agents ReplacePeers from that list)")
		}

		// Later traffic keeps being counted but changes nothing else.
		sample(t, a, 800, 500)
		if st, reason, used := raw(t, a.ID); st != "suspended" || !reasonIs(reason, "quota_exceeded") || used != 1300 {
			t.Fatalf("after more traffic: %s/%s used=%d", st, show(reason), used)
		}

		// Like expiry enforcement, this writes no audit row.
		var audits int
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE target = $1`, a.ID).Scan(&audits); err != nil || audits != 0 {
			t.Fatalf("audit rows for the enforced account: %d, %v; enforcement must not write any", audits, err)
		}
	})

	t.Run("reaching the cap exactly counts as reached", func(t *testing.T) {
		ns := newNS()
		quota := int64(1000)
		a := mk(t, ns, &quota)
		sample(t, a, 0, 0)
		sample(t, a, 500, 500) // exactly 1000
		if st, reason, used := raw(t, a.ID); st != "suspended" || !reasonIs(reason, "quota_exceeded") || used != 1000 {
			t.Fatalf("at the cap: %s/%s used=%d", st, show(reason), used)
		}
	})

	t.Run("an unlimited (NULL quota) account is never suspended", func(t *testing.T) {
		ns := newNS()
		a := mk(t, ns, nil)
		sample(t, a, 0, 0)
		sample(t, a, 1<<40, 1<<40)
		if st, reason, used := raw(t, a.ID); st != "active" || reason != nil || used != 1<<41 {
			t.Fatalf("unlimited: %s/%s used=%d", st, show(reason), used)
		}
		got, err := s.GetAccount(ctx, a.ID, ns) // and the read-time rule agrees
		if err != nil || got.Status != "active" {
			t.Fatalf("unlimited on read: %v %+v", err, got.Status)
		}
		if !inFeed(t, node1.ID, a.PublicKey) {
			t.Fatal("unlimited account missing from the feed")
		}
	})

	t.Run("other suspend reasons and deleted accounts are never overwritten", func(t *testing.T) {
		for _, reason := range []string{"manual", "abuse_flag", "expired", "device_limit", SuspendReasonReseller, "quota_exceeded"} {
			t.Run(reason, func(t *testing.T) {
				ns := newNS()
				quota := int64(1000)
				a := mk(t, ns, &quota)
				var expiry *time.Time
				if reason == "expired" {
					expiry = &past
				}
				setState(t, a.ID, "suspended", &reason, expiry, 0)
				sample(t, a, 0, 0)
				sample(t, a, 2000, 0) // well over the cap
				if st, got, used := raw(t, a.ID); st != "suspended" || !reasonIs(got, reason) || used != 2000 {
					t.Fatalf("%s: became %s/%s used=%d, want it untouched", reason, st, show(got), used)
				}
				// The read-time pass leaves it alone too.
				if got, err := s.GetAccount(ctx, a.ID, ns); err != nil || !reasonIs(got.SuspendReason, reason) {
					t.Fatalf("%s on read: %v %s", reason, err, show(got.SuspendReason))
				}
			})
		}
		t.Run("deleted", func(t *testing.T) {
			ns := newNS()
			quota := int64(1000)
			a := mk(t, ns, &quota)
			sample(t, a, 0, 0)
			if _, err := s.SoftDeleteAccount(ctx, a.ID, ns); err != nil {
				t.Fatal(err)
			}
			sample(t, a, 2000, 0)
			if st, reason, _ := raw(t, a.ID); st != "deleted" || reason != nil {
				t.Fatalf("deleted account became %s/%s", st, show(reason))
			}
			setState(t, a.ID, "deleted", nil, nil, 5000)
			if _, err := s.ListAccounts(ctx, 10, 0, ns, ""); err != nil { // runs the read-time pass
				t.Fatal(err)
			}
			if st, _, _ := raw(t, a.ID); st != "deleted" {
				t.Fatalf("deleted account became %s after a read-time pass", st)
			}
		})
	})

	t.Run("expired wins over the cap", func(t *testing.T) {
		t.Run("traffic path", func(t *testing.T) {
			ns := newNS()
			quota := int64(1000)
			a := mk(t, ns, &quota)
			sample(t, a, 0, 0)
			// Past expiry that no read has reconciled yet, then a sample crossing the cap.
			setState(t, a.ID, "active", nil, &past, 0)
			sample(t, a, 2000, 0)
			if st, reason, _ := raw(t, a.ID); st != "suspended" || !reasonIs(reason, "expired") {
				t.Fatalf("expired and over the cap: %s/%s, want suspended/expired", st, show(reason))
			}
		})
		t.Run("read path", func(t *testing.T) {
			ns := newNS()
			quota := int64(1000)
			a := mk(t, ns, &quota)
			setState(t, a.ID, "active", nil, &past, 5000)
			got, err := s.GetAccount(ctx, a.ID, ns)
			if err != nil || got.Status != "suspended" || !reasonIs(got.SuspendReason, "expired") {
				t.Fatalf("read: %v %s/%s, want suspended/expired", err, got.Status, show(got.SuspendReason))
			}
		})
		t.Run("a quota PATCH on an expired, still-active account", func(t *testing.T) {
			ns := newNS()
			quota := int64(1000)
			a := mk(t, ns, &quota)
			setState(t, a.ID, "active", nil, &past, 600)
			got, err := s.UpdateAccount(ctx, a.ID, ns, UpdateAccountParams{DataQuotaBytes: i64(600)})
			if err != nil || got.Status != "suspended" || !reasonIs(got.SuspendReason, "expired") {
				t.Fatalf("patch: %v %s/%s, want suspended/expired", err, got.Status, show(got.SuspendReason))
			}
		})
	})

	t.Run("a read finds an account over its cap before any sample does", func(t *testing.T) {
		quota := int64(1000)
		// Each read path in turn, on its own account (the first read flips the row).
		reads := map[string]func(ns *string, a Account) (Account, error){
			"GetAccount": func(ns *string, a Account) (Account, error) { return s.GetAccount(ctx, a.ID, ns) },
			"ListAccounts": func(ns *string, a Account) (Account, error) {
				l, err := s.ListAccounts(ctx, 10, 0, ns, "")
				if err != nil || len(l) != 1 {
					return Account{}, fmt.Errorf("list: %v %d", err, len(l))
				}
				return l[0], nil
			},
			"subscription token": func(ns *string, a Account) (Account, error) {
				return s.GetAccountBySubscriptionToken(ctx, a.SubscriptionToken)
			},
		}
		for name, read := range reads {
			t.Run(name, func(t *testing.T) {
				ns := newNS()
				a := mk(t, ns, &quota)
				// "A quota lowered below what is already used": same state, reached by SQL.
				setState(t, a.ID, "active", nil, nil, 400)
				if _, err := s.pool.Exec(ctx, `UPDATE accounts SET data_quota_bytes = 300 WHERE id = $1`, a.ID); err != nil {
					t.Fatal(err)
				}
				got, err := read(ns, a)
				if err != nil {
					t.Fatal(err)
				}
				if got.Status != "suspended" || !reasonIs(got.SuspendReason, "quota_exceeded") {
					t.Fatalf("%s: %s/%s, want suspended/quota_exceeded", name, got.Status, show(got.SuspendReason))
				}
				if inFeed(t, node1.ID, a.PublicKey) {
					t.Fatal("still in the desired peers after the read-time suspension")
				}
			})
		}
		t.Run("a unlimited account over any number stays active on read", func(t *testing.T) {
			ns := newNS()
			a := mk(t, ns, nil)
			setState(t, a.ID, "active", nil, nil, 1<<50)
			if got, err := s.GetAccount(ctx, a.ID, ns); err != nil || got.Status != "active" {
				t.Fatalf("%v %s", err, got.Status)
			}
		})
	})

	t.Run("PATCH moves the cap: lifts above usage, suspends at or below it", func(t *testing.T) {
		cases := []struct {
			name       string
			status     string
			reason     string
			expiry     *time.Time
			used       int64
			patch      UpdateAccountParams
			wantStatus string
			wantReason string
		}{
			{"raise above usage lifts", "suspended", "quota_exceeded", nil, 1500, UpdateAccountParams{DataQuotaBytes: i64(2000)}, "active", ""},
			{"raise above usage lifts (future expiry)", "suspended", "quota_exceeded", &future, 1500, UpdateAccountParams{DataQuotaBytes: i64(2000)}, "active", ""},
			{"raise to exactly the usage does not lift", "suspended", "quota_exceeded", nil, 1500, UpdateAccountParams{DataQuotaBytes: i64(1500)}, "suspended", "quota_exceeded"},
			{"raise but still below usage does not lift", "suspended", "quota_exceeded", nil, 1500, UpdateAccountParams{DataQuotaBytes: i64(1200)}, "suspended", "quota_exceeded"},
			{"raise on an expired account does not lift", "suspended", "quota_exceeded", &past, 1500, UpdateAccountParams{DataQuotaBytes: i64(2000)}, "suspended", "quota_exceeded"},
			{"raise plus a new expiry lifts", "suspended", "quota_exceeded", &past, 1500, UpdateAccountParams{DataQuotaBytes: i64(2000), ExpiryAt: &future}, "active", ""},
			{"raise does not lift manual", "suspended", "manual", nil, 1500, UpdateAccountParams{DataQuotaBytes: i64(2000)}, "suspended", "manual"},
			{"raise does not lift abuse_flag", "suspended", "abuse_flag", nil, 1500, UpdateAccountParams{DataQuotaBytes: i64(2000)}, "suspended", "abuse_flag"},
			{"raise does not lift device_limit", "suspended", "device_limit", nil, 1500, UpdateAccountParams{DataQuotaBytes: i64(2000)}, "suspended", "device_limit"},
			{"raise does not lift expired", "suspended", "expired", &past, 1500, UpdateAccountParams{DataQuotaBytes: i64(2000)}, "suspended", "expired"},
			{"a PATCH that leaves the quota alone lifts nothing", "suspended", "quota_exceeded", nil, 500, UpdateAccountParams{Label: strp("renamed")}, "suspended", "quota_exceeded"},
			{"lower to the usage suspends", "active", "", nil, 600, UpdateAccountParams{DataQuotaBytes: i64(600)}, "suspended", "quota_exceeded"},
			{"lower below the usage suspends", "active", "", nil, 600, UpdateAccountParams{DataQuotaBytes: i64(100)}, "suspended", "quota_exceeded"},
			{"lower but still above the usage keeps it active", "active", "", nil, 600, UpdateAccountParams{DataQuotaBytes: i64(601)}, "active", ""},
			{"lower does not touch manual", "suspended", "manual", nil, 600, UpdateAccountParams{DataQuotaBytes: i64(100)}, "suspended", "manual"},
			{"a PATCH that leaves an over-cap active account alone still settles it", "active", "", nil, 5000, UpdateAccountParams{Label: strp("renamed")}, "suspended", "quota_exceeded"},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				ns := newNS()
				quota := int64(1000)
				a := mk(t, ns, &quota)
				var reason *string
				if c.reason != "" {
					reason = strp(c.reason)
				}
				setState(t, a.ID, c.status, reason, c.expiry, c.used)

				got, err := s.UpdateAccount(ctx, a.ID, ns, c.patch)
				if err != nil {
					t.Fatal(err)
				}
				if got.Status != c.wantStatus || !reasonIs(got.SuspendReason, c.wantReason) {
					t.Fatalf("response %s/%s, want %s/%q", got.Status, show(got.SuspendReason), c.wantStatus, c.wantReason)
				}
				// The response is what was committed, and the feed follows it.
				if st, rs, used := raw(t, a.ID); st != c.wantStatus || !reasonIs(rs, c.wantReason) || used != c.used {
					t.Fatalf("stored %s/%s used=%d (was %d)", st, show(rs), used, c.used)
				}
				if in := inFeed(t, node1.ID, a.PublicKey); in != (c.wantStatus == "active") {
					t.Fatalf("in desired peers = %v, want %v", in, c.wantStatus == "active")
				}
			})
		}

		t.Run("quota set on an unlimited account that already used more suspends it", func(t *testing.T) {
			ns := newNS()
			a := mk(t, ns, nil)
			setState(t, a.ID, "active", nil, nil, 5000)
			got, err := s.UpdateAccount(ctx, a.ID, ns, UpdateAccountParams{DataQuotaBytes: i64(1000)})
			if err != nil || got.Status != "suspended" || !reasonIs(got.SuspendReason, "quota_exceeded") {
				t.Fatalf("%v %s/%s", err, got.Status, show(got.SuspendReason))
			}
		})
	})

	t.Run("renew, reset-usage and clear still lift; and respect the cap and expiry", func(t *testing.T) {
		type fixture struct {
			a  Account
			ns *string
		}
		setup := func(t *testing.T, status, reason string, expiry *time.Time, quota *int64, used int64) fixture {
			t.Helper()
			ns := newNS()
			a := mk(t, ns, quota)
			var rs *string
			if reason != "" {
				rs = strp(reason)
			}
			setState(t, a.ID, status, rs, expiry, used)
			return fixture{a, ns}
		}
		check := func(t *testing.T, got Account, err error, wantStatus, wantReason string) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != wantStatus || !reasonIs(got.SuspendReason, wantReason) {
				t.Fatalf("response %s/%s, want %s/%q", got.Status, show(got.SuspendReason), wantStatus, wantReason)
			}
			if st, rs, _ := raw(t, got.ID); st != wantStatus || !reasonIs(rs, wantReason) {
				t.Fatalf("stored %s/%s, want %s/%q", st, show(rs), wantStatus, wantReason)
			}
		}

		t.Run("renew adding quota above usage lifts", func(t *testing.T) {
			f := setup(t, "suspended", "quota_exceeded", nil, i64(1000), 1500)
			got, err := s.RenewAccount(ctx, f.a.ID, f.ns, i64(1000), nil) // 2000 > 1500
			check(t, got, err, "active", "")
		})
		t.Run("renew adding too little quota does not", func(t *testing.T) {
			f := setup(t, "suspended", "quota_exceeded", nil, i64(1000), 1500)
			got, err := s.RenewAccount(ctx, f.a.ID, f.ns, i64(500), nil) // 1500 == used
			check(t, got, err, "suspended", "quota_exceeded")
		})
		t.Run("renew with data alone does not revive an expired account", func(t *testing.T) {
			f := setup(t, "suspended", "quota_exceeded", &past, i64(1000), 1500)
			got, err := s.RenewAccount(ctx, f.a.ID, f.ns, i64(5000), nil)
			check(t, got, err, "suspended", "quota_exceeded")
		})
		t.Run("renew with data and validity lifts", func(t *testing.T) {
			f := setup(t, "suspended", "quota_exceeded", &past, i64(1000), 1500)
			ext := future
			got, err := s.RenewAccount(ctx, f.a.ID, f.ns, i64(5000), &ext)
			check(t, got, err, "active", "")
		})
		t.Run("renew extending an expired account that is still over its cap lands on quota_exceeded", func(t *testing.T) {
			f := setup(t, "suspended", "expired", &past, i64(1000), 1500)
			ext := future
			got, err := s.RenewAccount(ctx, f.a.ID, f.ns, nil, &ext)
			check(t, got, err, "suspended", "quota_exceeded")
		})
		t.Run("renew extending an expired account under its cap lifts", func(t *testing.T) {
			f := setup(t, "suspended", "expired", &past, i64(1000), 100)
			ext := future
			got, err := s.RenewAccount(ctx, f.a.ID, f.ns, nil, &ext)
			check(t, got, err, "active", "")
		})
		t.Run("renew with negative quota that drops below usage suspends", func(t *testing.T) {
			f := setup(t, "active", "", nil, i64(1000), 700)
			got, err := s.RenewAccount(ctx, f.a.ID, f.ns, i64(-400), nil) // 600 <= 700
			check(t, got, err, "suspended", "quota_exceeded")
		})
		t.Run("renew leaves a manual suspension alone", func(t *testing.T) {
			f := setup(t, "suspended", "manual", nil, i64(1000), 1500)
			got, err := s.RenewAccount(ctx, f.a.ID, f.ns, i64(5000), nil)
			check(t, got, err, "suspended", "manual")
		})

		t.Run("reset-usage lifts", func(t *testing.T) {
			f := setup(t, "suspended", "quota_exceeded", nil, i64(1000), 1500)
			got, err := s.ResetAccountUsage(ctx, f.a.ID, f.ns)
			check(t, got, err, "active", "")
			if got.DataUsedBytes != 0 {
				t.Fatalf("used = %d", got.DataUsedBytes)
			}
		})
		t.Run("reset-usage does not revive an expired account", func(t *testing.T) {
			f := setup(t, "suspended", "quota_exceeded", &past, i64(1000), 1500)
			got, err := s.ResetAccountUsage(ctx, f.a.ID, f.ns)
			check(t, got, err, "suspended", "quota_exceeded")
		})
		t.Run("reset-usage on a zero-byte quota cannot lift (0 >= 0 is reached)", func(t *testing.T) {
			f := setup(t, "suspended", "quota_exceeded", nil, i64(0), 10)
			got, err := s.ResetAccountUsage(ctx, f.a.ID, f.ns)
			check(t, got, err, "suspended", "quota_exceeded")
		})

		t.Run("clear_data_quota lifts", func(t *testing.T) {
			f := setup(t, "suspended", "quota_exceeded", nil, i64(1000), 1500)
			got, err := s.UpdateAccount(ctx, f.a.ID, f.ns, UpdateAccountParams{ClearDataQuota: true})
			check(t, got, err, "active", "")
			if got.DataQuotaBytes != nil {
				t.Fatal("quota not cleared")
			}
		})
		t.Run("clear_data_quota does not revive an expired account", func(t *testing.T) {
			f := setup(t, "suspended", "quota_exceeded", &past, i64(1000), 1500)
			got, err := s.UpdateAccount(ctx, f.a.ID, f.ns, UpdateAccountParams{ClearDataQuota: true})
			check(t, got, err, "suspended", "quota_exceeded")
		})
	})

	t.Run("enable is refused while the account is over its cap", func(t *testing.T) {
		setup := func(t *testing.T, reason string, quota *int64, used int64) (Account, *string) {
			t.Helper()
			ns := newNS()
			a := mk(t, ns, quota)
			setState(t, a.ID, "suspended", strp(reason), nil, used)
			return a, ns
		}
		t.Run("quota_exceeded and over the cap", func(t *testing.T) {
			a, ns := setup(t, "quota_exceeded", i64(1000), 1500)
			if _, err := s.EnableAccount(ctx, a.ID, ns); !errors.Is(err, ErrAccountQuotaExhausted) {
				t.Fatalf("got %v, want ErrAccountQuotaExhausted", err)
			}
			if st, rs, _ := raw(t, a.ID); st != "suspended" || !reasonIs(rs, "quota_exceeded") {
				t.Fatalf("a refused enable changed the row: %s/%s", st, show(rs))
			}
			// Exactly at the cap is over.
			if _, err := s.pool.Exec(ctx, `UPDATE accounts SET data_used_bytes = 1000 WHERE id = $1`, a.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.EnableAccount(ctx, a.ID, ns); !errors.Is(err, ErrAccountQuotaExhausted) {
				t.Fatalf("at the cap: got %v", err)
			}
		})
		t.Run("a manual suspension over the cap is refused too: it would be re-suspended at once", func(t *testing.T) {
			a, ns := setup(t, "manual", i64(1000), 1500)
			if _, err := s.EnableAccount(ctx, a.ID, ns); !errors.Is(err, ErrAccountQuotaExhausted) {
				t.Fatalf("got %v", err)
			}
		})
		t.Run("allowed once the cap is above usage", func(t *testing.T) {
			a, ns := setup(t, "manual", i64(1000), 1500)
			if _, err := s.EnableAccount(ctx, a.ID, ns); !errors.Is(err, ErrAccountQuotaExhausted) {
				t.Fatalf("before the raise: %v", err)
			}
			got, err := s.UpdateAccount(ctx, a.ID, ns, UpdateAccountParams{DataQuotaBytes: i64(5000)})
			if err != nil || got.Status != "suspended" || !reasonIs(got.SuspendReason, "manual") {
				t.Fatalf("the raise must leave a manual suspension: %v %s/%s", err, got.Status, show(got.SuspendReason))
			}
			on, err := s.EnableAccount(ctx, a.ID, ns)
			if err != nil || on.Status != "active" || on.SuspendReason != nil {
				t.Fatalf("after the raise: %v %+v", err, on.Status)
			}
			if !inFeed(t, node1.ID, a.PublicKey) {
				t.Fatal("enabled account not in the feed")
			}
		})
		t.Run("unlimited accounts and accounts under their cap enable as before", func(t *testing.T) {
			a, ns := setup(t, "manual", nil, 1<<45)
			if got, err := s.EnableAccount(ctx, a.ID, ns); err != nil || got.Status != "active" {
				t.Fatalf("unlimited: %v", err)
			}
			b, ns2 := setup(t, "abuse_flag", i64(1000), 999)
			if got, err := s.EnableAccount(ctx, b.ID, ns2); err != nil || got.Status != "active" {
				t.Fatalf("under the cap: %v", err)
			}
		})
		t.Run("404 and the reseller lock keep precedence", func(t *testing.T) {
			a, ns := setup(t, "quota_exceeded", i64(1000), 1500)
			if _, err := s.EnableAccount(ctx, a.ID, newNS()); !errors.Is(err, ErrAccountNotFound) {
				t.Fatalf("foreign namespace: %v", err)
			}
			if _, err := s.EnableAccount(ctx, "00000000-0000-0000-0000-000000000000", ns); !errors.Is(err, ErrAccountNotFound) {
				t.Fatalf("unknown id: %v", err)
			}
			if _, err := s.SoftDeleteAccount(ctx, a.ID, ns); err != nil {
				t.Fatal(err)
			}
			if _, err := s.EnableAccount(ctx, a.ID, ns); !errors.Is(err, ErrAccountNotFound) {
				t.Fatalf("deleted: %v", err)
			}

			r, err := s.CreateReseller(ctx, NewResellerParams{Username: "qenf-res-" + sfx, PasswordHash: "h"})
			if err != nil {
				t.Fatal(err)
			}
			adminIDs = append(adminIDs, r.AdminID)
			rns := ResellerNamespace(r.AdminID)
			namespaces = append(namespaces, rns)
			locked := mk(t, &rns, i64(1000))
			setState(t, locked.ID, "suspended", strp(SuspendReasonReseller), nil, 1500)
			if _, err := s.EnableAccount(ctx, locked.ID, &rns); !errors.Is(err, ErrAccountLockedByAdmin) {
				t.Fatalf("reseller_suspended and over the cap: got %v, want ErrAccountLockedByAdmin", err)
			}
			// A reseller enabling an account that is simply active is a harmless no-op.
			fine := mk(t, &rns, i64(1000))
			if got, err := s.EnableAccount(ctx, fine.ID, &rns); err != nil || got.Status != "active" {
				t.Fatalf("reseller enable of an active account: %v", err)
			}
		})
	})

	t.Run("the reseller bulk reactivate agrees with the rule", func(t *testing.T) {
		r, err := s.CreateReseller(ctx, NewResellerParams{Username: "qenf-bulk-" + sfx, PasswordHash: "h"})
		if err != nil {
			t.Fatal(err)
		}
		adminIDs = append(adminIDs, r.AdminID)
		rns := ResellerNamespace(r.AdminID)
		namespaces = append(namespaces, rns)
		over := mk(t, &rns, i64(1000))
		overAndExpired := mk(t, &rns, i64(1000))
		fine := mk(t, &rns, i64(1000))
		setState(t, over.ID, "active", nil, nil, 1500)
		setState(t, overAndExpired.ID, "active", nil, &past, 1500)
		setState(t, fine.ID, "active", nil, nil, 10)
		if _, err := s.SuspendResellerAccounts(ctx, r.AdminID); err != nil {
			t.Fatal(err)
		}
		reactivated, still, err := s.ReactivateResellerAccounts(ctx, r.AdminID)
		if err != nil || reactivated != 1 || still != 2 {
			t.Fatalf("reactivate: %v reactivated=%d still=%d, want 1 and 2", err, reactivated, still)
		}
		for id, want := range map[string]string{over.ID: "quota_exceeded", overAndExpired.ID: "expired"} {
			if st, rs, _ := raw(t, id); st != "suspended" || !reasonIs(rs, want) {
				t.Fatalf("%s: %s/%s, want suspended/%s", id, st, show(rs), want)
			}
		}
		if st, rs, _ := raw(t, fine.ID); st != "active" || rs != nil {
			t.Fatalf("fine: %s/%s", st, show(rs))
		}
	})
}
