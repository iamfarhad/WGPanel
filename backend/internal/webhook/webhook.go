// Package webhook holds the pure parts of outbound webhooks: request signing, the
// retry schedule and the SSRF guard. Persistence and the delivery worker live in
// store and httpapi.
package webhook

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// SignatureHeader carries "t=<unix>,v1=<hex>", where v1 = HMAC-SHA256(secret, t + "." + body).
const SignatureHeader = "X-Jirjirak-Signature"

// Sign returns the signature header value for body at time t.
func Sign(secret string, t time.Time, body []byte) string {
	ts := strconv.FormatInt(t.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(body)
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

// Verify checks a header against body, rejecting timestamps further than tolerance
// from now (replay protection). Receivers can copy this logic.
func Verify(secret, header string, body []byte, now time.Time, tolerance time.Duration) bool {
	var ts, sig string
	for _, part := range strings.Split(header, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch k {
		case "t":
			ts = v
		case "v1":
			sig = v
		}
	}
	unix, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || sig == "" {
		return false
	}
	if d := now.Sub(time.Unix(unix, 0)); d > tolerance || d < -tolerance {
		return false
	}
	want := Sign(secret, time.Unix(unix, 0), body)
	_, wantSig, _ := strings.Cut(want, ",v1=")
	return hmac.Equal([]byte(wantSig), []byte(sig))
}

// NewSecret returns a fresh random signing secret.
func NewSecret() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return "whsec_" + hex.EncodeToString(b)
}

// backoff is the wait before retry n (n = number of attempts already made).
var backoff = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour}

// MaxAttempts is the first try plus one retry per backoff step.
var MaxAttempts = len(backoff) + 1

// NextBackoff returns the delay before the next attempt after `attempts` failed
// attempts, or ok=false when the delivery should be marked failed.
func NextBackoff(attempts int) (time.Duration, bool) {
	if attempts < 1 || attempts > len(backoff) {
		return 0, false
	}
	return backoff[attempts-1], true
}

// ErrBlockedTarget is returned for destinations the SSRF guard refuses.
var ErrBlockedTarget = errors.New("webhook target is not allowed (private, loopback, link-local or metadata address)")

var blockedPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
		"172.16.0.0/12", "192.0.0.0/24", "192.168.0.0/16", "198.18.0.0/15", "224.0.0.0/4", "240.0.0.0/4",
		"::/128", "::1/128", "fc00::/7", "fe80::/10", "ff00::/8", "fd00:ec2::/32", "64:ff9b::/96",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// IsBlockedIP reports whether ip is a destination webhooks may not reach by default.
func IsBlockedIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	addr = addr.Unmap()
	for _, p := range blockedPrefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// ValidateURL checks the syntax and, unless allowPrivate, that a literal-IP host is
// not blocked and that every address the hostname resolves to is allowed. resolve may
// be nil (net.LookupIP).
func ValidateURL(raw string, allowPrivate bool, resolve func(string) ([]net.IP, error)) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return errors.New("url is not valid")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("url must start with http:// or https://")
	}
	if u.User != nil {
		return errors.New("url must not contain credentials")
	}
	if allowPrivate {
		return nil
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		if IsBlockedIP(ip) {
			return ErrBlockedTarget
		}
		return nil
	}
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") ||
		strings.HasSuffix(strings.ToLower(host), ".internal") {
		return ErrBlockedTarget
	}
	if resolve == nil {
		resolve = net.LookupIP
	}
	ips, err := resolve(host)
	if err != nil {
		return fmt.Errorf("could not resolve host: %w", err)
	}
	for _, ip := range ips {
		if IsBlockedIP(ip) {
			return ErrBlockedTarget
		}
	}
	return nil
}

// NewClient returns an HTTP client whose dialer re-checks the *connected* address
// (so DNS rebinding and redirects to internal hosts are caught after resolution).
// allowPrivate is consulted per connection so the staff override takes effect live.
func NewClient(timeout time.Duration, allowPrivate func() bool) *http.Client {
	dialer := &net.Dialer{
		Timeout: 5 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			if allowPrivate != nil && allowPrivate() {
				return nil
			}
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return ErrBlockedTarget
			}
			if ip := net.ParseIP(host); ip == nil || IsBlockedIP(ip) {
				return ErrBlockedTarget
			}
			return nil
		},
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:                 nil, // an env proxy would bypass the dial-time check
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: timeout,
			MaxIdleConns:          10,
			IdleConnTimeout:       30 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return errors.New("redirect to a non-http scheme")
			}
			return nil
		},
	}
}
