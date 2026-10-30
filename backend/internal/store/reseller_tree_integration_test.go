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

// TestResellerTreeIntegration covers sub-resellers (migration 0032) against a real
// Postgres: carving limits (including under concurrency), the depth cap, cascading
// disable/expiry/suspend, subtree visibility and its limits, subtree delete, and the
// branding resolution order. Skipped without WGPANEL_TEST_POSTGRES_DSN.
func TestResellerTreeIntegration(t *testing.T) {
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
	nodeKey := "tk-" + suffix
	node, err := s.CreateNode(ctx, "tree-"+suffix, "tree-"+suffix, "eu", "vpn.tree:51820", "10.91.0.0/16", 900, &nodeKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE nodes SET status='online' WHERE id=$1`, node.ID); err != nil {
		t.Fatal(err)
	}
	// Other store tests assert how many online nodes exist; leave none behind.
	t.Cleanup(func() {
		_, _ = s.pool.Exec(context.Background(), `UPDATE nodes SET status='pending' WHERE id=$1`, node.ID)
	})

	const gb = int64(1 << 30)
	var seq atomic.Int64
	create := func(res Reseller, quota *int64, expiry *time.Time) (Account, error) {
		n := seq.Add(1)
		ns := ResellerNamespace(res.AdminID)
		return s.CreateAccount(ctx, CreateAccountParams{
			Label: "a", NodeIDOrAuto: node.ID, PublicKey: fmt.Sprintf("tpk-%s-%d", suffix, n),
			PrivateKeyEncrypted: "x", DataQuotaBytes: quota, ExpiryAt: expiry,
			SubscriptionToken: fmt.Sprintf("ttok-%s-%d", suffix, n), OwnerKeyNamespace: &ns,
		})
	}
	mkTry := func(name string, parent *Reseller, p NewResellerParams) (Reseller, error) {
		p.Username, p.PasswordHash = fmt.Sprintf("%s-%d-%s", name, seq.Add(1), suffix), "h"
		if parent != nil {
			p.ParentID, p.ByParent = &parent.AdminID, true
		}
		return s.CreateReseller(ctx, p)
	}
	mk := func(name string, parent *Reseller, p NewResellerParams) Reseller {
		r, err := mkTry(name, parent, p)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		return r
	}
	limits := func(t *testing.T, r Reseller) ResellerLimits {
		t.Helper()
		l, err := s.GetResellerLimits(ctx, r.AdminID)
		if err != nil {
			t.Fatal(err)
		}
		return l
	}

	t.Run("carving", func(t *testing.T) {
		root := mk("root", nil, NewResellerParams{MaxAccounts: ip(10), DataLimitBytes: i64(100 * gb)})
		mk("c1", &root, NewResellerParams{MaxAccounts: ip(4), DataLimitBytes: i64(40 * gb)})
		mk("c2", &root, NewResellerParams{MaxAccounts: ip(4), DataLimitBytes: i64(40 * gb)})

		if _, err := mkTry("c3", &root, NewResellerParams{MaxAccounts: ip(3), DataLimitBytes: i64(1 * gb)}); !errors.Is(err, ErrChildAccountAllocation) {
			t.Fatalf("accounts over-allocation: %v", err)
		}
		if _, err := mkTry("c3", &root, NewResellerParams{MaxAccounts: ip(1), DataLimitBytes: i64(30 * gb)}); !errors.Is(err, ErrChildCreditAllocation) {
			t.Fatalf("credit over-allocation: %v", err)
		}
		if _, err := mkTry("c3", &root, NewResellerParams{}); !errors.Is(err, ErrChildLimitRequired) {
			t.Fatalf("unlimited child of a limited parent: %v", err)
		}
		// The root's own accounts get what the children did not take: 2 slots, 20 GB.
		if _, err := create(root, i64(21*gb), nil); !errors.Is(err, ErrResellerDataCredit) {
			t.Fatalf("own account beyond unreserved credit: %v", err)
		}
		for i := 0; i < 2; i++ {
			if _, err := create(root, i64(5*gb), nil); err != nil {
				t.Fatalf("own account %d: %v", i, err)
			}
		}
		if _, err := create(root, i64(1*gb), nil); !errors.Is(err, ErrResellerAccountLimit) {
			t.Fatalf("own account beyond unreserved slots: %v", err)
		}
		// ...and the other way round: now that the root used its slots, no new child fits.
		if _, err := mkTry("c4", &root, NewResellerParams{MaxAccounts: ip(1), DataLimitBytes: i64(1 * gb)}); !errors.Is(err, ErrChildAccountAllocation) {
			t.Fatalf("child after own accounts used the slots: %v", err)
		}

		// Re-planning a child counts its current reservation only once.
		c1 := mk("c5", nil, NewResellerParams{MaxAccounts: ip(5), DataLimitBytes: i64(50 * gb)})
		kid := mk("k", &c1, NewResellerParams{MaxAccounts: ip(3), DataLimitBytes: i64(10 * gb)})
		if err := s.UpdateResellerLimits(ctx, kid.AdminID, ResellerLimitsPatch{MaxAccounts: Opt[int]{Set: true, Value: ip(5)}}); err != nil {
			t.Fatalf("raising a child to exactly the parent's size: %v", err)
		}
		if err := s.UpdateResellerLimits(ctx, kid.AdminID, ResellerLimitsPatch{MaxAccounts: Opt[int]{Set: true, Value: ip(6)}}); !errors.Is(err, ErrChildAccountAllocation) {
			t.Fatalf("raising a child past the parent: %v", err)
		}
		// Downward: the parent cannot drop below what it handed out.
		if err := s.UpdateResellerLimits(ctx, c1.AdminID, ResellerLimitsPatch{MaxAccounts: Opt[int]{Set: true, Value: ip(4)}}); !errors.Is(err, ErrResellerCarveConflict) {
			t.Fatalf("parent below its reservation: %v", err)
		}
		if err := s.UpdateResellerLimits(ctx, c1.AdminID, ResellerLimitsPatch{DataLimitBytes: Opt[int64]{Set: true}, MaxAccounts: Opt[int]{Set: true}}); err != nil {
			t.Fatalf("unlimiting a parent is always fine: %v", err)
		}
		// A limit-less parent allows limit-less children.
		mk("free", &c1, NewResellerParams{})
	})

	t.Run("concurrent_child_and_own_creates", func(t *testing.T) {
		root := mk("race", nil, NewResellerParams{MaxAccounts: ip(10), DataLimitBytes: i64(1000 * gb)})
		var wg sync.WaitGroup
		var okKids, okOwn atomic.Int64
		var unexpected atomic.Value
		for i := 0; i < 12; i++ {
			wg.Add(2)
			go func() {
				defer wg.Done()
				_, err := mkTry("rk", &root, NewResellerParams{MaxAccounts: ip(1), DataLimitBytes: i64(gb)})
				switch {
				case err == nil:
					okKids.Add(1)
				case !errors.Is(err, ErrChildAccountAllocation):
					unexpected.Store(err)
				}
			}()
			go func() {
				defer wg.Done()
				_, err := create(root, i64(gb), nil)
				switch {
				case err == nil:
					okOwn.Add(1)
				case !errors.Is(err, ErrResellerAccountLimit):
					unexpected.Store(err)
				}
			}()
		}
		wg.Wait()
		if v := unexpected.Load(); v != nil {
			t.Fatalf("unexpected error: %v", v)
		}
		if got := okKids.Load() + okOwn.Load(); got != 10 {
			t.Fatalf("children (%d) + own accounts (%d) = %d, want exactly the 10 slots", okKids.Load(), okOwn.Load(), got)
		}
		r, _ := s.GetReseller(ctx, root.AdminID)
		if r.Usage.AccountsUsed+r.ReservedAccounts != 10 {
			t.Fatalf("committed = %d own + %d reserved, want 10", r.Usage.AccountsUsed, r.ReservedAccounts)
		}
	})

	t.Run("depth_cap", func(t *testing.T) {
		a := mk("d1", nil, NewResellerParams{})
		b := mk("d2", &a, NewResellerParams{})
		c := mk("d3", &b, NewResellerParams{})
		if MaxResellerDepth != 3 || c.Depth() != 3 {
			t.Fatalf("depth constant %d, grandchild depth %d", MaxResellerDepth, c.Depth())
		}
		if _, err := mkTry("d4", &c, NewResellerParams{}); !errors.Is(err, ErrResellerTooDeep) {
			t.Fatalf("fourth level: %v", err)
		}
	})

	t.Run("expiry_and_groups", func(t *testing.T) {
		soon := time.Now().Add(30 * 24 * time.Hour)
		later := soon.Add(24 * time.Hour)
		root := mk("ex", nil, NewResellerParams{ExpiresAt: &soon, NodeGroups: []string{"tree-" + suffix, "g2"}})
		if _, err := mkTry("x", &root, NewResellerParams{ExpiresAt: &later}); !errors.Is(err, ErrChildExpiryTooLong) {
			t.Fatalf("child past parent: %v", err)
		}
		if _, err := mkTry("x", &root, NewResellerParams{}); !errors.Is(err, ErrChildExpiryRequired) {
			t.Fatalf("child with no expiry under an expiring parent: %v", err)
		}
		if _, err := mkTry("x", &root, NewResellerParams{ExpiresAt: &soon, NodeGroups: []string{"g3"}}); !errors.Is(err, ErrChildNodeGroupNotAllowed) {
			t.Fatalf("foreign group: %v", err)
		}
		kid := mk("x", &root, NewResellerParams{ExpiresAt: &soon})
		if got := limits(t, kid).NodeGroups; len(got) != 2 {
			t.Fatalf("child asking for all groups under a restricted parent should inherit them, got %v", got)
		}
		// Restricting a parent under a child that uses other groups is refused.
		open := mk("open", nil, NewResellerParams{})
		mk("openkid", &open, NewResellerParams{})
		if err := s.UpdateResellerLimits(ctx, open.AdminID, ResellerLimitsPatch{NodeGroups: &[]string{"g1"}}); !errors.Is(err, ErrResellerCarveConflict) {
			t.Fatalf("restricting a parent with an unrestricted child: %v", err)
		}
		// Accounts may not outlive the plan end up the chain.
		gk := mk("gk", &kid, NewResellerParams{ExpiresAt: &soon})
		if _, err := create(gk, nil, &later); !errors.Is(err, ErrResellerExpiryTooLong) {
			t.Fatalf("account beyond the chain's plan end: %v", err)
		}
		if _, err := create(gk, nil, &soon); err != nil {
			t.Fatalf("account within it: %v", err)
		}
	})

	t.Run("cascade_disable_expire_suspend", func(t *testing.T) {
		parent := mk("cp", nil, NewResellerParams{})
		child := mk("cc", &parent, NewResellerParams{})
		grand := mk("cg", &child, NewResellerParams{})
		sibling := mk("cs", &parent, NewResellerParams{})
		other := mk("co", nil, NewResellerParams{})
		accts := map[string]Account{}
		for name, r := range map[string]Reseller{"parent": parent, "child": child, "grand": grand, "sibling": sibling, "other": other} {
			a, err := create(r, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			accts[name] = a
		}

		// Disabling the parent blocks the whole subtree, not the unrelated reseller.
		off := true
		if err := s.UpdateResellerLimits(ctx, parent.AdminID, ResellerLimitsPatch{Disabled: &off}); err != nil {
			t.Fatal(err)
		}
		for name, r := range map[string]Reseller{"child": child, "grand": grand, "sibling": sibling} {
			if l := limits(t, r); !l.IsDisabled() || !errors.Is(l.blockedErr(), ErrResellerDisabled) {
				t.Fatalf("%s should be blocked by a disabled ancestor", name)
			}
			if _, err := create(r, nil, nil); !errors.Is(err, ErrResellerDisabled) {
				t.Fatalf("%s create under a disabled ancestor: %v", name, err)
			}
		}
		if l := limits(t, other); l.IsDisabled() {
			t.Fatal("unrelated reseller blocked")
		}
		on := false
		if err := s.UpdateResellerLimits(ctx, parent.AdminID, ResellerLimitsPatch{Disabled: &on}); err != nil {
			t.Fatal(err)
		}
		if _, err := create(grand, nil, nil); err != nil {
			t.Fatalf("re-enabling the parent should unblock the subtree: %v", err)
		}

		// Expiry cascades the same way.
		past := time.Now().Add(-time.Minute)
		if err := s.UpdateResellerLimits(ctx, parent.AdminID, ResellerLimitsPatch{ExpiresAt: Opt[time.Time]{Set: true, Value: &past}}); err != nil {
			t.Fatal(err)
		}
		if _, err := create(grand, nil, nil); !errors.Is(err, ErrResellerExpired) {
			t.Fatalf("grandchild under an expired ancestor: %v", err)
		}
		if err := s.UpdateResellerLimits(ctx, parent.AdminID, ResellerLimitsPatch{ExpiresAt: Opt[time.Time]{Set: true}}); err != nil {
			t.Fatal(err)
		}

		// Bulk suspend cascades to the subtree's accounts only.
		n, err := s.SuspendResellerAccounts(ctx, parent.AdminID)
		if err != nil || n != 5 {
			t.Fatalf("suspended %d (err %v), want parent+child+grand(2)+sibling = 5", n, err)
		}
		for name, a := range accts {
			got, _ := s.GetAccount(ctx, a.ID, nil)
			want := "suspended"
			if name == "other" {
				want = "active"
			}
			if got.Status != want {
				t.Fatalf("%s account is %s, want %s", name, got.Status, want)
			}
		}
		if re, _, err := s.ReactivateResellerAccounts(ctx, parent.AdminID); err != nil || re != 5 {
			t.Fatalf("reactivated %d (err %v), want 5", re, err)
		}

		// A parent's own bulk suspend of a child uses a reason the child cannot undo,
		// while the parent can lift it per account.
		if n, err := s.SuspendChildAccounts(ctx, child.AdminID); err != nil || n != 3 {
			t.Fatalf("child bulk suspend = %d (err %v), want child+grand(2) = 3", n, err)
		}
		childNS, parentNS := ResellerNamespace(child.AdminID), ResellerNamespace(parent.AdminID)
		if _, err := s.EnableAccount(ctx, accts["child"].ID, &childNS); !errors.Is(err, ErrAccountLockedByAdmin) {
			t.Fatalf("child undoing its parent's suspension: %v", err)
		}
		if _, err := s.EnableAccount(ctx, accts["child"].ID, &parentNS); err != nil {
			t.Fatalf("parent lifting its own suspension of a child account: %v", err)
		}
		// Grandchild account: suspended with the child's subtree; the child (an ancestor of
		// it) may lift it.
		if _, err := s.EnableAccount(ctx, accts["grand"].ID, &childNS); err != nil {
			t.Fatalf("ancestor enabling a descendant's account: %v", err)
		}

		// Staff disable cannot be lifted by the parent.
		if err := s.UpdateResellerLimits(ctx, child.AdminID, ResellerLimitsPatch{Disabled: &off}); err != nil {
			t.Fatal(err)
		}
		if err := s.UpdateResellerLimits(ctx, child.AdminID, ResellerLimitsPatch{Disabled: &on, ByParent: true}); !errors.Is(err, ErrChildLockedByAdmin) {
			t.Fatalf("parent lifting a staff disable: %v", err)
		}
		if err := s.UpdateResellerLimits(ctx, child.AdminID, ResellerLimitsPatch{Disabled: &on}); err != nil {
			t.Fatalf("staff lifting it: %v", err)
		}
		// Parent-initiated disable is liftable by the parent.
		if err := s.UpdateResellerLimits(ctx, child.AdminID, ResellerLimitsPatch{Disabled: &off, ByParent: true}); err != nil {
			t.Fatal(err)
		}
		if err := s.UpdateResellerLimits(ctx, child.AdminID, ResellerLimitsPatch{Disabled: &on, ByParent: true}); err != nil {
			t.Fatalf("parent re-enabling its own disable: %v", err)
		}
	})

	t.Run("subtree_visibility", func(t *testing.T) {
		parent := mk("vp", nil, NewResellerParams{})
		a := mk("va", &parent, NewResellerParams{})
		b := mk("vb", &parent, NewResellerParams{})
		grand := mk("vg", &a, NewResellerParams{})
		acct := map[string]Account{}
		ns := map[string]string{}
		for name, r := range map[string]Reseller{"parent": parent, "a": a, "b": b, "grand": grand} {
			x, err := create(r, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			acct[name], ns[name] = x, ResellerNamespace(r.AdminID)
		}
		visible := func(viewer, owner string) bool {
			v := ns[viewer]
			_, err := s.GetAccount(ctx, acct[owner].ID, &v)
			if err != nil && !errors.Is(err, ErrAccountNotFound) {
				t.Fatal(err)
			}
			return err == nil
		}
		want := map[string]map[string]bool{
			"parent": {"parent": true, "a": true, "b": true, "grand": true},
			"a":      {"parent": false, "a": true, "b": false, "grand": true},
			"b":      {"parent": false, "a": false, "b": true, "grand": false},
			"grand":  {"parent": false, "a": false, "b": false, "grand": true},
		}
		for viewer, row := range want {
			for owner, w := range row {
				if got := visible(viewer, owner); got != w {
					t.Errorf("%s sees %s's account = %v, want %v", viewer, owner, got, w)
				}
			}
		}
		// Lists are scoped the same way.
		pns := ns["parent"]
		list, err := s.ListAccounts(ctx, 100, 0, &pns, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 4 {
			t.Fatalf("parent lists %d accounts, want 4", len(list))
		}
		bns := ns["b"]
		if list, _ = s.ListAccounts(ctx, 100, 0, &bns, ""); len(list) != 1 {
			t.Fatalf("leaf lists %d accounts, want only its own", len(list))
		}
		// Writes stay strict: a parent may suspend/enable a descendant's account but not
		// edit, renew or delete it.
		if _, err := s.SuspendAccount(ctx, acct["grand"].ID, &pns, "manual"); err != nil {
			t.Fatalf("parent suspend of a grandchild's account: %v", err)
		}
		if _, err := s.EnableAccount(ctx, acct["grand"].ID, &pns); err != nil {
			t.Fatalf("parent enable: %v", err)
		}
		lbl := "x"
		if _, err := s.UpdateAccount(ctx, acct["grand"].ID, &pns, UpdateAccountParams{Label: &lbl}); !errors.Is(err, ErrAccountNotFound) {
			t.Fatalf("parent update of a descendant's account: %v", err)
		}
		if _, err := s.SoftDeleteAccount(ctx, acct["grand"].ID, &pns); !errors.Is(err, ErrAccountNotFound) {
			t.Fatalf("parent delete of a descendant's account: %v", err)
		}
		if _, err := s.GetAccountPrivateKey(ctx, acct["grand"].ID, &pns); !errors.Is(err, ErrAccountNotFound) {
			t.Fatalf("parent config download of a descendant's account: %v", err)
		}
		// A child cannot suspend its parent's or sibling's accounts.
		ans := ns["a"]
		for _, victim := range []string{"parent", "b"} {
			if _, err := s.SuspendAccount(ctx, acct[victim].ID, &ans, "manual"); !errors.Is(err, ErrAccountNotFound) {
				t.Fatalf("a suspended %s's account: %v", victim, err)
			}
		}
		// GetDescendant: strictly below only.
		if _, err := s.GetDescendant(ctx, a.AdminID, grand.AdminID); err != nil {
			t.Fatalf("descendant lookup: %v", err)
		}
		for _, bad := range []Reseller{parent, a, b} {
			if _, err := s.GetDescendant(ctx, a.AdminID, bad.AdminID); !errors.Is(err, ErrNotDescendant) {
				t.Fatalf("GetDescendant(a, %s) = %v, want ErrNotDescendant", bad.Username, err)
			}
		}
		if got, _ := s.ListDescendants(ctx, parent.AdminID); len(got) != 3 {
			t.Fatalf("parent has %d descendants, want 3", len(got))
		}
	})

	t.Run("delete_subtree", func(t *testing.T) {
		root := mk("dr", nil, NewResellerParams{})
		kid := mk("dk", &root, NewResellerParams{})
		grand := mk("dg", &kid, NewResellerParams{})
		if err := s.DeleteReseller(ctx, root.AdminID); !errors.Is(err, ErrResellerHasChildren) {
			t.Fatalf("delete with children: %v", err)
		}
		acct, err := create(grand, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.DeleteResellerTree(ctx, root.AdminID, true); !errors.Is(err, ErrResellerHasAccounts) {
			t.Fatalf("cascade over a grandchild with accounts: %v", err)
		}
		gns := ResellerNamespace(grand.AdminID)
		if _, err := s.SoftDeleteAccount(ctx, acct.ID, &gns); err != nil {
			t.Fatal(err)
		}
		n, err := s.DeleteResellerTree(ctx, root.AdminID, true)
		if err != nil || n != 3 {
			t.Fatalf("cascade delete removed %d (err %v), want 3", n, err)
		}
		for _, r := range []Reseller{root, kid, grand} {
			if _, err := s.GetResellerLimits(ctx, r.AdminID); !errors.Is(err, ErrResellerNotFound) {
				t.Fatalf("%s survived: %v", r.Username, err)
			}
		}
	})

	t.Run("branding_resolution", func(t *testing.T) {
		clearGlobal := func() {
			if _, err := s.pool.Exec(ctx, `DELETE FROM brand_profiles WHERE admin_id IS NULL`); err != nil {
				t.Fatal(err)
			}
			_, _ = s.pool.Exec(ctx, `UPDATE panel_settings SET support_contact = NULL WHERE id = 1`)
		}
		clearGlobal()
		defer clearGlobal()

		top := mk("bt", nil, NewResellerParams{})
		mid := mk("bm", &top, NewResellerParams{})
		leaf := mk("bl", &mid, NewResellerParams{})
		bare := mk("bb", nil, NewResellerParams{})
		resolve := func(r Reseller) (string, string) {
			b, src, err := s.ResolveBrand(ctx, ResellerNamespace(r.AdminID))
			if err != nil {
				t.Fatal(err)
			}
			return b.BrandName, src
		}
		save := func(id *string, name string) {
			if _, err := s.SaveBrand(ctx, id, BrandUpdate{BrandName: name}); err != nil {
				t.Fatal(err)
			}
		}

		if name, src := resolve(leaf); name != "" || src != BrandSourceGlobal {
			t.Fatalf("nothing configured: %q/%s", name, src)
		}
		save(nil, "Global")
		if name, src := resolve(leaf); name != "Global" || src != BrandSourceGlobal {
			t.Fatalf("global fallback: %q/%s", name, src)
		}
		save(&top.AdminID, "Top")
		if name, src := resolve(leaf); name != "Top" || src != BrandSourceReseller {
			t.Fatalf("grandparent brand: %q/%s", name, src)
		}
		save(&mid.AdminID, "Mid")
		if name, _ := resolve(leaf); name != "Mid" {
			t.Fatalf("nearest ancestor should win: %q", name)
		}
		save(&leaf.AdminID, "Leaf")
		if name, _ := resolve(leaf); name != "Leaf" {
			t.Fatalf("own brand should win: %q", name)
		}
		// A saved-but-blank profile is not branding: it inherits.
		if _, err := s.SaveBrand(ctx, &leaf.AdminID, BrandUpdate{}); err != nil {
			t.Fatal(err)
		}
		if name, _ := resolve(leaf); name != "Mid" {
			t.Fatalf("blank profile should fall through to the parent: %q", name)
		}
		// Siblings and unrelated resellers never inherit another branch.
		if name, src := resolve(bare); name != "Global" || src != BrandSourceGlobal {
			t.Fatalf("unrelated reseller: %q/%s", name, src)
		}
		// Admin-owned (no namespace) and API-key namespaces use the global profile.
		for _, ns := range []string{"", "abcdef0123456789abcdef0123456789"} {
			if b, src, _ := s.ResolveBrand(ctx, ns); b.BrandName != "Global" || src != BrandSourceGlobal {
				t.Fatalf("namespace %q: %q/%s", ns, b.BrandName, src)
			}
		}
		// Deleting a reseller deletes its profile (FK cascade).
		throwaway := mk("bx", nil, NewResellerParams{})
		save(&throwaway.AdminID, "Gone")
		if _, err := s.DeleteResellerTree(ctx, throwaway.AdminID, false); err != nil {
			t.Fatal(err)
		}
		var n int
		_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM brand_profiles WHERE admin_id = $1`, throwaway.AdminID).Scan(&n)
		if n != 0 {
			t.Fatal("brand profile outlived its reseller")
		}
		// Legacy panel_settings.support_contact still feeds the global support link.
		clearGlobal()
		if _, err := s.pool.Exec(ctx, `UPDATE panel_settings SET support_contact = '@jirjirak_support' WHERE id = 1`); err != nil {
			t.Fatal(err)
		}
		if b, _, _ := s.ResolveBrand(ctx, ResellerNamespace(bare.AdminID)); b.SupportLink != "@jirjirak_support" {
			t.Fatalf("legacy support contact not used: %q", b.SupportLink)
		}
	})

	t.Run("branding_validation", func(t *testing.T) {
		png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
		if ct, err := ValidateLogo(png); err != nil || ct != "image/png" {
			t.Fatalf("png: %q %v", ct, err)
		}
		for name, data := range map[string][]byte{
			"svg":   []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`),
			"html":  []byte("<html></html>"),
			"empty": nil,
			"big":   append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, MaxLogoBytes)...),
		} {
			if _, err := ValidateLogo(data); err == nil {
				t.Errorf("%s logo accepted", name)
			}
		}
		for in, want := range map[string]string{
			"@my_support": "@my_support", "my_support": "@my_support", "t.me/my_support": "https://t.me/my_support",
			"https://example.com/help": "https://example.com/help", "": "",
		} {
			if got, err := NormalizeSupportLink(in); err != nil || got != want {
				t.Errorf("NormalizeSupportLink(%q) = %q, %v; want %q", in, got, err, want)
			}
		}
		for _, bad := range []string{"javascript:alert(1)", "http://insecure.example", "data:text/html,x", "@ab", "https://user:pw@host.example", "ftp://x.example/y"} {
			if _, err := NormalizeSupportLink(bad); err == nil {
				t.Errorf("support link %q accepted", bad)
			}
		}
		b := BrandProfile{AccentColor: "red"}
		if err := b.Normalize(); err == nil {
			t.Error("bad accent color accepted")
		}
		b = BrandProfile{LogoURL: "http://x.example/l.png"}
		if err := b.Normalize(); err == nil {
			t.Error("http logo URL accepted")
		}
	})
}
