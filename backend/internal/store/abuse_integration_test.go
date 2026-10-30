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

// TestAbuseControlsIntegration covers migration 0033: tags + filters, the auto-cleanup
// selection, IP-mode device limits, the sharing heuristic with its debounce, torrent hit
// ingestion with auto-suspend, and per-node torrent overrides. Skipped without
// WGPANEL_TEST_POSTGRES_DSN. It restores the abuse settings and deletes what it creates.
func TestAbuseControlsIntegration(t *testing.T) {
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
	origCfg, err := s.GetAbuseSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}

	sfx := fmt.Sprint(time.Now().UnixNano())
	group := "abuse-" + sfx
	ns := "abuse-ns-" + sfx
	var nodeIDs []string
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = s.pool.Exec(bg, `DELETE FROM accounts WHERE owner_key_namespace = $1`, ns)
		for _, id := range nodeIDs {
			_, _ = s.pool.Exec(bg, `DELETE FROM nodes WHERE id = $1`, id)
		}
		_, _ = s.pool.Exec(bg, `DELETE FROM event_state WHERE key LIKE 'abuse:%'`)
		// Restore the singleton exactly (UpdateAbuseSettings treats nil as unchanged).
		o := origCfg
		_, _ = s.UpdateAbuseSettings(bg, UpdateAbuseSettingsParams{
			AutoCleanupEnabled: &o.AutoCleanupEnabled, AutoCleanupDays: &o.AutoCleanupDays,
			DeviceLimitMode: &o.DeviceLimitMode, DeviceWindowMinutes: &o.DeviceWindowMinutes,
			SharingDetectionEnabled: &o.SharingDetectionEnabled, SharingMinNetworks: &o.SharingMinNetworks,
			SharingWindowMinutes: &o.SharingWindowMinutes, TorrentBlockEnabled: &o.TorrentBlockEnabled,
			TorrentSuspendEnabled: &o.TorrentSuspendEnabled, TorrentSuspendHits: &o.TorrentSuspendHits,
			TorrentSuspendWindowMinutes: &o.TorrentSuspendWindowMinutes,
		})
	})

	key := "pk-node-abuse" + sfx
	node, err := s.CreateNode(ctx, "abuse-"+sfx, group, "eu", "abuse.test:51820", "10.89.0.0/16", 500, &key)
	if err != nil {
		t.Fatal(err)
	}
	nodeIDs = append(nodeIDs, node.ID)
	if _, err := s.pool.Exec(ctx, `UPDATE nodes SET status = 'online' WHERE id = $1`, node.ID); err != nil {
		t.Fatal(err)
	}

	var seq atomic.Int64
	mk := func(t *testing.T, mod func(*CreateAccountParams)) Account {
		t.Helper()
		n := seq.Add(1)
		nsCopy := ns
		p := CreateAccountParams{
			Label: fmt.Sprintf("abuse-acct-%d", n), PublicKey: fmt.Sprintf("pk-%s-%d", sfx, n), PrivateKeyEncrypted: "x",
			SubscriptionToken: fmt.Sprintf("tok-%s-%d", sfx, n), OwnerKeyNamespace: &nsCopy, AllowedNodeGroups: []string{group},
		}
		if mod != nil {
			mod(&p)
		}
		a, err := s.CreateAccount(ctx, p)
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
	setCfg := func(t *testing.T, p UpdateAbuseSettingsParams) {
		t.Helper()
		if _, err := s.UpdateAbuseSettings(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	ptr := func(v int) *int { return &v }
	bptr := func(v bool) *bool { return &v }
	sptr := func(v string) *string { return &v }

	t.Run("tags and note: store, filter, search, bulk edit", func(t *testing.T) {
		a := mk(t, nil)
		b := mk(t, nil)
		c := mk(t, nil)
		note := "paid by card, friend of Ali"
		tagsA, _ := NormalizeTags([]string{"VIP", "Trial"})
		tagsB, _ := NormalizeTags([]string{"vip"})
		if err := s.SetAccountMeta(ctx, a.ID, nil, &note, &tagsA); err != nil {
			t.Fatal(err)
		}
		if err := s.SetAccountMeta(ctx, b.ID, nil, nil, &tagsB); err != nil {
			t.Fatal(err)
		}
		// A foreign namespace cannot touch it.
		other := "someone-else"
		if err := s.SetAccountMeta(ctx, a.ID, &other, &note, nil); !errors.Is(err, ErrAccountNotFound) {
			t.Fatalf("foreign namespace: %v", err)
		}
		long := make([]byte, MaxNoteLength+1)
		for i := range long {
			long[i] = 'x'
		}
		if err := s.SetAccountMeta(ctx, a.ID, nil, sptr(string(long)), nil); !errors.Is(err, ErrNoteTooLong) {
			t.Fatalf("long note: %v", err)
		}

		list := func(f AccountFilter) map[string]bool {
			rows, err := s.ListAccounts(WithAccountFilter(ctx, f), 200, 0, &ns, "")
			if err != nil {
				t.Fatal(err)
			}
			out := map[string]bool{}
			for _, r := range rows {
				out[r.ID] = true
			}
			return out
		}
		if got := list(AccountFilter{Tags: []string{"vip"}}); !got[a.ID] || !got[b.ID] || got[c.ID] {
			t.Fatalf("tag vip: %v", got)
		}
		if got := list(AccountFilter{Tags: []string{"vip", "trial"}}); !got[a.ID] || got[b.ID] {
			t.Fatalf("tags AND: %v", got)
		}
		if got := list(AccountFilter{Query: "FRIEND of ali"}); !got[a.ID] || got[b.ID] {
			t.Fatalf("search by note: %v", got)
		}
		if got := list(AccountFilter{Query: "tri"}); !got[a.ID] || got[b.ID] {
			t.Fatalf("search by tag: %v", got)
		}
		if got := list(AccountFilter{Query: "100%_"}); len(got) != 0 {
			t.Fatalf("wildcards must be literal: %v", got)
		}
		after, err := s.ListAccountsAfter(WithAccountFilter(ctx, AccountFilter{Tags: []string{"trial"}}), 10, nil, "", &ns, "")
		if err != nil || len(after) != 1 || after[0].ID != a.ID {
			t.Fatalf("keyset list with filter: %v %v", after, err)
		}

		if err := s.ModifyAccountTags(ctx, c.ID, nil, []string{"vip", "new"}, nil); err != nil {
			t.Fatal(err)
		}
		if err := s.ModifyAccountTags(ctx, a.ID, nil, nil, []string{"trial", "absent"}); err != nil {
			t.Fatal(err)
		}
		meta, err := s.AccountMetaByIDs(ctx, []string{a.ID, b.ID, c.ID})
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(meta[c.ID].Tags) != "[new vip]" || fmt.Sprint(meta[a.ID].Tags) != "[vip]" || meta[a.ID].Note != note {
			t.Fatalf("meta after bulk edit: %+v", meta)
		}
		if meta[b.ID].Note != "" {
			t.Fatal("note should default to empty")
		}
		full := manyTags(MaxTagsPerAcct)
		if err := s.SetAccountMeta(ctx, b.ID, nil, nil, &full); err != nil {
			t.Fatal(err)
		}
		if err := s.ModifyAccountTags(ctx, b.ID, nil, []string{"one-more"}, nil); !errors.Is(err, ErrInvalidTag) {
			t.Fatalf("cap on bulk add: %v", err)
		}
		if err := s.ModifyAccountTags(ctx, "00000000-0000-0000-0000-000000000000", nil, []string{"x"}, nil); !errors.Is(err, ErrAccountNotFound) {
			t.Fatalf("missing account: %v", err)
		}
		// Clearing: an empty (non-nil) list removes every tag.
		empty := []string{}
		if err := s.SetAccountMeta(ctx, b.ID, nil, nil, &empty); err != nil {
			t.Fatal(err)
		}
		if m, _ := s.AccountMetaByIDs(ctx, []string{b.ID}); len(m[b.ID].Tags) != 0 {
			t.Fatalf("tags not cleared: %v", m[b.ID].Tags)
		}
	})

	t.Run("auto cleanup: only expired/quota suspensions older than N days", func(t *testing.T) {
		suspend := func(a Account, reason string, age time.Duration) {
			t.Helper()
			if _, err := s.pool.Exec(ctx, `UPDATE accounts SET status = 'suspended', suspend_reason = $2 WHERE id = $1`, a.ID, reason); err != nil {
				t.Fatal(err)
			}
			// suspended_at is trigger-managed; age it directly (this UPDATE does not touch
			// status/suspend_reason, so the trigger stays out of the way).
			if _, err := s.pool.Exec(ctx, `UPDATE accounts SET suspended_at = now() - $2::interval WHERE id = $1`, a.ID, fmt.Sprintf("%d seconds", int(age.Seconds()))); err != nil {
				t.Fatal(err)
			}
		}
		day := 24 * time.Hour
		expiredOld := mk(t, nil)
		suspend(expiredOld, "expired", 40*day)
		quotaOld := mk(t, nil)
		suspend(quotaOld, "quota_exceeded", 31*day)
		expiredFresh := mk(t, nil)
		suspend(expiredFresh, "expired", 29*day)
		manualOld := mk(t, nil)
		suspend(manualOld, "manual", 400*day)
		var protected []Account
		for _, reason := range []string{"abuse_flag", "device_limit", "torrent_abuse", "reseller_suspended"} {
			a := mk(t, nil)
			suspend(a, reason, 400*day)
			protected = append(protected, a)
		}
		activeOld := mk(t, nil)
		if _, err := s.pool.Exec(ctx, `UPDATE accounts SET suspended_at = now() - interval '400 days' WHERE id = $1`, activeOld.ID); err != nil {
			t.Fatal(err)
		}

		// The trigger starts the clock at suspension and clears it on re-enable.
		if a := mk(t, nil); true {
			if _, err := s.SuspendAccount(ctx, a.ID, nil, "manual"); err != nil {
				t.Fatal(err)
			}
			var at *time.Time
			_ = s.pool.QueryRow(ctx, `SELECT suspended_at FROM accounts WHERE id = $1`, a.ID).Scan(&at)
			if at == nil || time.Since(*at) > time.Minute {
				t.Fatalf("suspended_at not set by the trigger: %v", at)
			}
			if _, err := s.EnableAccount(ctx, a.ID, nil); err != nil {
				t.Fatal(err)
			}
			_ = s.pool.QueryRow(ctx, `SELECT suspended_at FROM accounts WHERE id = $1`, a.ID).Scan(&at)
			if at != nil {
				t.Fatalf("suspended_at not cleared on enable: %v", at)
			}
		}

		deleted, err := s.AutoDeleteDeadAccounts(ctx, 30, 100)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]AutoDeletedAccount{}
		for _, d := range deleted {
			got[d.ID] = d
		}
		if len(got) < 2 || got[expiredOld.ID].Reason != "expired" || got[quotaOld.ID].Reason != "quota_exceeded" {
			t.Fatalf("expected the two old expired/quota accounts, got %+v", got)
		}
		if got[expiredOld.ID].Namespace != ns || got[expiredOld.ID].SuspendedAt.IsZero() {
			t.Fatalf("event data incomplete: %+v", got[expiredOld.ID])
		}
		for _, keep := range append([]Account{expiredFresh, manualOld, activeOld}, protected...) {
			if _, bad := got[keep.ID]; bad {
				t.Fatalf("account %s must not be auto-deleted", keep.ID)
			}
		}
		if st := get(t, expiredOld.ID).Status; st != "deleted" {
			t.Fatalf("expiredOld status %q", st)
		}
		for _, keep := range []Account{expiredFresh, manualOld} {
			if st := get(t, keep.ID).Status; st != "suspended" {
				t.Fatalf("%s should still be suspended, is %q", keep.ID, st)
			}
		}
		if st := get(t, activeOld.ID).Status; st != "active" {
			t.Fatalf("active account touched: %q", st)
		}
		// Idempotent: nothing left to delete among our rows.
		again, err := s.AutoDeleteDeadAccounts(ctx, 30, 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range again {
			if d.Namespace == ns {
				t.Fatalf("second run deleted %+v", d)
			}
		}
		// A shorter threshold picks up the 29-day one, but still never the manual one.
		later, err := s.AutoDeleteDeadAccounts(ctx, 20, 100)
		if err != nil {
			t.Fatal(err)
		}
		var sawFresh bool
		for _, d := range later {
			if d.ID == expiredFresh.ID {
				sawFresh = true
			}
			if d.ID == manualOld.ID {
				t.Fatal("manual suspension deleted")
			}
		}
		if !sawFresh {
			t.Fatal("N days not respected: 29-day-old expired account should go at N=20")
		}
	})

	t.Run("device limit: endpoint vs ip mode", func(t *testing.T) {
		limit := 1
		a := mk(t, func(p *CreateAccountParams) { p.DeviceLimit = &limit })
		beat := func(endpoint string) {
			t.Helper()
			hs := time.Now()
			if err := s.IngestHeartbeatTelemetry(ctx, node.ID, []PeerTrafficReport{{PublicKey: a.PublicKey, LastHandshake: &hs, Endpoint: endpoint}}, nil); err != nil {
				t.Fatal(err)
			}
		}
		exceeded := func() bool { return get(t, a.ID).DeviceLimitExceededAt != nil }

		setCfg(t, UpdateAbuseSettingsParams{DeviceLimitMode: sptr(DeviceModeEndpoint), DeviceWindowMinutes: ptr(5)})
		beat("203.0.113.10:40001")
		if exceeded() {
			t.Fatal("one endpoint must not exceed a limit of 1")
		}
		beat("203.0.113.10:40002") // same IP, new NAT port
		if !exceeded() {
			t.Fatal("endpoint mode: a second ip:port should exceed the limit")
		}

		// Switch to IP mode: the same two endpoints are one IP, so the flag clears on the
		// next sighting.
		setCfg(t, UpdateAbuseSettingsParams{DeviceLimitMode: sptr(DeviceModeIP)})
		beat("203.0.113.10:40003")
		if exceeded() {
			t.Fatal("ip mode: several ports of one IP must count once")
		}
		beat("198.51.100.20:40001")
		if !exceeded() {
			t.Fatal("ip mode: a second IP should exceed the limit")
		}

		// A shorter window forgets older sightings: age the first IP's rows beyond 1 minute.
		setCfg(t, UpdateAbuseSettingsParams{DeviceWindowMinutes: ptr(1)})
		if _, err := s.pool.Exec(ctx, `UPDATE account_devices SET last_seen_at = now() - interval '3 minutes' WHERE account_id = $1 AND source_endpoint LIKE '203.%'`, a.ID); err != nil {
			t.Fatal(err)
		}
		beat("198.51.100.20:40001")
		if exceeded() {
			t.Fatal("window: only one IP is inside the 1 minute window")
		}
		setCfg(t, UpdateAbuseSettingsParams{DeviceLimitMode: sptr(DeviceModeEndpoint), DeviceWindowMinutes: ptr(5)})
	})

	t.Run("sharing heuristic and debounce", func(t *testing.T) {
		a := mk(t, nil)
		seen := func(endpoint string, ago time.Duration) {
			t.Helper()
			if _, err := s.pool.Exec(ctx, `
				INSERT INTO account_devices (account_id, source_endpoint, node_id, first_seen_at, last_seen_at)
				VALUES ($1, $2, $3, now() - $4::interval, now() - $4::interval)
				ON CONFLICT (account_id, source_endpoint) DO UPDATE SET last_seen_at = EXCLUDED.last_seen_at`,
				a.ID, endpoint, node.ID, fmt.Sprintf("%d seconds", int(ago.Seconds()))); err != nil {
				t.Fatal(err)
			}
		}
		flagged := func() bool {
			var at *time.Time
			_ = s.pool.QueryRow(ctx, `SELECT sharing_suspected_at FROM accounts WHERE id = $1`, a.ID).Scan(&at)
			return at != nil
		}
		eval := func() []SharingEvent {
			evs, err := s.EvaluateSharing(ctx, 3, 10*time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			var mine []SharingEvent
			for _, e := range evs {
				if e.AccountID == a.ID {
					mine = append(mine, e)
				}
			}
			return mine
		}

		// Two IPs in one /24 plus one elsewhere = 2 networks: below N=3.
		seen("203.0.113.5:1", time.Minute)
		seen("203.0.113.99:1", time.Minute)
		seen("198.51.100.1:1", time.Minute)
		if evs := eval(); len(evs) != 0 || flagged() {
			t.Fatalf("2 networks must not flag: %v", evs)
		}
		// A third network crosses it.
		seen("192.0.2.7:1", time.Minute)
		evs := eval()
		if len(evs) != 1 || len(evs[0].Networks) != 3 || !flagged() || evs[0].Namespace != ns {
			t.Fatalf("3 networks should flag once: %+v flagged=%v", evs, flagged())
		}
		// Still over: no repeat event (edge-triggered).
		if evs := eval(); len(evs) != 0 {
			t.Fatalf("repeat event while flagged: %v", evs)
		}
		// Sightings outside the window do not count: the flag clears.
		for _, e := range []string{"198.51.100.1:1", "192.0.2.7:1"} {
			seen(e, time.Hour)
		}
		if evs := eval(); len(evs) != 0 || flagged() {
			t.Fatalf("flag should clear when networks age out: %v flagged=%v", evs, flagged())
		}
		// Re-crossing inside the debounce window raises the flag again but does NOT
		// re-announce.
		seen("198.51.100.1:1", time.Minute)
		seen("192.0.2.7:1", time.Minute)
		if evs := eval(); len(evs) != 0 {
			t.Fatalf("debounce failed, event re-emitted: %v", evs)
		}
		if !flagged() {
			t.Fatal("flag should be standing again after re-crossing")
		}
		// Once the debounce window has passed, a fresh rise announces again.
		for _, e := range []string{"198.51.100.1:1", "192.0.2.7:1"} {
			seen(e, time.Hour)
		}
		eval() // clears
		if _, err := s.pool.Exec(ctx, `UPDATE event_state SET last_fired_at = now() - interval '7 hours' WHERE key = $1`, "abuse:sharing:"+a.ID); err != nil {
			t.Fatal(err)
		}
		seen("198.51.100.1:1", time.Minute)
		seen("192.0.2.7:1", time.Minute)
		if evs := eval(); len(evs) != 1 {
			t.Fatalf("after the debounce window a rise should announce: %v", evs)
		}
		// Read-side detail for the account view.
		nets, err := s.AccountSharingNetworks(ctx, a.ID, &ns, 10*time.Minute)
		if err != nil || len(nets) != 3 {
			t.Fatalf("detail: %v %v", nets, err)
		}
		if _, err := s.AccountSharingNetworks(ctx, a.ID, sptr("other"), 10*time.Minute); err != nil {
			t.Fatal(err)
		}
		if other, _ := s.AccountSharingNetworks(ctx, a.ID, sptr("other"), 10*time.Minute); len(other) != 0 {
			t.Fatal("namespace scoping leaked the sharing detail")
		}
		if err := s.ClearSharingFlags(ctx); err != nil || flagged() {
			t.Fatalf("clear flags: %v", err)
		}
	})

	t.Run("torrent hits: ingestion, window threshold, auto-suspend", func(t *testing.T) {
		a := mk(t, nil)
		manual := mk(t, nil)
		peers, err := s.ListAccountPeersWithNode(ctx, a.ID, nil)
		if err != nil || len(peers) != 1 {
			t.Fatalf("peers: %v %v", peers, err)
		}
		ip := peers[0].AssignedIP
		mpeers, _ := s.ListAccountPeersWithNode(ctx, manual.ID, nil)
		mip := mpeers[0].AssignedIP

		// Suspend off: hits are recorded and nothing is suspended.
		setCfg(t, UpdateAbuseSettingsParams{TorrentSuspendEnabled: bptr(false), TorrentSuspendHits: ptr(10), TorrentSuspendWindowMinutes: ptr(60)})
		evs, err := s.IngestTorrentHits(ctx, node.ID, []TorrentHitReport{{IP: ip, Hits: 7}, {IP: "10.89.250.250", Hits: 5}})
		if err != nil {
			t.Fatal(err)
		}
		if len(evs) != 1 || evs[0].AccountID != a.ID || evs[0].Hits != 7 || evs[0].Suspended || !evs[0].Announce {
			t.Fatalf("unexpected events %+v", evs)
		}
		if get(t, a.ID).Status != "active" {
			t.Fatal("suspended with auto-suspend off")
		}
		// The next report right away is debounced for the announcement.
		evs, _ = s.IngestTorrentHits(ctx, node.ID, []TorrentHitReport{{IP: ip, Hits: 1}})
		if len(evs) != 1 || evs[0].Announce {
			t.Fatalf("debounce: %+v", evs)
		}

		// Auto-suspend on, threshold 10 within 60 minutes. 8 already recorded (un-acted).
		setCfg(t, UpdateAbuseSettingsParams{TorrentSuspendEnabled: bptr(true)})
		evs, _ = s.IngestTorrentHits(ctx, node.ID, []TorrentHitReport{{IP: ip, Hits: 1}})
		if evs[0].Suspended || evs[0].WindowHits != 9 {
			t.Fatalf("9 of 10 should not suspend: %+v", evs[0])
		}
		evs, _ = s.IngestTorrentHits(ctx, node.ID, []TorrentHitReport{{IP: ip, Hits: 1}})
		if !evs[0].Suspended || !evs[0].Announce {
			t.Fatalf("10 of 10 should suspend and announce: %+v", evs[0])
		}
		got := get(t, a.ID)
		if got.Status != "suspended" || got.SuspendReason == nil || *got.SuspendReason != "torrent_abuse" {
			t.Fatalf("not suspended as torrent_abuse: %+v", got)
		}

		// Manual enable starts from zero: old hits are marked acted.
		if _, err := s.EnableAccount(ctx, a.ID, nil); err != nil {
			t.Fatal(err)
		}
		evs, _ = s.IngestTorrentHits(ctx, node.ID, []TorrentHitReport{{IP: ip, Hits: 3}})
		if evs[0].Suspended || evs[0].WindowHits != 3 {
			t.Fatalf("re-suspended on acted hits: %+v", evs[0])
		}

		// Hits outside the window do not count.
		if _, err := s.pool.Exec(ctx, `UPDATE account_torrent_hits SET ts = now() - interval '3 hours' WHERE account_id = $1`, a.ID); err != nil {
			t.Fatal(err)
		}
		evs, _ = s.IngestTorrentHits(ctx, node.ID, []TorrentHitReport{{IP: ip, Hits: 9}})
		if evs[0].Suspended || evs[0].WindowHits != 9 {
			t.Fatalf("old hits counted: %+v", evs[0])
		}

		// A manually suspended account keeps its reason even past the threshold.
		if _, err := s.SuspendAccount(ctx, manual.ID, nil, "manual"); err != nil {
			t.Fatal(err)
		}
		evs, _ = s.IngestTorrentHits(ctx, node.ID, []TorrentHitReport{{IP: mip, Hits: 500}})
		if evs[0].Suspended {
			t.Fatal("manual suspension replaced")
		}
		if m := get(t, manual.ID); *m.SuspendReason != "manual" {
			t.Fatalf("reason overwritten: %v", *m.SuspendReason)
		}

		sum, err := s.GetAccountTorrentSummary(ctx, a.ID, &ns)
		if err != nil || sum.Hits30d < 20 || len(sum.Recent) == 0 || sum.LastHit == nil {
			t.Fatalf("summary: %+v %v", sum, err)
		}
		if other, _ := s.GetAccountTorrentSummary(ctx, a.ID, sptr("other")); other.Hits30d != 0 {
			t.Fatal("namespace scoping leaked torrent history")
		}
		meta, _ := s.AccountMetaByIDs(ctx, []string{a.ID})
		// 7+1+1+1+3 aged to 3 hours ago (still inside 24h) plus the 9 just now.
		if meta[a.ID].TorrentHits24h != 22 {
			t.Fatalf("24h hits = %d, want 22", meta[a.ID].TorrentHits24h)
		}
		if err := s.PruneTorrentHits(ctx, time.Hour); err != nil {
			t.Fatal(err)
		}
		if sum, _ := s.GetAccountTorrentSummary(ctx, a.ID, &ns); sum.Hits30d != 9 {
			t.Fatalf("prune: %d hits left, want only the 9 from the last hour", sum.Hits30d)
		}
	})

	t.Run("torrent block: global default and per-node override", func(t *testing.T) {
		setCfg(t, UpdateAbuseSettingsParams{TorrentBlockEnabled: bptr(false)})
		if on, err := s.EffectiveTorrentBlock(ctx, node.ID); err != nil || on {
			t.Fatalf("default should be off: %v %v", on, err)
		}
		setCfg(t, UpdateAbuseSettingsParams{TorrentBlockEnabled: bptr(true)})
		if on, _ := s.EffectiveTorrentBlock(ctx, node.ID); !on {
			t.Fatal("global on should reach the node")
		}
		if err := s.SetNodeTorrentOverride(ctx, node.ID, bptr(false)); err != nil {
			t.Fatal(err)
		}
		if on, _ := s.EffectiveTorrentBlock(ctx, node.ID); on {
			t.Fatal("node override off must win over global on")
		}
		list, _ := s.ListNodeTorrentOverrides(ctx)
		var found bool
		for _, n := range list {
			if n.NodeID == node.ID {
				found = true
				if n.Override == nil || *n.Override || n.Effective {
					t.Fatalf("listing: %+v", n)
				}
			}
		}
		if !found {
			t.Fatal("node missing from listing")
		}
		if err := s.SetNodeTorrentOverride(ctx, node.ID, nil); err != nil {
			t.Fatal(err)
		}
		if on, _ := s.EffectiveTorrentBlock(ctx, node.ID); !on {
			t.Fatal("cleared override should inherit global on")
		}
		if err := s.SetNodeTorrentOverride(ctx, "00000000-0000-0000-0000-000000000000", bptr(true)); !errors.Is(err, ErrNodeNotFound) {
			t.Fatalf("unknown node: %v", err)
		}
	})
}
