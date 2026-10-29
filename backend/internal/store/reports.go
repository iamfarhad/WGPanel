package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Report types carry their JSON tags directly: the reports payload is a pure read
// model with no separate internal representation worth mapping.

type ReportRange struct {
	Key    string    `json:"key"`
	From   time.Time `json:"from"`
	To     time.Time `json:"to"`
	Bucket string    `json:"bucket"` // "hour" | "day"
}

type ReportScope struct {
	ResellerID *string `json:"reseller_id"`
	Owner      *string `json:"owner"`
}

type ReportTotals struct {
	RxBytes           int64 `json:"rx_bytes"`
	TxBytes           int64 `json:"tx_bytes"`
	ActiveAccounts    int   `json:"active_accounts"`
	NewAccounts       int   `json:"new_accounts"`
	AccountsTotal     int   `json:"accounts_total"`
	AccountsActive    int   `json:"accounts_active"`
	AccountsSuspended int   `json:"accounts_suspended"`
	OnlineNow         int   `json:"online_now"`
}

type TrafficPoint struct {
	TS             time.Time `json:"ts"`
	RxBytes        int64     `json:"rx_bytes"`
	TxBytes        int64     `json:"tx_bytes"`
	ActiveAccounts int       `json:"active_accounts"`
}

type CountPoint struct {
	TS    time.Time `json:"ts"`
	Count int       `json:"count"`
}

type StatusCount struct {
	Status string  `json:"status"`
	Reason *string `json:"reason"`
	Count  int     `json:"count"`
}

type BucketCount struct {
	Bucket string `json:"bucket"`
	Count  int    `json:"count"`
}

// ReportExpiringAccount / ReportTopAccount are separate types (not one struct with
// omitempty) so nullable fields such as data_quota_bytes serialise as explicit nulls.
type ReportExpiringAccount struct {
	ID             string    `json:"id"`
	Label          string    `json:"label"`
	Owner          *string   `json:"owner"`
	ResellerID     *string   `json:"reseller_id"`
	ExpiryAt       time.Time `json:"expiry_at"`
	Status         string    `json:"status"`
	DataUsedBytes  int64     `json:"data_used_bytes"`
	DataQuotaBytes *int64    `json:"data_quota_bytes"`
}

type ReportTopAccount struct {
	ID         string  `json:"id"`
	Label      string  `json:"label"`
	Owner      *string `json:"owner"`
	ResellerID *string `json:"reseller_id"`
	RxBytes    int64   `json:"rx_bytes"`
	TxBytes    int64   `json:"tx_bytes"`
}

type ReportNode struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Status        string   `json:"status"`
	RxBytes       int64    `json:"rx_bytes"`
	TxBytes       int64    `json:"tx_bytes"`
	Accounts      int      `json:"accounts"`
	CPUAvg        *float64 `json:"cpu_avg"`
	MemAvgPercent *float64 `json:"mem_avg_percent"`
}

type ReportReseller struct {
	ID                 string     `json:"id"`
	Username           string     `json:"username"`
	Disabled           bool       `json:"disabled"`
	ExpiresAt          *time.Time `json:"expires_at"`
	Accounts           int        `json:"accounts"`
	DataAllocatedBytes int64      `json:"data_allocated_bytes"`
	DataLimitBytes     *int64     `json:"data_limit_bytes"`
	RxBytes            int64      `json:"rx_bytes"`
	TxBytes            int64      `json:"tx_bytes"`
}

type Report struct {
	Range             ReportRange             `json:"range"`
	Scope             ReportScope             `json:"scope"`
	Totals            ReportTotals            `json:"totals"`
	TrafficSeries     []TrafficPoint          `json:"traffic_series"`
	NewAccountsSeries []CountPoint            `json:"new_accounts_series"`
	StatusBreakdown   []StatusCount           `json:"status_breakdown"`
	QuotaBuckets      []BucketCount           `json:"quota_buckets"`
	ExpiryBuckets     []BucketCount           `json:"expiry_buckets"`
	Expiring          []ReportExpiringAccount `json:"expiring"`
	TopAccounts       []ReportTopAccount      `json:"top_accounts"`
	Nodes             []ReportNode            `json:"nodes"`
	Resellers         []ReportReseller        `json:"resellers"`
}

type ReportParams struct {
	RangeKey string // 24h|7d|30d|90d|365d
	// CallerNamespace pins a reseller/API-key caller to its namespace (nil = staff).
	CallerNamespace *string
	// OwnerFilter narrows a staff view: "" none, "admin", or an exact namespace.
	OwnerFilter        string
	IncludeNodeMetrics bool // staff only: CPU/RAM averages leak infrastructure detail
	IncludeResellers   bool // super_admin, unscoped only
	Now                time.Time
}

type rangeSpec struct {
	dur  time.Duration
	unit string // postgres date_trunc unit == bucket name
	step time.Duration
	n    int
}

var reportRanges = map[string]rangeSpec{
	"24h":  {unit: "hour", step: time.Hour, n: 24},
	"7d":   {unit: "hour", step: time.Hour, n: 7 * 24},
	"30d":  {unit: "day", step: 24 * time.Hour, n: 30},
	"90d":  {unit: "day", step: 24 * time.Hour, n: 90},
	"365d": {unit: "day", step: 24 * time.Hour, n: 365},
}

// ValidReportRange reports whether key is a supported range.
func ValidReportRange(key string) bool { _, ok := reportRanges[key]; return ok }

// reportScopeSQL is the account predicate every report query shares ($1 caller
// namespace, $2 staff owner filter) - the same shape as namespaceFilter plus the
// admin-only owner narrowing, so a reseller can never reach another namespace.
func reportScopeSQL(alias string) string {
	return fmt.Sprintf(`(($1::text IS NULL OR %[1]s.owner_key_namespace = $1)
		AND ($2::text = '' OR ($2 = 'admin' AND %[1]s.owner_key_namespace IS NULL) OR %[1]s.owner_key_namespace = $2))`, alias)
}

func resellerIDOf(owner *string) *string {
	if owner == nil {
		return nil
	}
	if id, ok := strings.CutPrefix(*owner, ResellerNamespacePrefix); ok {
		return &id
	}
	return nil
}

// BuildReport assembles the whole reports payload. Hour-bucketed ranges read raw
// samples; day-bucketed ones read the traffic_daily continuous aggregate (which, with
// real-time aggregation, also covers today's not-yet-materialised rows).
func (s *Store) BuildReport(ctx context.Context, p ReportParams) (Report, error) {
	spec, ok := reportRanges[p.RangeKey]
	if !ok {
		return Report{}, fmt.Errorf("unknown report range %q", p.RangeKey)
	}
	now := p.Now
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC().Truncate(time.Second)
	if err := s.reconcileLimits(ctx); err != nil {
		return Report{}, err
	}

	// The first bucket starts (n-1) steps before the current bucket, so the series has
	// exactly n evenly spaced buckets ending with the (partial) current one.
	cur := now.Truncate(spec.step)
	from := cur.Add(-time.Duration(spec.n-1) * spec.step)
	r := Report{
		Range: ReportRange{Key: p.RangeKey, From: from, To: now, Bucket: spec.unit},
		Scope: ReportScope{ResellerID: resellerIDOf(scopeOwnerRaw(p)), Owner: scopeOwnerRaw(p)},
	}
	q := reportQuery{s: s, ctx: ctx, ns: p.CallerNamespace, owner: p.OwnerFilter, from: from, now: now, spec: spec}

	steps := []func(*Report) error{
		q.traffic, q.newAccounts, q.accountTotals, q.statusBreakdown, q.quotaBuckets,
		q.expiryBuckets, q.expiring, q.topAccounts,
	}
	for _, step := range steps {
		if err := step(&r); err != nil {
			return Report{}, err
		}
	}
	var err error
	if r.Nodes, err = q.nodes(p.IncludeNodeMetrics); err != nil {
		return Report{}, err
	}
	if p.IncludeResellers {
		if r.Resellers, err = q.resellers(); err != nil {
			return Report{}, err
		}
	}
	return r, nil
}

// scopeOwner/scopeOwnerRaw describe the applied scope in the response: the staff
// owner filter when set, else the caller's own namespace.
func scopeOwnerRaw(p ReportParams) *string {
	switch {
	case p.OwnerFilter != "":
		return &p.OwnerFilter
	case p.CallerNamespace != nil:
		return p.CallerNamespace
	}
	return nil
}
