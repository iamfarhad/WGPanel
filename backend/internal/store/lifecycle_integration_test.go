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

// TestAccountLifecycleIntegration covers migration 0025: on-hold activation (first
// handshake and deadline), scheduled usage resets (including that they never touch a
// reseller's allocation-based credit), and plan templates. Skipped without
// WGPANEL_TEST_POSTGRES_DSN. Cleans up everything it creates.
func TestAccountLifecycleIntegration(t *testing.T) {
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
	group := "life-" + sfx
	var namespaces, adminIDs, nodeIDs []string
	t.Cleanup(func() {
		bg := context.Background()
		for _, ns := range namespaces {
			_, _ = s.pool.Exec(bg, `DELETE FROM plan_templates WHERE owner_namespace = $1`, ns)
			_, _ = s.pool.Exec(bg, `DELETE FROM accounts WHERE owner_key_namespace = $1`, ns)
		}
		_, _ = s.pool.Exec(bg, `DELETE FROM plan_templates WHERE name LIKE $1`, "%"+sfx)
		for _, id := range adminIDs {
			_, _ = s.pool.Exec(bg, `DELETE FROM admins WHERE id = $1`, id)
		}
		for _, id := range nodeIDs {
			_, _ = s.pool.Exec(bg, `DELETE FROM nodes WHERE id = $1`, id)
		}
	})

	key := "pk-node-life" + sfx
	node, err := s.CreateNode(ctx, "life-"+sfx, group, "eu", "life.test:51820", "10.88.0.0/16", 500, &key)
	if err != nil {
		t.Fatal(err)
	}
	nodeIDs = append(nodeIDs, node.ID)
	if _, err := s.pool.Exec(ctx, `UPDATE nodes SET status = 'online' WHERE id = $1`, node.ID); err != nil {
		t.Fatal(err)
	}

	var seq atomic.Int64
	newNS := func() *string {
		ns := fmt.Sprintf("life-ns-%s-%d", sfx, seq.Add(1))
		namespaces = append(namespaces, ns)
		return &ns
	}
	mk := func(t *testing.T, ns *string, mod func(*CreateAccountParams)) (Account, error) {
		t.Helper()
		n := seq.Add(1)
		p := CreateAccountParams{
			Label: fmt.Sprintf("acct-%d", n), PublicKey: fmt.Sprintf("pk-%s-%d", sfx, n), PrivateKeyEncrypted: "x",
			SubscriptionToken: fmt.Sprintf("tok-%s-%d", sfx, n), OwnerKeyNamespace: ns, AllowedNodeGroups: []string{group},
		}
		if mod != nil {
			mod(&p)
		}
		return s.CreateAccount(ctx, p)
	}
	must := func(t *testing.T, ns *string, mod func(*CreateAccountParams)) Account {
		t.Helper()
		a, err := mk(t, ns, mod)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	get := func(t *testing.T, id string) Account {
		t.Helper()
		a, err := s.GetAccount(ctx, id, nil)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	days := func(n int) *int { return &n }

	t.Run("on hold: created without expiry, first handshake starts the clock", func(t *testing.T) {
		ns := newNS()
		a := must(t, ns, func(p *CreateAccountParams) { p.HoldDurationDays = days(30); p.ResetStrategy = "week" })
		if !a.OnHold() || a.ExpiryAt != nil || *a.HoldDurationDays != 30 || a.Status != "active" {
			t.Fatalf("not on hold: %+v", a)
		}
		firstReset := a.NextResetAt

		// A heartbeat without a handshake leaves it on hold.
		if err := s.IngestHeartbeatTelemetry(ctx, node.ID, []PeerTrafficReport{{PublicKey: a.PublicKey, ReceiveBytes: 1, TransmitBytes: 1}}, nil); err != nil {
			t.Fatal(err)
		}
		if !get(t, a.ID).OnHold() {
			t.Fatal("activated without a handshake")
		}

		hs := time.Now()
		if err := s.IngestHeartbeatTelemetry(ctx, node.ID, []PeerTrafficReport{{PublicKey: a.PublicKey, ReceiveBytes: 2, TransmitBytes: 2, LastHandshake: &hs}}, nil); err != nil {
			t.Fatal(err)
		}
		got := get(t, a.ID)
		if got.OnHold() || got.HoldDurationDays != nil || got.ActivatedAt == nil || got.ExpiryAt == nil {
			t.Fatalf("not activated: %+v", got)
		}
		if d := time.Until(*got.ExpiryAt); d < 29*24*time.Hour+23*time.Hour || d > 30*24*time.Hour+time.Hour {
			t.Fatalf("expiry %v is not ~30 days out", got.ExpiryAt)
		}
		if got.NextResetAt == nil || !got.NextResetAt.After(*firstReset) {
			t.Fatalf("reset schedule should restart at activation: %v vs %v", got.NextResetAt, firstReset)
		}
		var n int
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE target = $1 AND action = 'account.activated'`, a.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("activation audit rows = %d, want 1", n)
		}
		// Idempotent: another handshake must not move the expiry.
		hs2 := time.Now().Add(time.Minute)
		if err := s.IngestHeartbeatTelemetry(ctx, node.ID, []PeerTrafficReport{{PublicKey: a.PublicKey, ReceiveBytes: 3, TransmitBytes: 3, LastHandshake: &hs2}}, nil); err != nil {
			t.Fatal(err)
		}
		if again := get(t, a.ID); !again.ExpiryAt.Equal(*got.ExpiryAt) {
			t.Fatal("expiry moved on a later handshake")
		}
	})

	t.Run("on hold: deadline activates anyway", func(t *testing.T) {
		ns := newNS()
		past := time.Now().Add(-time.Hour)
		future := time.Now().Add(48 * time.Hour)
		due := must(t, ns, func(p *CreateAccountParams) { p.HoldDurationDays = days(7); p.HoldDeadlineAt = &past })
		later := must(t, ns, func(p *CreateAccountParams) { p.HoldDurationDays = days(7); p.HoldDeadlineAt = &future })
		noDeadline := must(t, ns, func(p *CreateAccountParams) { p.HoldDurationDays = days(7) })
		if _, err := s.ActivateOverdueHolds(ctx); err != nil {
			t.Fatal(err)
		}
		if got := get(t, due.ID); got.OnHold() || got.ExpiryAt == nil {
			t.Fatalf("overdue hold not activated: %+v", got)
		}
		if !get(t, later.ID).OnHold() || !get(t, noDeadline.ID).OnHold() {
			t.Fatal("accounts with a future/no deadline must stay on hold")
		}
	})

	t.Run("on hold: renew extends the duration, patch with expiry activates, validation", func(t *testing.T) {
		ns := newNS()
		a := must(t, ns, func(p *CreateAccountParams) { p.HoldDurationDays = days(10) })
		got, err := s.RenewAccountHold(ctx, a.ID, ns, nil, nil, days(5))
		if err != nil || !got.OnHold() || *got.HoldDurationDays != 15 || got.ExpiryAt != nil {
			t.Fatalf("renew: %v %+v", err, got)
		}
		got, err = s.UpdateAccount(ctx, a.ID, ns, UpdateAccountParams{HoldDurationDays: days(20)})
		if err != nil || *got.HoldDurationDays != 20 {
			t.Fatalf("edit hold days: %v %+v", err, got)
		}
		exp := time.Now().Add(72 * time.Hour)
		got, err = s.UpdateAccount(ctx, a.ID, ns, UpdateAccountParams{ExpiryAt: &exp})
		if err != nil || got.OnHold() || got.ExpiryAt == nil || got.ActivatedAt == nil {
			t.Fatalf("fixed expiry should activate: %v %+v", err, got)
		}

		if _, err := mk(t, ns, func(p *CreateAccountParams) { p.HoldDurationDays = days(5); p.ExpiryAt = &exp }); !errors.Is(err, ErrHoldWithExpiry) {
			t.Fatalf("hold+expiry: %v", err)
		}
		if _, err := mk(t, ns, func(p *CreateAccountParams) { p.ResetStrategy = "hourly" }); !errors.Is(err, ErrInvalidResetStrategy) {
			t.Fatalf("bad strategy: %v", err)
		}
	})

	t.Run("on hold: reseller plan expiry is enforced and clamps activation", func(t *testing.T) {
		planEnd := time.Now().Add(10 * 24 * time.Hour)
		r, err := s.CreateReseller(ctx, NewResellerParams{Username: "life-plan-" + sfx, PasswordHash: "h", ExpiresAt: &planEnd})
		if err != nil {
			t.Fatal(err)
		}
		adminIDs = append(adminIDs, r.AdminID)
		ns := ResellerNamespace(r.AdminID)
		namespaces = append(namespaces, ns)
		if _, err := mk(t, &ns, func(p *CreateAccountParams) { p.HoldDurationDays = days(30) }); !errors.Is(err, ErrResellerExpiryTooLong) {
			t.Fatalf("30-day hold beyond a 10-day plan: %v", err)
		}
		a, err := mk(t, &ns, func(p *CreateAccountParams) { p.HoldDurationDays = days(9) })
		if err != nil {
			t.Fatal(err)
		}
		// Push the plan end closer, then activate: the expiry must be clamped to it.
		clamp := time.Now().Add(2 * 24 * time.Hour)
		if err := s.UpdateResellerLimits(ctx, r.AdminID, ResellerLimitsPatch{ExpiresAt: Opt[time.Time]{Set: true, Value: &clamp}}); err != nil {
			t.Fatal(err)
		}
		hs := time.Now()
		if err := s.IngestHeartbeatTelemetry(ctx, node.ID, []PeerTrafficReport{{PublicKey: a.PublicKey, LastHandshake: &hs}}, nil); err != nil {
			t.Fatal(err)
		}
		got := get(t, a.ID)
		if got.ExpiryAt == nil || got.ExpiryAt.After(clamp.Add(time.Second)) {
			t.Fatalf("expiry %v not clamped to plan end %v", got.ExpiryAt, clamp)
		}
	})

	t.Run("scheduled reset: same logic as manual reset, schedule advances", func(t *testing.T) {
		ns := newNS()
		quota := int64(1000)
		a := must(t, ns, func(p *CreateAccountParams) { p.DataQuotaBytes = &quota; p.ResetStrategy = "day" })
		if a.ResetStrategy != "day" || a.NextResetAt == nil || time.Until(*a.NextResetAt) < 23*time.Hour {
			t.Fatalf("schedule not set: %+v", a)
		}
		// Used up and suspended for quota, and due now.
		if _, err := s.pool.Exec(ctx, `UPDATE accounts SET data_used_bytes = 1000, status = 'suspended', suspend_reason = 'quota_exceeded', next_reset_at = now() - interval '1 minute' WHERE id = $1`, a.ID); err != nil {
			t.Fatal(err)
		}
		// A manually suspended one must keep its suspension.
		manual := must(t, ns, func(p *CreateAccountParams) { p.DataQuotaBytes = &quota; p.ResetStrategy = "day" })
		if _, err := s.pool.Exec(ctx, `UPDATE accounts SET data_used_bytes = 500, status = 'suspended', suspend_reason = 'manual', next_reset_at = now() - interval '1 minute' WHERE id = $1`, manual.ID); err != nil {
			t.Fatal(err)
		}
		// Not due: untouched.
		notDue := must(t, ns, func(p *CreateAccountParams) { p.DataQuotaBytes = &quota; p.ResetStrategy = "day" })
		if _, err := s.pool.Exec(ctx, `UPDATE accounts SET data_used_bytes = 7 WHERE id = $1`, notDue.ID); err != nil {
			t.Fatal(err)
		}

		resets, err := s.ResetDueUsage(ctx, 1000)
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]ScheduledReset{}
		for _, r := range resets {
			seen[r.AccountID] = r
		}
		if r, ok := seen[a.ID]; !ok || r.PreviousUsedBytes != 1000 {
			t.Fatalf("account not reset: %+v", seen[a.ID])
		}
		if _, ok := seen[notDue.ID]; ok {
			t.Fatal("not-due account was reset")
		}
		got := get(t, a.ID)
		if got.DataUsedBytes != 0 || got.Status != "active" || got.SuspendReason != nil || got.LastResetAt == nil {
			t.Fatalf("after reset: %+v", got)
		}
		if got.NextResetAt == nil || time.Until(*got.NextResetAt) < 23*time.Hour {
			t.Fatalf("next_reset_at not advanced: %v", got.NextResetAt)
		}
		if m := get(t, manual.ID); m.DataUsedBytes != 0 || m.Status != "suspended" || *m.SuspendReason != "manual" {
			t.Fatalf("manual suspension must survive: %+v", m)
		}
		if n := get(t, notDue.ID); n.DataUsedBytes != 7 {
			t.Fatalf("not-due usage changed: %d", n.DataUsedBytes)
		}
		// Second sweep finds nothing due for these accounts.
		again, err := s.ResetDueUsage(ctx, 1000)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range again {
			if r.AccountID == a.ID || r.AccountID == manual.ID {
				t.Fatal("reset twice")
			}
		}
	})

	t.Run("scheduled reset: skips accounts on hold and a long outage fires once", func(t *testing.T) {
		ns := newNS()
		held := must(t, ns, func(p *CreateAccountParams) { p.HoldDurationDays = days(3); p.ResetStrategy = "day" })
		if _, err := s.pool.Exec(ctx, `UPDATE accounts SET next_reset_at = now() - interval '1 hour' WHERE id = $1`, held.ID); err != nil {
			t.Fatal(err)
		}
		late := must(t, ns, func(p *CreateAccountParams) { p.ResetStrategy = "day" })
		if _, err := s.pool.Exec(ctx, `UPDATE accounts SET next_reset_at = now() - interval '10 days' WHERE id = $1`, late.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ResetDueUsage(ctx, 1000); err != nil {
			t.Fatal(err)
		}
		if r, _ := s.ResetDueUsage(ctx, 1000); len(r) > 0 {
			for _, x := range r {
				if x.AccountID == held.ID || x.AccountID == late.ID {
					t.Fatal("fired again")
				}
			}
		}
		if got := get(t, late.ID); got.NextResetAt == nil || !got.NextResetAt.After(time.Now()) {
			t.Fatalf("outage catch-up left next_reset_at in the past: %v", got.NextResetAt)
		}
		if got := get(t, held.ID); got.LastResetAt != nil {
			t.Fatal("on-hold account was reset")
		}
	})

	t.Run("scheduled reset never double-charges a reseller's credit", func(t *testing.T) {
		credit := int64(10_000)
		r, err := s.CreateReseller(ctx, NewResellerParams{Username: "life-credit-" + sfx, PasswordHash: "h", DataLimitBytes: &credit})
		if err != nil {
			t.Fatal(err)
		}
		adminIDs = append(adminIDs, r.AdminID)
		ns := ResellerNamespace(r.AdminID)
		namespaces = append(namespaces, ns)
		q := int64(4000)
		a, err := mk(t, &ns, func(p *CreateAccountParams) { p.DataQuotaBytes = &q; p.ResetStrategy = "month" })
		if err != nil {
			t.Fatal(err)
		}
		allocated := func() int64 {
			var sum int64
			if err := s.pool.QueryRow(ctx, `SELECT COALESCE(sum(data_quota_bytes),0) FROM accounts WHERE owner_key_namespace = $1 AND status != 'deleted'`, ns).Scan(&sum); err != nil {
				t.Fatal(err)
			}
			return sum
		}
		if allocated() != 4000 {
			t.Fatalf("allocated = %d", allocated())
		}
		// The manual endpoint is refused for a credit-limited reseller...
		if _, err := s.ResetAccountUsage(ctx, a.ID, &ns); !errors.Is(err, ErrResellerResetNotAllowed) {
			t.Fatalf("manual reset by reseller: %v", err)
		}
		// ...but the scheduled one (the plan the account was sold with) runs, and
		// leaves the allocation - what the credit counts - exactly as it was.
		for i := 0; i < 3; i++ {
			if _, err := s.pool.Exec(ctx, `UPDATE accounts SET data_used_bytes = 3000, next_reset_at = now() - interval '1 minute' WHERE id = $1`, a.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ResetDueUsage(ctx, 1000); err != nil {
				t.Fatal(err)
			}
			if got := get(t, a.ID); got.DataUsedBytes != 0 || got.DataQuotaBytes == nil || *got.DataQuotaBytes != 4000 {
				t.Fatalf("cycle %d: %+v", i, got)
			}
			if allocated() != 4000 {
				t.Fatalf("cycle %d: credit allocation changed to %d", i, allocated())
			}
		}
		// The remaining credit is still exactly credit - allocation: 6000 more fits, 6001 not.
		over := int64(6001)
		if _, err := mk(t, &ns, func(p *CreateAccountParams) { p.DataQuotaBytes = &over }); !errors.Is(err, ErrResellerDataCredit) {
			t.Fatalf("over credit: %v", err)
		}
		fits := int64(6000)
		if _, err := mk(t, &ns, func(p *CreateAccountParams) { p.DataQuotaBytes = &fits }); err != nil {
			t.Fatalf("credit unexpectedly consumed by resets: %v", err)
		}
	})

	t.Run("update: reset_strategy sets and clears the schedule", func(t *testing.T) {
		ns := newNS()
		a := must(t, ns, nil)
		week := "week"
		got, err := s.UpdateAccount(ctx, a.ID, ns, UpdateAccountParams{ResetStrategy: &week})
		if err != nil || got.ResetStrategy != "week" || got.NextResetAt == nil || time.Until(*got.NextResetAt) < 6*24*time.Hour {
			t.Fatalf("set: %v %+v", err, got)
		}
		none := "none"
		got, err = s.UpdateAccount(ctx, a.ID, ns, UpdateAccountParams{ResetStrategy: &none})
		if err != nil || got.ResetStrategy != "none" || got.NextResetAt != nil {
			t.Fatalf("clear: %v %+v", err, got)
		}
		bad := "hourly"
		if _, err := s.UpdateAccount(ctx, a.ID, ns, UpdateAccountParams{ResetStrategy: &bad}); !errors.Is(err, ErrInvalidResetStrategy) {
			t.Fatalf("bad: %v", err)
		}
	})

	t.Run("reports count on-hold accounts without changing active", func(t *testing.T) {
		ns := newNS()
		must(t, ns, func(p *CreateAccountParams) { p.HoldDurationDays = days(5) })
		must(t, ns, nil)
		rep, err := s.BuildReport(ctx, ReportParams{RangeKey: "7d", CallerNamespace: ns})
		if err != nil {
			t.Fatal(err)
		}
		if rep.Totals.AccountsOnHold != 1 || rep.Totals.AccountsActive != 2 || rep.Totals.AccountsTotal != 2 {
			t.Fatalf("totals: %+v", rep.Totals)
		}
		onHold := 0
		for _, b := range rep.ExpiryBuckets {
			if b.Bucket == "on_hold" {
				onHold = b.Count
			}
		}
		if onHold != 1 {
			t.Fatalf("expiry buckets: %+v", rep.ExpiryBuckets)
		}
	})

	t.Run("templates: scoping and CRUD", func(t *testing.T) {
		resNS := newNS()
		otherNS := newNS()
		quota := int64(5_000_000_000)
		base := TemplateParams{Name: "t-" + sfx, DataQuotaBytes: &quota, DurationDays: days(30), ResetStrategy: "month", OnHold: true}

		shared, err := s.CreateTemplate(ctx, nil, TemplateParams{Name: "shared-" + sfx, AvailableToResellers: true, ResetStrategy: "none"})
		if err != nil {
			t.Fatal(err)
		}
		private, err := s.CreateTemplate(ctx, nil, TemplateParams{Name: "staff-only-" + sfx, ResetStrategy: "none"})
		if err != nil {
			t.Fatal(err)
		}
		own, err := s.CreateTemplate(ctx, resNS, base)
		if err != nil {
			t.Fatal(err)
		}
		// A reseller cannot flag its own template as shared.
		flagged, err := s.CreateTemplate(ctx, resNS, TemplateParams{Name: "flag-" + sfx, AvailableToResellers: true, ResetStrategy: "none"})
		if err != nil || flagged.AvailableToResellers {
			t.Fatalf("reseller template flagged shared: %v %+v", err, flagged)
		}

		ids := func(list []PlanTemplate) map[string]bool {
			m := map[string]bool{}
			for _, x := range list {
				m[x.ID] = true
			}
			return m
		}
		list, _ := s.ListTemplates(ctx, resNS)
		got := ids(list)
		if !got[shared.ID] || !got[own.ID] || got[private.ID] {
			t.Fatalf("reseller sees %v", got)
		}
		list, _ = s.ListTemplates(ctx, otherNS)
		got = ids(list)
		if !got[shared.ID] || got[own.ID] || got[private.ID] {
			t.Fatalf("other namespace sees %v", got)
		}
		list, _ = s.ListTemplates(ctx, nil)
		got = ids(list)
		if !got[shared.ID] || !got[own.ID] || !got[private.ID] {
			t.Fatalf("staff sees %v", got)
		}

		if _, err := s.GetTemplate(ctx, own.ID, otherNS); !errors.Is(err, ErrTemplateNotFound) {
			t.Fatalf("foreign get: %v", err)
		}
		if _, err := s.GetTemplate(ctx, private.ID, resNS); !errors.Is(err, ErrTemplateNotFound) {
			t.Fatalf("reseller get of staff-only: %v", err)
		}
		// A reseller may read but not edit/delete a shared staff template.
		if _, err := s.UpdateTemplate(ctx, shared.ID, resNS, base); !errors.Is(err, ErrTemplateNotFound) {
			t.Fatalf("reseller update of shared: %v", err)
		}
		if err := s.DeleteTemplate(ctx, shared.ID, resNS); !errors.Is(err, ErrTemplateNotFound) {
			t.Fatalf("reseller delete of shared: %v", err)
		}
		upd := base
		upd.Name = "renamed-" + sfx
		upd.OnHold = false
		u, err := s.UpdateTemplate(ctx, own.ID, resNS, upd)
		if err != nil || u.Name != upd.Name || u.OnHold {
			t.Fatalf("update: %v %+v", err, u)
		}
		if err := s.DeleteTemplate(ctx, own.ID, resNS); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteTemplate(ctx, own.ID, resNS); !errors.Is(err, ErrTemplateNotFound) {
			t.Fatalf("double delete: %v", err)
		}
		if _, err := s.CreateTemplate(ctx, nil, TemplateParams{Name: "bad-" + sfx, ResetStrategy: "hourly"}); !errors.Is(err, ErrInvalidResetStrategy) {
			t.Fatalf("bad strategy: %v", err)
		}
		if err := s.DeleteTemplate(ctx, private.ID, nil); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteTemplate(ctx, shared.ID, nil); err != nil {
			t.Fatal(err)
		}
	})
}
