package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// TestReportsIntegration covers scoping isolation, gap-filled series, quota/expiry
// bucketing and the continuous-aggregate-backed day path against a real TimescaleDB.
func TestReportsIntegration(t *testing.T) {
	dsn := os.Getenv("WGPANEL_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set WGPANEL_TEST_POSTGRES_DSN to run store integration tests")
	}
	ctx := context.Background()
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	// The no-tx migration must be re-runnable (a crash midway leaves it unrecorded) and
	// the aggregate must keep real-time aggregation on.
	if _, err := s.pool.Exec(ctx, `DELETE FROM schema_migrations WHERE filename = '0023_traffic_daily.sql'`); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("re-running the no-tx migration: %v", err)
	}
	var materializedOnly bool
	var tsVersion string
	if err := s.pool.QueryRow(ctx, `SELECT materialized_only FROM timescaledb_information.continuous_aggregates WHERE view_name = 'traffic_daily'`).Scan(&materializedOnly); err != nil || materializedOnly {
		t.Fatalf("traffic_daily materialized_only=%v err=%v, want real-time aggregation", materializedOnly, err)
	}
	_ = s.pool.QueryRow(ctx, `SELECT extversion FROM pg_extension WHERE extname='timescaledb'`).Scan(&tsVersion)
	t.Logf("timescaledb %s", tsVersion)

	sfx := fmt.Sprint(time.Now().UnixNano())
	key := "rp-" + sfx
	node, err := s.CreateNode(ctx, "rp-"+sfx, "g-"+sfx, "eu", "r.test:51820", "10.66.0.0/16", 500, &key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE nodes SET status='online' WHERE id=$1`, node.ID); err != nil {
		t.Fatal(err)
	}
	mkRes := func(name string) Reseller {
		r, err := s.CreateReseller(ctx, NewResellerParams{Username: name + sfx, PasswordHash: "h"})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r1, r2 := mkRes("rpa"), mkRes("rpb")
	seq := 0
	mkAcct := func(owner *string, quota *int64, expiry *time.Time) Account {
		seq++
		a, err := s.CreateAccount(ctx, CreateAccountParams{
			Label: fmt.Sprintf("acct-%d", seq), PublicKey: fmt.Sprintf("rpk-%s-%d", sfx, seq), PrivateKeyEncrypted: "x",
			SubscriptionToken: fmt.Sprintf("rpt-%s-%d", sfx, seq), DataQuotaBytes: quota, ExpiryAt: expiry, OwnerKeyNamespace: owner,
		})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	ns1, ns2 := ResellerNamespace(r1.AdminID), ResellerNamespace(r2.AdminID)
	a1 := mkAcct(&ns1, nil, nil)
	a2 := mkAcct(&ns2, nil, nil)
	sample := func(acct Account, ago time.Duration, rx, tx int64) {
		if _, err := s.pool.Exec(ctx, `INSERT INTO peer_traffic_samples (ts, account_id, node_id, rx_delta, tx_delta) VALUES ($1,$2,$3,$4,$5)`,
			time.Now().Add(-ago), acct.ID, node.ID, rx, tx); err != nil {
			t.Fatal(err)
		}
	}
	sample(a1, 30*time.Second, 100, 10)
	sample(a1, 5*time.Hour, 200, 20)
	sample(a1, 30*time.Hour, 400, 40) // yesterday-ish: outside 24h, inside 7d
	sample(a2, 2*time.Hour, 7000, 700)

	t.Run("reseller scope is isolated", func(t *testing.T) {
		r, err := s.BuildReport(ctx, ReportParams{RangeKey: "7d", CallerNamespace: &ns1})
		if err != nil {
			t.Fatal(err)
		}
		if r.Totals.RxBytes != 700 || r.Totals.TxBytes != 70 || r.Totals.ActiveAccounts != 1 || r.Totals.AccountsTotal != 1 {
			t.Fatalf("totals = %+v", r.Totals)
		}
		if len(r.TopAccounts) != 1 || r.TopAccounts[0].ID != a1.ID {
			t.Fatalf("top = %+v", r.TopAccounts)
		}
		if r.Resellers != nil {
			t.Fatal("reseller leaderboard leaked to a reseller")
		}
		for _, n := range r.Nodes {
			// Accounts fan out to every online node (other tests leave some), so only
			// this test's node carries traffic - and never the other reseller's 7000.
			if n.CPUAvg != nil || n.MemAvgPercent != nil || (n.ID == node.ID && n.RxBytes != 700) || (n.ID != node.ID && n.RxBytes != 0) {
				t.Fatalf("node leak/metrics for reseller: %+v", n)
			}
		}
		if r.Scope.ResellerID == nil || *r.Scope.ResellerID != r1.AdminID {
			t.Fatalf("scope = %+v", r.Scope)
		}
		// A caller-supplied owner filter cannot widen a pinned namespace.
		w, _ := s.BuildReport(ctx, ReportParams{RangeKey: "7d", CallerNamespace: &ns1, OwnerFilter: ns2})
		if w.Totals.RxBytes != 0 || w.Totals.AccountsTotal != 0 {
			t.Fatalf("owner filter crossed namespaces: %+v", w.Totals)
		}
	})

	t.Run("series are gap-free and evenly spaced", func(t *testing.T) {
		for key, n := range map[string]int{"24h": 24, "7d": 168, "30d": 30, "90d": 90, "365d": 365} {
			r, err := s.BuildReport(ctx, ReportParams{RangeKey: key, CallerNamespace: &ns1})
			if err != nil {
				t.Fatal(key, err)
			}
			if len(r.TrafficSeries) != n || len(r.NewAccountsSeries) != n {
				t.Fatalf("%s: %d/%d points, want %d", key, len(r.TrafficSeries), len(r.NewAccountsSeries), n)
			}
			step := r.TrafficSeries[1].TS.Sub(r.TrafficSeries[0].TS)
			for i := 1; i < n; i++ {
				if r.TrafficSeries[i].TS.Sub(r.TrafficSeries[i-1].TS) != step {
					t.Fatalf("%s: uneven spacing at %d", key, i)
				}
			}
		}
		r, _ := s.BuildReport(ctx, ReportParams{RangeKey: "24h", CallerNamespace: &ns1})
		if r.Totals.RxBytes != 300 { // the 30h-old sample is outside 24h
			t.Fatalf("24h rx = %d", r.Totals.RxBytes)
		}
	})

	t.Run("staff scoping and leaderboard", func(t *testing.T) {
		all, _ := s.BuildReport(ctx, ReportParams{RangeKey: "7d", IncludeNodeMetrics: true, IncludeResellers: true})
		if all.Totals.RxBytes < 7700 || len(all.Resellers) < 2 {
			t.Fatalf("staff view: rx=%d resellers=%d", all.Totals.RxBytes, len(all.Resellers))
		}
		one, _ := s.BuildReport(ctx, ReportParams{RangeKey: "7d", OwnerFilter: ns2})
		if one.Totals.RxBytes != 7000 || one.Totals.AccountsTotal != 1 {
			t.Fatalf("owner=%s: %+v", ns2, one.Totals)
		}
		admin, _ := s.BuildReport(ctx, ReportParams{RangeKey: "7d", OwnerFilter: "admin"})
		for _, tp := range admin.TopAccounts {
			if tp.Owner != nil {
				t.Fatalf("owner=admin returned owned account %+v", tp)
			}
		}
	})

	t.Run("day path reads the continuous aggregate", func(t *testing.T) {
		// Refresh first: on a reused DB an earlier refresh may have advanced the
		// materialisation watermark past these just-inserted rows.
		if _, err := s.pool.Exec(ctx, `CALL refresh_continuous_aggregate('traffic_daily', now() - interval '3 days', now() + interval '1 day')`); err != nil {
			t.Fatal(err)
		}
		materialised, _ := s.BuildReport(ctx, ReportParams{RangeKey: "30d", CallerNamespace: &ns1})
		if materialised.Totals.RxBytes != 700 || materialised.Totals.TxBytes != 70 {
			t.Fatalf("materialised rx=%d tx=%d", materialised.Totals.RxBytes, materialised.Totals.TxBytes)
		}
		if materialised.Range.Bucket != "day" || materialised.Totals.ActiveAccounts != 1 {
			t.Fatalf("range/active = %+v / %d", materialised.Range, materialised.Totals.ActiveAccounts)
		}
	})

	t.Run("quota and expiry buckets", func(t *testing.T) {
		ns3 := ResellerNamespace(mkRes("rpc").AdminID)
		q := func(v int64) *int64 { return &v }
		at := func(d time.Duration) *time.Time { t := time.Now().Add(d); return &t }
		set := func(a Account, used int64) {
			if _, err := s.pool.Exec(ctx, `UPDATE accounts SET data_used_bytes=$2 WHERE id=$1`, a.ID, used); err != nil {
				t.Fatal(err)
			}
		}
		mkAcct(&ns3, nil, nil)                              // unlimited, never
		set(mkAcct(&ns3, q(100), at(2*24*time.Hour)), 10)   // lt50, 3d
		set(mkAcct(&ns3, q(100), at(5*24*time.Hour)), 50)   // 50to80, 7d
		set(mkAcct(&ns3, q(100), at(20*24*time.Hour)), 85)  // 80to100, 30d
		set(mkAcct(&ns3, q(100), at(90*24*time.Hour)), 100) // exceeded, later
		mkAcct(&ns3, nil, at(-time.Hour))                   // expired
		r, err := s.BuildReport(ctx, ReportParams{RangeKey: "7d", CallerNamespace: &ns3})
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]int{"unlimited": 2, "lt50": 1, "50to80": 1, "80to100": 1, "exceeded": 1}
		for _, b := range r.QuotaBuckets {
			if b.Count != want[b.Bucket] {
				t.Errorf("quota %s = %d, want %d", b.Bucket, b.Count, want[b.Bucket])
			}
		}
		wantExp := map[string]int{"never": 1, "3d": 1, "7d": 1, "30d": 1, "later": 1, "expired": 1}
		for _, b := range r.ExpiryBuckets {
			if b.Count != wantExp[b.Bucket] {
				t.Errorf("expiry %s = %d, want %d", b.Bucket, b.Count, wantExp[b.Bucket])
			}
		}
		if len(r.QuotaBuckets) != 5 || len(r.ExpiryBuckets) != 7 {
			t.Fatalf("bucket lists not complete: %d/%d", len(r.QuotaBuckets), len(r.ExpiryBuckets))
		}
		if len(r.Expiring) != 2 || !r.Expiring[0].ExpiryAt.Before(r.Expiring[1].ExpiryAt) {
			t.Fatalf("expiring = %+v", r.Expiring)
		}
		if r.Totals.NewAccounts != 6 {
			t.Fatalf("new accounts = %d", r.Totals.NewAccounts)
		}
	})
}
