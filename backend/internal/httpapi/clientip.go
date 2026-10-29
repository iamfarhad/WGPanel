package httpapi

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// clientIP returns the real client address. The API sits behind Caddy, which
// terminates TLS and (reverse_proxy with no trusted_proxies configured) REPLACES any
// client-supplied X-Forwarded-For with the connecting peer's address. So the header is
// only believed when the TCP peer itself is a private/loopback address (i.e. the proxy
// on the Docker network or localhost); a direct public connection can never spoof it.
// The last X-Forwarded-For element is used, as the entry appended by the nearest proxy.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, perr := netip.ParseAddr(host)
	if perr == nil && (peer.IsLoopback() || peer.IsPrivate() || peer.IsLinkLocalUnicast()) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			last := strings.TrimSpace(parts[len(parts)-1])
			if a, err := netip.ParseAddr(last); err == nil {
				return a.Unmap().String()
			}
		}
	}
	if perr == nil {
		return peer.Unmap().String()
	}
	return host
}

// parseAllowlist validates and normalises IP / CIDR entries ("10.0.0.0/8", "203.0.113.5").
// A bare IP becomes a /32 or /128 prefix.
func parseAllowlist(entries []string) ([]netip.Prefix, []string, error) {
	prefixes := make([]netip.Prefix, 0, len(entries))
	norm := make([]string, 0, len(entries))
	seen := map[string]bool{}
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		var p netip.Prefix
		if strings.Contains(e, "/") {
			pp, err := netip.ParsePrefix(e)
			if err != nil {
				return nil, nil, fmt.Errorf("%q is not a valid IP address or CIDR range", e)
			}
			p = pp.Masked()
		} else {
			a, err := netip.ParseAddr(e)
			if err != nil {
				return nil, nil, fmt.Errorf("%q is not a valid IP address or CIDR range", e)
			}
			a = a.Unmap()
			p = netip.PrefixFrom(a, a.BitLen())
		}
		if s := p.String(); !seen[s] {
			seen[s] = true
			prefixes = append(prefixes, p)
			norm = append(norm, s)
		}
	}
	return prefixes, norm, nil
}

func ipInPrefixes(ip string, prefixes []netip.Prefix) bool {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	a = a.Unmap()
	for _, p := range prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// securityRuntime caches the security settings for a few seconds so the allowlist
// check on every admin request does not cost a database round-trip.
type securityRuntime struct {
	mu       sync.Mutex
	loadedAt time.Time
	prefixes []netip.Prefix
	maxFail  int
	lockMin  int
}

const securityCacheTTL = 5 * time.Second

func (s *Server) loadSecurity(ctx context.Context) (prefixes []netip.Prefix, maxFail, lockMin int) {
	s.sec.mu.Lock()
	defer s.sec.mu.Unlock()
	if !s.sec.loadedAt.IsZero() && time.Since(s.sec.loadedAt) < securityCacheTTL {
		return s.sec.prefixes, s.sec.maxFail, s.sec.lockMin
	}
	cfg, err := s.Store.GetSecuritySettings(ctx)
	if err != nil {
		s.Logger.Error("load_security_settings_failed", "error", err)
		if !s.sec.loadedAt.IsZero() {
			return s.sec.prefixes, s.sec.maxFail, s.sec.lockMin
		}
		// Fail open on the allowlist (the database being down breaks everything else
		// anyway) and fall back to the documented lockout defaults.
		return nil, 5, 15
	}
	pfx, _, _ := parseAllowlist(cfg.AdminIPAllowlist)
	s.sec.prefixes, s.sec.maxFail, s.sec.lockMin = pfx, cfg.LoginMaxFailures, cfg.LoginLockoutMinutes
	s.sec.loadedAt = time.Now()
	return s.sec.prefixes, s.sec.maxFail, s.sec.lockMin
}

func (s *Server) invalidateSecurityCache() {
	s.sec.mu.Lock()
	s.sec.loadedAt = time.Time{}
	s.sec.mu.Unlock()
}

// enforceAdminIPAllowlist answers 403 ip_not_allowed and returns false when the admin
// allowlist is set and the caller's IP is not on it. Used only on the admin login flow
// and JWT-authenticated admin routes - never on subscription, node-agent or API-key
// routes.
func (s *Server) enforceAdminIPAllowlist(w http.ResponseWriter, r *http.Request) bool {
	if s.Store == nil {
		return true
	}
	prefixes, _, _ := s.loadSecurity(r.Context())
	if len(prefixes) == 0 || ipInPrefixes(clientIP(r), prefixes) {
		return true
	}
	writeJSONError(w, http.StatusForbidden, "ip_not_allowed", "admin access is not permitted from your IP address")
	return false
}
