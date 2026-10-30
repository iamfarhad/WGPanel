package store

import "sort"

// resellers is the per-reseller breakdown, as a tree: every reseller with its
// allocation and the traffic its own accounts moved in the range, plus a roll-up of its
// whole subtree (Subtree*). Rows are ordered depth-first, busiest subtree first at each
// level, so a parent is immediately followed by its children.
//
// Scope: staff with no filter see every reseller; a reseller sees itself and its
// descendants (never its parent or siblings); an API key sees none. A reseller whose
// parent is outside the listed set is treated as a root, which is how a reseller's own
// view starts at itself.
func (q reportQuery) resellers() ([]ReportReseller, error) {
	var list []Reseller
	switch {
	case q.ns == nil:
		all, err := q.s.ListResellers(q.ctx)
		if err != nil {
			return nil, err
		}
		list = all
	default:
		id, ok := resellerIDFromNamespace(q.ns)
		if !ok {
			return []ReportReseller{}, nil
		}
		self, err := q.s.GetReseller(q.ctx, id)
		if err != nil {
			return nil, err
		}
		desc, err := q.s.ListDescendants(q.ctx, id)
		if err != nil {
			return nil, err
		}
		if len(desc) == 0 {
			// A reseller with nobody below it has no breakdown to show (its own numbers
			// are the report totals): null, as it always was for resellers.
			return nil, nil
		}
		list = append([]Reseller{self}, desc...)
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

	byID := make(map[string]*ReportReseller, len(list))
	for _, r := range list {
		t := traffic[ResellerNamespace(r.AdminID)]
		byID[r.AdminID] = &ReportReseller{
			ID: r.AdminID, Username: r.Username, Disabled: r.IsDisabled(), ExpiresAt: r.PlanEnd(),
			Accounts: r.Usage.AccountsUsed, DataAllocatedBytes: r.Usage.DataAllocatedBytes,
			DataLimitBytes: r.DataLimitBytes, RxBytes: t.rx, TxBytes: t.tx,
			ParentID: r.ParentID, Depth: r.Depth(), Children: r.Children,
			SubtreeAccounts: r.Usage.AccountsUsed, SubtreeRxBytes: t.rx, SubtreeTxBytes: t.tx,
		}
	}
	// Roll every reseller's own numbers up into each of its ancestors that is listed.
	for _, r := range list {
		own := byID[r.AdminID]
		for _, anc := range r.Ancestors {
			if a, ok := byID[anc[len(ResellerNamespacePrefix):]]; ok {
				a.SubtreeAccounts += own.Accounts
				a.SubtreeRxBytes += own.RxBytes
				a.SubtreeTxBytes += own.TxBytes
			}
		}
	}

	kids := map[string][]*ReportReseller{}
	var roots []*ReportReseller
	for _, r := range byID {
		if r.ParentID != nil {
			if _, ok := byID[*r.ParentID]; ok {
				kids[*r.ParentID] = append(kids[*r.ParentID], r)
				continue
			}
		}
		roots = append(roots, r)
	}
	busiest := func(s []*ReportReseller) {
		sort.SliceStable(s, func(i, j int) bool {
			ti, tj := s[i].SubtreeRxBytes+s[i].SubtreeTxBytes, s[j].SubtreeRxBytes+s[j].SubtreeTxBytes
			if ti != tj {
				return ti > tj
			}
			return s[i].Username < s[j].Username
		})
	}
	out := make([]ReportReseller, 0, len(list))
	var walk func(level []*ReportReseller)
	walk = func(level []*ReportReseller) {
		busiest(level)
		for _, r := range level {
			out = append(out, *r)
			walk(kids[r.ID])
		}
	}
	walk(roots)
	return out, nil
}
