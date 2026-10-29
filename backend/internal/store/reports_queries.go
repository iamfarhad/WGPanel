package store

import (
	"context"
	"time"
)

type reportQuery struct {
	s     *Store
	ctx   context.Context
	ns    *string
	owner string
	from  time.Time
	now   time.Time
	spec  rangeSpec
}

// trafficSource names the relation/columns the range reads: raw samples for hourly
// ranges, the daily continuous aggregate for the long ones.
func (q reportQuery) trafficSource() (table, ts, rx, tx string) {
	if q.spec.unit == "hour" {
		return "peer_traffic_samples", "ts", "rx_delta", "tx_delta"
	}
	return "traffic_daily", "bucket", "rx_bytes", "tx_bytes"
}

// interval is the time_bucket width. time_bucket (not date_trunc) so day buckets are
// UTC midnights regardless of the DB session time zone.
func (q reportQuery) interval() string { return "1 " + q.spec.unit }

func (q reportQuery) fill(start time.Time, n int) []time.Time {
	out := make([]time.Time, n)
	for i := range out {
		out[i] = start.Add(time.Duration(i) * q.spec.step)
	}
	return out
}

func (q reportQuery) traffic(r *Report) error {
	tbl, ts, rx, tx := q.trafficSource()
	rows, err := q.s.pool.Query(q.ctx, `
		SELECT time_bucket('`+q.interval()+`', t.`+ts+`) AS b, COALESCE(sum(t.`+rx+`),0)::bigint, COALESCE(sum(t.`+tx+`),0)::bigint, count(DISTINCT t.account_id)::int
		FROM `+tbl+` t JOIN accounts a ON a.id = t.account_id
		WHERE `+reportScopeSQL("a")+` AND t.`+ts+` >= $3
		GROUP BY 1`, q.ns, q.owner, q.from)
	if err != nil {
		return err
	}
	defer rows.Close()
	byBucket := map[time.Time]TrafficPoint{}
	for rows.Next() {
		var p TrafficPoint
		if err := rows.Scan(&p.TS, &p.RxBytes, &p.TxBytes, &p.ActiveAccounts); err != nil {
			return err
		}
		byBucket[p.TS.UTC()] = p
	}
	if err := rows.Err(); err != nil {
		return err
	}
	r.TrafficSeries = make([]TrafficPoint, 0, q.spec.n)
	for _, b := range q.fill(q.from, q.spec.n) {
		p := byBucket[b] // zero value for a missing bucket
		p.TS = b
		r.Totals.RxBytes += p.RxBytes
		r.Totals.TxBytes += p.TxBytes
		r.TrafficSeries = append(r.TrafficSeries, p)
	}
	// Distinct accounts across the whole range (not the sum of per-bucket counts).
	return q.s.pool.QueryRow(q.ctx, `
		SELECT count(DISTINCT t.account_id)::int
		FROM `+tbl+` t JOIN accounts a ON a.id = t.account_id
		WHERE `+reportScopeSQL("a")+` AND t.`+ts+` >= $3`, q.ns, q.owner, q.from).Scan(&r.Totals.ActiveAccounts)
}

func (q reportQuery) newAccounts(r *Report) error {
	rows, err := q.s.pool.Query(q.ctx, `
		SELECT time_bucket('`+q.interval()+`', a.created_at) AS b, count(*)::int
		FROM accounts a WHERE `+reportScopeSQL("a")+` AND a.created_at >= $3
		GROUP BY 1`, q.ns, q.owner, q.from)
	if err != nil {
		return err
	}
	defer rows.Close()
	byBucket := map[time.Time]int{}
	for rows.Next() {
		var ts time.Time
		var n int
		if err := rows.Scan(&ts, &n); err != nil {
			return err
		}
		byBucket[ts.UTC()] = n
	}
	if err := rows.Err(); err != nil {
		return err
	}
	r.NewAccountsSeries = make([]CountPoint, 0, q.spec.n)
	for _, b := range q.fill(q.from, q.spec.n) {
		r.Totals.NewAccounts += byBucket[b]
		r.NewAccountsSeries = append(r.NewAccountsSeries, CountPoint{TS: b, Count: byBucket[b]})
	}
	return nil
}

func (q reportQuery) accountTotals(r *Report) error {
	// A peer counts as online iff it handshook within 3 minutes (PRD-monitoring-stats §5).
	return q.s.pool.QueryRow(q.ctx, `
		SELECT count(*)::int,
		       count(*) FILTER (WHERE a.status = 'active')::int,
		       count(*) FILTER (WHERE a.status = 'suspended')::int,
		       count(*) FILTER (WHERE EXISTS (
		           SELECT 1 FROM account_peers ap
		           WHERE ap.account_id = a.id AND ap.last_handshake_at > now() - interval '3 minutes'))::int
		FROM accounts a WHERE a.status != 'deleted' AND `+reportScopeSQL("a"), q.ns, q.owner).
		Scan(&r.Totals.AccountsTotal, &r.Totals.AccountsActive, &r.Totals.AccountsSuspended, &r.Totals.OnlineNow)
}

func (q reportQuery) statusBreakdown(r *Report) error {
	rows, err := q.s.pool.Query(q.ctx, `
		SELECT a.status, a.suspend_reason, count(*)::int
		FROM accounts a WHERE a.status != 'deleted' AND `+reportScopeSQL("a")+`
		GROUP BY 1, 2 ORDER BY 1, 2`, q.ns, q.owner)
	if err != nil {
		return err
	}
	defer rows.Close()
	r.StatusBreakdown = []StatusCount{}
	for rows.Next() {
		var c StatusCount
		if err := rows.Scan(&c.Status, &c.Reason, &c.Count); err != nil {
			return err
		}
		r.StatusBreakdown = append(r.StatusBreakdown, c)
	}
	return rows.Err()
}

// bucketCounts runs a query returning (bucket, count) rows and emits every bucket in
// `order`, zero-filled, so the chart never has to guess at missing categories.
func (q reportQuery) bucketCounts(sql string, order []string, args ...any) ([]BucketCount, error) {
	rows, err := q.s.pool.Query(q.ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	got := map[string]int{}
	for rows.Next() {
		var b string
		var n int
		if err := rows.Scan(&b, &n); err != nil {
			return nil, err
		}
		got[b] = n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]BucketCount, 0, len(order))
	for _, b := range order {
		out = append(out, BucketCount{Bucket: b, Count: got[b]})
	}
	return out, nil
}

func (q reportQuery) quotaBuckets(r *Report) (err error) {
	r.QuotaBuckets, err = q.bucketCounts(`
		SELECT CASE WHEN a.data_quota_bytes IS NULL THEN 'unlimited'
		            WHEN a.data_used_bytes >= a.data_quota_bytes THEN 'exceeded'
		            WHEN a.data_used_bytes * 100 >= a.data_quota_bytes * 80 THEN '80to100'
		            WHEN a.data_used_bytes * 100 >= a.data_quota_bytes * 50 THEN '50to80'
		            ELSE 'lt50' END AS b, count(*)::int
		FROM accounts a WHERE a.status != 'deleted' AND `+reportScopeSQL("a")+` GROUP BY 1`,
		[]string{"unlimited", "lt50", "50to80", "80to100", "exceeded"}, q.ns, q.owner)
	return
}

// expiryBuckets are mutually exclusive: each account lands in the first matching one.
// "today" runs to the end of the current UTC day.
func (q reportQuery) expiryBuckets(r *Report) (err error) {
	r.ExpiryBuckets, err = q.bucketCounts(`
		SELECT CASE WHEN a.expiry_at IS NULL THEN 'never'
		            WHEN a.expiry_at <= now() THEN 'expired'
		            WHEN a.expiry_at < date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' + interval '1 day' THEN 'today'
		            WHEN a.expiry_at <= now() + interval '3 days' THEN '3d'
		            WHEN a.expiry_at <= now() + interval '7 days' THEN '7d'
		            WHEN a.expiry_at <= now() + interval '30 days' THEN '30d'
		            ELSE 'later' END AS b, count(*)::int
		FROM accounts a WHERE a.status != 'deleted' AND `+reportScopeSQL("a")+` GROUP BY 1`,
		[]string{"expired", "today", "3d", "7d", "30d", "later", "never"}, q.ns, q.owner)
	return
}

func (q reportQuery) expiring(r *Report) error {
	rows, err := q.s.pool.Query(q.ctx, `
		SELECT a.id, a.label, a.owner_key_namespace, a.expiry_at, a.status, a.data_used_bytes, a.data_quota_bytes
		FROM accounts a
		WHERE a.status = 'active' AND a.expiry_at > now() AND a.expiry_at <= now() + interval '7 days'
		  AND `+reportScopeSQL("a")+`
		ORDER BY a.expiry_at LIMIT 20`, q.ns, q.owner)
	if err != nil {
		return err
	}
	defer rows.Close()
	r.Expiring = []ReportExpiringAccount{}
	for rows.Next() {
		var a ReportExpiringAccount
		if err := rows.Scan(&a.ID, &a.Label, &a.Owner, &a.ExpiryAt, &a.Status, &a.DataUsedBytes, &a.DataQuotaBytes); err != nil {
			return err
		}
		a.ResellerID = resellerIDOf(a.Owner)
		r.Expiring = append(r.Expiring, a)
	}
	return rows.Err()
}

func (q reportQuery) topAccounts(r *Report) error {
	tbl, ts, rx, tx := q.trafficSource()
	rows, err := q.s.pool.Query(q.ctx, `
		SELECT a.id, a.label, a.owner_key_namespace, sum(t.`+rx+`)::bigint AS rx, sum(t.`+tx+`)::bigint AS tx
		FROM `+tbl+` t JOIN accounts a ON a.id = t.account_id
		WHERE `+reportScopeSQL("a")+` AND t.`+ts+` >= $3
		GROUP BY a.id, a.label, a.owner_key_namespace
		ORDER BY sum(t.`+rx+`) + sum(t.`+tx+`) DESC LIMIT 10`, q.ns, q.owner, q.from)
	if err != nil {
		return err
	}
	defer rows.Close()
	r.TopAccounts = []ReportTopAccount{}
	for rows.Next() {
		var a ReportTopAccount
		if err := rows.Scan(&a.ID, &a.Label, &a.Owner, &a.RxBytes, &a.TxBytes); err != nil {
			return err
		}
		a.ResellerID = resellerIDOf(a.Owner)
		r.TopAccounts = append(r.TopAccounts, a)
	}
	return rows.Err()
}
