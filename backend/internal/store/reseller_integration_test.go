package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func i64(v int64) *int64 { return &v }
func ip(v int) *int      { return &v }

// TestResellerQuotaIntegration covers reseller limit enforcement against a real
// Postgres (skipped without WGPANEL_TEST_POSTGRES_DSN, like TestStoreIntegration).
func TestResellerQuotaIntegration(t *testing.T) {
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

	suffix := fmt.Sprint(time.Now().UnixNano())
	nodeKey := "rk-" + suffix
	node, err := s.CreateNode(ctx, "rs-eu-"+suffix, "eu-"+suffix, "eu", "vpn.test:51820", "10.88.0.0/16", 500, &nodeKey)
	if err != nil {
		t.Fatal(err)
	}
	k2 := "rk2-" + suffix
	other, err := s.CreateNode(ctx, "rs-us-"+suffix, "us-"+suffix, "us", "vpn2.test:51820", "10.89.0.0/16", 500, &k2)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{node.ID, other.ID} {
		if _, err := s.pool.Exec(ctx, `UPDATE nodes SET status='online' WHERE id=$1`, id); err != nil {
			t.Fatal(err)
		}
	}

	var n atomic.Int64
	create := func(res Reseller, quota *int64, expiry *time.Time, nodeID string) (Account, error) {
		seq := n.Add(1)
		ns := ResellerNamespace(res.AdminID)
		return s.CreateAccount(ctx, CreateAccountParams{
			Label: "a", NodeIDOrAuto: nodeID, PublicKey: fmt.Sprintf("pk-%s-%d", suffix, seq),
			PrivateKeyEncrypted: "x", DataQuotaBytes: quota, ExpiryAt: expiry,
			SubscriptionToken: fmt.Sprintf("tok-%s-%d", suffix, seq), OwnerKeyNamespace: &ns,
		})
	}
	mk := func(name string, p NewResellerParams) Reseller {
		p.Username = name + suffix
		p.PasswordHash = "h"
		r, err := s.CreateReseller(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}

	t.Run("max_accounts", func(t *testing.T) {
		r := mk("maxacc", NewResellerParams{MaxAccounts: ip(2)})
		for i := 0; i < 2; i++ {
			if _, err := create(r, nil, nil, ""); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := create(r, nil, nil, ""); !errors.Is(err, ErrResellerAccountLimit) {
			t.Fatalf("want ErrResellerAccountLimit, got %v", err)
		}
	})

	t.Run("deleting frees a slot", func(t *testing.T) {
		r := mk("freeslot", NewResellerParams{MaxAccounts: ip(1)})
		a, _ := create(r, nil, nil, "")
		ns := ResellerNamespace(r.AdminID)
		if _, err := s.SoftDeleteAccount(ctx, a.ID, &ns); err != nil {
			t.Fatal(err)
		}
		if _, err := create(r, nil, nil, ""); err != nil {
			t.Fatalf("slot not freed: %v", err)
		}
	})

	t.Run("data credit, unlimited, update and renew", func(t *testing.T) {
		r := mk("credit", NewResellerParams{DataLimitBytes: i64(100)})
		ns := ResellerNamespace(r.AdminID)
		if _, err := create(r, nil, nil, ""); !errors.Is(err, ErrResellerUnlimitedNotAllowed) {
			t.Fatalf("unlimited: %v", err)
		}
		a, err := create(r, i64(60), nil, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := create(r, i64(41), nil, ""); !errors.Is(err, ErrResellerDataCredit) {
			t.Fatalf("overshoot: %v", err)
		}
		if _, err := create(r, i64(40), nil, ""); err != nil {
			t.Fatalf("exact fit: %v", err)
		}
		if _, err := s.UpdateAccount(ctx, a.ID, &ns, UpdateAccountParams{DataQuotaBytes: i64(61)}); !errors.Is(err, ErrResellerDataCredit) {
			t.Fatalf("update overshoot: %v", err)
		}
		if _, err := s.UpdateAccount(ctx, a.ID, &ns, UpdateAccountParams{DataQuotaBytes: i64(10)}); err != nil {
			t.Fatalf("shrinking: %v", err)
		}
		if _, err := s.RenewAccount(ctx, a.ID, &ns, i64(51), nil); !errors.Is(err, ErrResellerDataCredit) {
			t.Fatalf("renew overshoot: %v", err)
		}
		if _, err := s.RenewAccount(ctx, a.ID, &ns, i64(50), nil); err != nil {
			t.Fatalf("renew fit: %v", err)
		}
		got, _ := s.GetReseller(ctx, r.AdminID)
		if got.Usage.DataAllocatedBytes != 100 || got.Usage.AccountsUsed != 2 {
			t.Fatalf("usage = %+v", got.Usage)
		}
	})

	t.Run("plan expiry date caps accounts and blocks an expired reseller", func(t *testing.T) {
		planEnd := time.Now().Add(30 * 24 * time.Hour)
		r := mk("expiry", NewResellerParams{ExpiresAt: &planEnd})
		ok := time.Now().Add(29 * 24 * time.Hour)
		far := time.Now().Add(31 * 24 * time.Hour)
		if _, err := create(r, nil, nil, ""); !errors.Is(err, ErrResellerExpiryRequired) {
			t.Fatalf("no expiry: %v", err)
		}
		if _, err := create(r, nil, &far, ""); !errors.Is(err, ErrResellerExpiryTooLong) {
			t.Fatalf("far expiry: %v", err)
		}
		a, err := create(r, nil, &ok, "")
		if err != nil {
			t.Fatal(err)
		}
		ns := ResellerNamespace(r.AdminID)
		if _, err := s.RenewAccount(ctx, a.ID, &ns, nil, &far); !errors.Is(err, ErrResellerExpiryTooLong) {
			t.Fatalf("renew past plan: %v", err)
		}
		if _, err := s.UpdateAccount(ctx, a.ID, &ns, UpdateAccountParams{ExpiryAt: &far}); !errors.Is(err, ErrResellerExpiryTooLong) {
			t.Fatalf("update past plan: %v", err)
		}
		// Plan ends: creates are refused as expired, existing accounts keep working.
		past := time.Now().Add(-time.Minute)
		if err := s.UpdateResellerLimits(ctx, r.AdminID, ResellerLimitsPatch{ExpiresAt: Opt[time.Time]{Set: true, Value: &past}}); err != nil {
			t.Fatal(err)
		}
		if _, err := create(r, nil, &ok, ""); !errors.Is(err, ErrResellerExpired) {
			t.Fatalf("expired create: %v", err)
		}
		if got, _ := s.GetAccount(ctx, a.ID, &ns); got.Status != "active" {
			t.Fatalf("expired reseller's account status = %s", got.Status)
		}
		// null = never expires again.
		if err := s.UpdateResellerLimits(ctx, r.AdminID, ResellerLimitsPatch{ExpiresAt: Opt[time.Time]{Set: true}}); err != nil {
			t.Fatal(err)
		}
		if _, err := create(r, nil, nil, ""); err != nil {
			t.Fatalf("after clearing expiry: %v", err)
		}
	})

	t.Run("node groups", func(t *testing.T) {
		r := mk("groups", NewResellerParams{NodeGroups: []string{"eu-" + suffix}})
		if _, err := create(r, nil, nil, other.ID); !errors.Is(err, ErrResellerNodeGroupNotAllowed) {
			t.Fatalf("pinned foreign group: %v", err)
		}
		a, err := create(r, nil, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		peers, _ := s.ListAccountPeersWithNode(ctx, a.ID, nil)
		if len(peers) != 1 || peers[0].NodeID != node.ID {
			t.Fatalf("fan-out ignored node_groups: %+v", peers)
		}
	})

	t.Run("namespace isolation and disabled", func(t *testing.T) {
		r1 := mk("iso1", NewResellerParams{})
		r2 := mk("iso2", NewResellerParams{})
		a, _ := create(r1, nil, nil, "")
		ns2 := ResellerNamespace(r2.AdminID)
		if _, err := s.GetAccount(ctx, a.ID, &ns2); !errors.Is(err, ErrAccountNotFound) {
			t.Fatalf("cross-namespace get: %v", err)
		}
		off := true
		if err := s.UpdateResellerLimits(ctx, r1.AdminID, ResellerLimitsPatch{Disabled: &off}); err != nil {
			t.Fatal(err)
		}
		if _, err := create(r1, nil, nil, ""); !errors.Is(err, ErrResellerDisabled) {
			t.Fatalf("disabled create: %v", err)
		}
	})

	t.Run("bulk suspend and reactivate", func(t *testing.T) {
		r := mk("bulk", NewResellerParams{})
		ns := ResellerNamespace(r.AdminID)
		a1, _ := create(r, nil, nil, "")
		a2, _ := create(r, nil, nil, "")
		if _, err := s.SuspendAccount(ctx, a2.ID, &ns, "manual"); err != nil {
			t.Fatal(err)
		}
		if c, err := s.SuspendResellerAccounts(ctx, r.AdminID); err != nil || c != 1 {
			t.Fatalf("suspended %d, %v", c, err)
		}
		if _, err := s.EnableAccount(ctx, a1.ID, &ns); !errors.Is(err, ErrAccountLockedByAdmin) {
			t.Fatalf("reseller re-enable: %v", err)
		}
		if re, still, err := s.ReactivateResellerAccounts(ctx, r.AdminID); err != nil || re != 1 || still != 0 {
			t.Fatalf("reactivated %d/%d, %v", re, still, err)
		}
		got, _ := s.GetAccount(ctx, a2.ID, &ns)
		if got.Status != "suspended" {
			t.Fatalf("manually suspended account was reactivated: %s", got.Status)
		}
	})

	t.Run("delete refused while accounts exist", func(t *testing.T) {
		r := mk("del", NewResellerParams{})
		a, _ := create(r, nil, nil, "")
		if err := s.DeleteReseller(ctx, r.AdminID); !errors.Is(err, ErrResellerHasAccounts) {
			t.Fatalf("delete: %v", err)
		}
		ns := ResellerNamespace(r.AdminID)
		_, _ = s.SoftDeleteAccount(ctx, a.ID, &ns)
		if err := s.DeleteReseller(ctx, r.AdminID); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("admin list hides resellers", func(t *testing.T) {
		admins, _ := s.ListAdmins(ctx)
		for _, a := range admins {
			if a.Role == "reseller" {
				t.Fatalf("reseller %s listed", a.Username)
			}
		}
	})

	t.Run("concurrent creates cannot overshoot", func(t *testing.T) {
		r := mk("race", NewResellerParams{MaxAccounts: ip(5), DataLimitBytes: i64(500)})
		var wg sync.WaitGroup
		var mu sync.Mutex
		okCount, limited := 0, 0
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := create(r, i64(100), nil, "")
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err == nil:
					okCount++
				case errors.Is(err, ErrResellerAccountLimit), errors.Is(err, ErrResellerDataCredit):
					limited++
				default:
					t.Errorf("unexpected: %v", err)
				}
			}()
		}
		wg.Wait()
		got, _ := s.GetReseller(ctx, r.AdminID)
		if okCount != 5 || got.Usage.AccountsUsed != 5 || got.Usage.DataAllocatedBytes != 500 || limited != 15 {
			t.Fatalf("ok=%d limited=%d usage=%+v", okCount, limited, got.Usage)
		}
	})
}
