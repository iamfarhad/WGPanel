package httpapi

import (
	"testing"
	"time"

	"wgpanel-api/internal/store"
)

func ptr[T any](v T) *T { return &v }

func TestResolveAccountSpecPrecedence(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	quota := int64(10_000_000_000)
	tmpl := &store.PlanTemplate{
		DataQuotaBytes: &quota, DurationDays: ptr(30), DeviceLimit: ptr(2), BandwidthLimitMbps: ptr(20),
		ResetStrategy: "month", NodeGroup: ptr("eu"),
	}

	t.Run("template fills everything", func(t *testing.T) {
		sp, err := resolveAccountSpec(createAccountRequest{Label: "x"}, tmpl, now)
		if err != nil {
			t.Fatal(err)
		}
		if sp.Quota == nil || *sp.Quota != quota || *sp.DeviceLimit != 2 || *sp.Bandwidth != 20 || sp.ResetStrategy != "month" {
			t.Fatalf("template values not applied: %+v", sp)
		}
		if sp.Expiry == nil || !sp.Expiry.Equal(now.Add(30*24*time.Hour)) {
			t.Fatalf("expiry = %v", sp.Expiry)
		}
		if sp.TemplateNodeGrp == nil || *sp.TemplateNodeGrp != "eu" {
			t.Fatal("node group lost")
		}
	})

	t.Run("explicit fields override the template", func(t *testing.T) {
		exp := "2027-05-05T00:00:00Z"
		sp, err := resolveAccountSpec(createAccountRequest{
			DataQuotaGB: ptr(5.0), DeviceLimit: ptr(9), BandwidthLimitMbps: ptr(50), ResetStrategy: ptr("week"), ExpiryAt: &exp,
		}, tmpl, now)
		if err != nil {
			t.Fatal(err)
		}
		if *sp.Quota != 5_000_000_000 || *sp.DeviceLimit != 9 || *sp.Bandwidth != 50 || sp.ResetStrategy != "week" {
			t.Fatalf("overrides lost: %+v", sp)
		}
		if sp.Expiry == nil || sp.Expiry.Year() != 2027 {
			t.Fatalf("explicit expiry lost: %v", sp.Expiry)
		}
	})

	t.Run("no template, nothing set", func(t *testing.T) {
		sp, err := resolveAccountSpec(createAccountRequest{}, nil, now)
		if err != nil || sp.Quota != nil || sp.Expiry != nil || sp.HoldDays != nil || sp.ResetStrategy != "none" {
			t.Fatalf("unexpected: %+v %v", sp, err)
		}
	})
}

func TestResolveAccountSpecOnHold(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	t.Run("on hold uses the duration and has no expiry", func(t *testing.T) {
		sp, err := resolveAccountSpec(createAccountRequest{OnHold: ptr(true), HoldDurationDays: ptr(30), HoldDeadlineDays: ptr(7)}, nil, now)
		if err != nil {
			t.Fatal(err)
		}
		if sp.HoldDays == nil || *sp.HoldDays != 30 || sp.Expiry != nil {
			t.Fatalf("%+v", sp)
		}
		if sp.HoldDeadline == nil || !sp.HoldDeadline.Equal(now.Add(7*24*time.Hour)) {
			t.Fatalf("deadline = %v", sp.HoldDeadline)
		}
	})

	t.Run("template on-hold flag, request can turn it off", func(t *testing.T) {
		tmpl := &store.PlanTemplate{OnHold: true, DurationDays: ptr(10), HoldDeadlineDays: ptr(3)}
		sp, err := resolveAccountSpec(createAccountRequest{}, tmpl, now)
		if err != nil || sp.HoldDays == nil || *sp.HoldDays != 10 || sp.HoldDeadline == nil {
			t.Fatalf("%+v %v", sp, err)
		}
		sp, err = resolveAccountSpec(createAccountRequest{OnHold: ptr(false)}, tmpl, now)
		if err != nil || sp.HoldDays != nil || sp.Expiry == nil || sp.HoldDeadline != nil {
			t.Fatalf("on_hold=false should start the clock now: %+v %v", sp, err)
		}
	})

	t.Run("rejects expiry with on hold, missing duration, bad strategy", func(t *testing.T) {
		exp := "2027-01-01T00:00:00Z"
		for name, req := range map[string]createAccountRequest{
			"expiry+hold":    {OnHold: ptr(true), HoldDurationDays: ptr(5), ExpiryAt: &exp},
			"no duration":    {OnHold: ptr(true)},
			"zero duration":  {OnHold: ptr(true), HoldDurationDays: ptr(0)},
			"bad strategy":   {ResetStrategy: ptr("hourly")},
			"negative quota": {DataQuotaGB: ptr(-1.0)},
			"bad expiry":     {ExpiryAt: ptr("tomorrow")},
		} {
			if _, err := resolveAccountSpec(req, nil, now); err == nil || err.Status != 400 {
				t.Errorf("%s: want 400, got %v", name, err)
			}
		}
	})
}

func TestBulkHelpers(t *testing.T) {
	if got := bulkLabel("vip", 3, 5); got != "vip-03" {
		t.Errorf("got %s", got)
	}
	if got := bulkLabel("vip", 7, 120); got != "vip-007" {
		t.Errorf("got %s", got)
	}
	for action, perm := range map[string]string{"create": "create", "renew": "update", "reset_usage": "update",
		"suspend": "suspend", "enable": "suspend", "delete": "delete", "nope": ""} {
		if got := bulkPermission(action); got != perm {
			t.Errorf("%s: %q != %q", action, got, perm)
		}
	}
	support := &CallerIdentity{IsAdmin: true, AdminRole: "support"}
	if identityHasPermission(support, "delete") || !identityHasPermission(support, "read") {
		t.Error("support must be read-only")
	}
	key := &CallerIdentity{Permissions: []string{"create", "read"}}
	if identityHasPermission(key, "delete") || !identityHasPermission(key, "create") {
		t.Error("key permissions not honoured")
	}
}

func TestToAPIErrorMapsResellerAndAccountErrors(t *testing.T) {
	cases := map[error]string{
		store.ErrResellerAccountLimit:  "reseller_account_limit_reached",
		store.ErrResellerDataCredit:    "reseller_data_credit_exceeded",
		store.ErrAccountNotFound:       "account_not_found",
		store.ErrAccountQuotaExhausted: "quota_exhausted",
		store.ErrNoAvailableNode:       "no_available_node",
	}
	for err, code := range cases {
		if got := toAPIError(err, "x"); got.Code != code {
			t.Errorf("%v -> %s, want %s", err, got.Code, code)
		}
	}
}
