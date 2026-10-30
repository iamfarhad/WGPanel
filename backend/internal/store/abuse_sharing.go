package store

import (
	"context"
	"net"
	"net/netip"
	"sort"
	"time"
)

// Sharing heuristic (migration 0033). An account that is active at the same moment from
// several unrelated networks is most likely handed around. "Unrelated" = different IPv4
// /24 (IPv6 /48): two devices at one home, or one phone moving between two addresses of
// the same carrier pool, share a network and count once; a /24 is the usual unit an ISP
// hands to one site, and a /48 is the standard IPv6 site prefix.
//
// It stays a heuristic by construction. CGNAT can put strangers in one /24 (under-count)
// and a roaming mobile user can cross several carrier ranges in minutes (over-count), so
// it is a flag for a human plus an event, never an automatic suspension.

// sharingEventDebounce bounds how often one account can re-emit account.sharing_suspected
// even if it flaps around the threshold: the flag itself is edge-triggered (set once,
// cleared when the account drops below N), and this window additionally suppresses a
// second event for an account that cleared and re-crossed shortly after.
const sharingEventDebounce = 6 * time.Hour

// NetworkOfEndpoint maps a WireGuard endpoint ("ip:port" or "[v6]:port") to its sharing
// network (a.b.c.0/24 or the /48) and the bare IP. IPv4-mapped IPv6 addresses count as
// IPv4. ok is false for anything that does not parse as an address.
func NetworkOfEndpoint(endpoint string) (network, ip string, ok bool) {
	host, _, err := net.SplitHostPort(endpoint)
	if err != nil {
		host = endpoint // tolerate a bare address
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return "", "", false
	}
	addr = addr.Unmap().WithZone("")
	bits := 48
	if addr.Is4() {
		bits = 24
	}
	prefix, err := addr.Prefix(bits)
	if err != nil {
		return "", "", false
	}
	return prefix.String(), addr.String(), true
}

// SharingDevice is one recent sighting fed to GroupSharingNetworks.
type SharingDevice struct {
	Endpoint string
	NodeName string
	LastSeen time.Time
}

// SharingNetwork is one distinct network an account was seen from.
type SharingNetwork struct {
	Network  string
	IPs      []string
	Nodes    []string
	LastSeen time.Time
}

// GroupSharingNetworks buckets sightings by network, newest network first. Pure, so the
// heuristic is unit-testable without a database.
func GroupSharingNetworks(devs []SharingDevice) []SharingNetwork {
	byNet := map[string]*SharingNetwork{}
	ipSeen := map[string]map[string]bool{}
	nodeSeen := map[string]map[string]bool{}
	for _, d := range devs {
		network, ip, ok := NetworkOfEndpoint(d.Endpoint)
		if !ok {
			continue
		}
		n := byNet[network]
		if n == nil {
			n = &SharingNetwork{Network: network}
			byNet[network] = n
			ipSeen[network], nodeSeen[network] = map[string]bool{}, map[string]bool{}
		}
		if !ipSeen[network][ip] {
			ipSeen[network][ip] = true
			n.IPs = append(n.IPs, ip)
		}
		if d.NodeName != "" && !nodeSeen[network][d.NodeName] {
			nodeSeen[network][d.NodeName] = true
			n.Nodes = append(n.Nodes, d.NodeName)
		}
		if d.LastSeen.After(n.LastSeen) {
			n.LastSeen = d.LastSeen
		}
	}
	out := make([]SharingNetwork, 0, len(byNet))
	for _, n := range byNet {
		sort.Strings(n.IPs)
		sort.Strings(n.Nodes)
		out = append(out, *n)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].LastSeen.Equal(out[j].LastSeen) {
			return out[i].LastSeen.After(out[j].LastSeen)
		}
		return out[i].Network < out[j].Network
	})
	return out
}

// AccountSharingNetworks is the read side for the account detail view: the distinct
// networks within the window, scoped to callerNamespace.
func (s *Store) AccountSharingNetworks(ctx context.Context, accountID string, callerNamespace *string, window time.Duration) ([]SharingNetwork, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT d.source_endpoint, n.name, d.last_seen_at
		FROM account_devices d
		JOIN nodes n ON n.id = d.node_id
		JOIN accounts ON accounts.id = d.account_id
		WHERE d.account_id = $2 AND `+namespaceFilter+` AND d.last_seen_at > now() - make_interval(secs => $3)
	`, callerNamespace, accountID, window.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var devs []SharingDevice
	for rows.Next() {
		var d SharingDevice
		if err := rows.Scan(&d.Endpoint, &d.NodeName, &d.LastSeen); err != nil {
			return nil, err
		}
		devs = append(devs, d)
	}
	return GroupSharingNetworks(devs), rows.Err()
}

// SharingEvent is a newly raised suspicion the caller should announce.
type SharingEvent struct {
	AccountID   string
	Label       string
	ExternalRef *string
	Namespace   string
	Networks    []string
}

// EvaluateSharing recomputes the sharing flag for every account that could cross the
// threshold. Only accounts with at least minNetworks recent endpoints are even loaded
// (fewer endpoints can never make that many networks). It sets sharing_suspected_at on
// the way up (once), clears it on the way down, and returns an event only for accounts
// that rose AND are outside the debounce window.
func (s *Store) EvaluateSharing(ctx context.Context, minNetworks int, window time.Duration) ([]SharingEvent, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT d.account_id, d.source_endpoint, d.last_seen_at
		FROM account_devices d
		JOIN accounts a ON a.id = d.account_id AND a.status != 'deleted'
		WHERE d.last_seen_at > now() - make_interval(secs => $1)
		  AND d.account_id IN (
		      SELECT account_id FROM account_devices
		      WHERE last_seen_at > now() - make_interval(secs => $1)
		      GROUP BY account_id HAVING count(*) >= $2)
	`, window.Seconds(), minNetworks)
	if err != nil {
		return nil, err
	}
	perAccount := map[string][]SharingDevice{}
	for rows.Next() {
		var id string
		var d SharingDevice
		if err := rows.Scan(&id, &d.Endpoint, &d.LastSeen); err != nil {
			rows.Close()
			return nil, err
		}
		perAccount[id] = append(perAccount[id], d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	suspected := []string{}
	networksOf := map[string][]string{}
	for id, devs := range perAccount {
		nets := GroupSharingNetworks(devs)
		if len(nets) >= minNetworks {
			suspected = append(suspected, id)
			for _, n := range nets {
				networksOf[id] = append(networksOf[id], n.Network)
			}
		}
	}

	if _, err := s.pool.Exec(ctx, `
		UPDATE accounts SET sharing_suspected_at = NULL
		WHERE sharing_suspected_at IS NOT NULL AND NOT (id = ANY($1::uuid[]))
	`, suspected); err != nil {
		return nil, err
	}
	rose, err := s.pool.Query(ctx, `
		UPDATE accounts SET sharing_suspected_at = now()
		WHERE id = ANY($1::uuid[]) AND sharing_suspected_at IS NULL
		RETURNING id, label, external_ref, COALESCE(owner_key_namespace, '')
	`, suspected)
	if err != nil {
		return nil, err
	}
	var candidates []SharingEvent
	for rose.Next() {
		var e SharingEvent
		if err := rose.Scan(&e.AccountID, &e.Label, &e.ExternalRef, &e.Namespace); err != nil {
			rose.Close()
			return nil, err
		}
		e.Networks = networksOf[e.AccountID]
		candidates = append(candidates, e)
	}
	rose.Close()
	if err := rose.Err(); err != nil {
		return nil, err
	}

	var events []SharingEvent
	for _, c := range candidates {
		fire, err := s.FireDebounced(ctx, "abuse:sharing:"+c.AccountID, sharingEventDebounce)
		if err != nil {
			return events, err
		}
		if fire {
			events = append(events, c)
		}
	}
	return events, nil
}

// ClearSharingFlags drops every standing flag - used when the detector is switched off,
// so stale "possibly shared" badges do not outlive the feature.
func (s *Store) ClearSharingFlags(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `UPDATE accounts SET sharing_suspected_at = NULL WHERE sharing_suspected_at IS NOT NULL`)
	return err
}
