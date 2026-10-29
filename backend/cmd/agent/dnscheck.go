package main

import (
	"context"
	"log/slog"
	"net"
	"strings"
	"time"
)

// dnsProbeTimeout bounds each resolver probe (above the forwarder's upstream timeout,
// so probing the node's own forwarder can't time out first); a var so tests can
// shorten it.
var dnsProbeTimeout = 5 * time.Second

// checkClientDNS probes, from this node, the resolvers the panel writes into client
// configs (the heartbeat's client_dns). Client traffic leaves through this node's
// NAT, so a resolver this node can't reach is unreachable for every client too: the
// tunnel connects and IPs work, but no name resolves - "connected, no internet".
// Only warns: a resolver can be briefly down, and one of several failing is
// survivable for clients.
func checkClientDNS(clientDNS string, logger *slog.Logger) {
	servers := dnsServers(clientDNS)
	var unreachable []string
	for _, server := range servers {
		addr := net.JoinHostPort(server, "53")
		// One retry: a resolver's very first answer can be slow (cold cache, first
		// upstream connection), which must not read as "unreachable".
		if probeDNS(addr) != nil && probeDNS(addr) != nil {
			unreachable = append(unreachable, server)
		}
	}
	if len(unreachable) == 0 {
		return
	}
	logger.Warn("client_dns_unreachable",
		"unreachable", unreachable, "configured", servers,
		"all_unreachable", len(unreachable) == len(servers),
		"hint", "clients use these resolvers through this node - in the panel set Settings -> Client DNS to \"each node\" (the default), or to resolvers this server can reach")
}

// dnsServers extracts the resolver IPs from a wg-quick DNS = value, which may also
// carry search domains (not probeable, skipped).
func dnsServers(clientDNS string) []string {
	var servers []string
	for _, entry := range strings.Split(clientDNS, ",") {
		entry = strings.TrimSpace(entry)
		if net.ParseIP(entry) != nil {
			servers = append(servers, entry)
		}
	}
	return servers
}

// probeDNS resolves a well-known name through exactly the resolver at addr, over UDP
// like a client's DNS queries.
func probeDNS(addr string) error {
	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "udp", addr)
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), dnsProbeTimeout)
	defer cancel()
	_, err := r.LookupNetIP(ctx, "ip4", "example.com.")
	return err
}
