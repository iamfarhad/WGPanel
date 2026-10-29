package store

import "sort"

// nodes lists nodes with traffic from the SCOPED accounts only. Staff with no filter
// see every node; anyone scoped sees only nodes their accounts have a peer on or
// moved traffic through - never another namespace's load. CPU/RAM averages come from
// node_metrics and are returned only when metrics is true (staff).
func (q reportQuery) nodes(metrics bool) ([]ReportNode, error) {
	tbl, ts, rx, tx := q.trafficSource()
	allNodes := q.ns == nil && q.owner == ""
	rows, err := q.s.pool.Query(q.ctx, `
		WITH t AS (
			SELECT t.node_id, sum(t.`+rx+`)::bigint AS rx, sum(t.`+tx+`)::bigint AS tx
			FROM `+tbl+` t JOIN accounts a ON a.id = t.account_id
			WHERE `+reportScopeSQL("a")+` AND t.`+ts+` >= $3 GROUP BY 1),
		c AS (
			SELECT ap.node_id, count(DISTINCT ap.account_id)::int AS n
			FROM account_peers ap JOIN accounts a ON a.id = ap.account_id
			WHERE a.status != 'deleted' AND `+reportScopeSQL("a")+` GROUP BY 1),
		m AS (
			SELECT node_id, avg(cpu_percent)::float8 AS cpu,
			       (avg(mem_used_bytes::float8 / NULLIF(mem_total_bytes, 0)) * 100)::float8 AS mem
			FROM node_metrics WHERE $4::bool AND ts >= $3 GROUP BY 1)
		SELECT n.id, n.name, n.status, COALESCE(t.rx, 0), COALESCE(t.tx, 0), COALESCE(c.n, 0), m.cpu, m.mem
		FROM nodes n
		LEFT JOIN t ON t.node_id = n.id
		LEFT JOIN c ON c.node_id = n.id
		LEFT JOIN m ON m.node_id = n.id
		WHERE $5::bool OR t.node_id IS NOT NULL OR c.node_id IS NOT NULL
		ORDER BY COALESCE(t.rx, 0) + COALESCE(t.tx, 0) DESC, n.name
		LIMIT 100`, q.ns, q.owner, q.from, metrics, allNodes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ReportNode{}
	for rows.Next() {
		var n ReportNode
		if err := rows.Scan(&n.ID, &n.Name, &n.Status, &n.RxBytes, &n.TxBytes, &n.Accounts, &n.CPUAvg, &n.MemAvgPercent); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// resellers is the super-admin leaderboard: every reseller with its allocation and
// the traffic its accounts moved in the range, busiest first.
func (q reportQuery) resellers() ([]ReportReseller, error) {
	list, err := q.s.ListResellers(q.ctx)
	if err != nil {
		return nil, err
	}
	tbl, ts, rx, tx := q.trafficSource()
	rows, err := q.s.pool.Query(q.ctx, `
		SELECT a.owner_key_namespace, sum(t.`+rx+`)::bigint, sum(t.`+tx+`)::bigint
		FROM `+tbl+` t JOIN accounts a ON a.id = t.account_id
		WHERE a.owner_key_namespace LIKE 'reseller:%' AND t.`+ts+` >= $1
		GROUP BY 1`, q.from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type pair struct{ rx, tx int64 }
	traffic := map[string]pair{}
	for rows.Next() {
		var ns string
		var p pair
		if err := rows.Scan(&ns, &p.rx, &p.tx); err != nil {
			return nil, err
		}
		traffic[ns] = p
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]ReportReseller, 0, len(list))
	for _, r := range list {
		t := traffic[ResellerNamespace(r.AdminID)]
		out = append(out, ReportReseller{
			ID: r.AdminID, Username: r.Username, Disabled: r.Disabled, ExpiresAt: r.ExpiresAt,
			Accounts: r.Usage.AccountsUsed, DataAllocatedBytes: r.Usage.DataAllocatedBytes,
			DataLimitBytes: r.DataLimitBytes, RxBytes: t.rx, TxBytes: t.tx,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].RxBytes+out[i].TxBytes > out[j].RxBytes+out[j].TxBytes })
	return out, nil
}
