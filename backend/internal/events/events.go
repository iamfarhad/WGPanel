// Package events is the panel's internal domain-event bus. Request handlers and
// sweeps call Bus.Emit, which only enqueues onto a buffered channel (never blocks);
// a worker started by httpapi drains it into in-panel notifications, notification
// channels and webhooks.
package events

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"time"
)

// Event type names. Keep in sync with frontend/src/lib/notificationEvents.ts.
const (
	NodeDown            = "node.down"
	NodeRecovered       = "node.recovered"
	AccountCreated      = "account.created"
	AccountDeleted      = "account.deleted"
	AccountSuspended    = "account.suspended"
	AccountEnabled      = "account.enabled"
	AccountQuota80      = "account.quota_80"
	AccountQuotaGone    = "account.quota_exhausted"
	AccountExpiringSoon = "account.expiring_soon"
	AccountExpired      = "account.expired"
	ResellerCreditLow   = "reseller.credit_low"
	ResellerPlanExpires = "reseller.plan_expiring"
	BackupSucceeded     = "backup.succeeded"
	BackupFailed        = "backup.failed"
	// Abuse controls (migration 0033). Keep in sync with notificationEvents.ts.
	AccountSharingSuspected = "account.sharing_suspected"
	AccountTorrentDetected  = "account.torrent_detected"
)

// Test pings are not part of All - they are delivered only to the endpoint tested.
const Ping = "ping"

const (
	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityCritical = "critical"
)

// Meta describes one event type.
type Meta struct {
	Type     string
	Severity string
	// InPanel: whether the event produces an in-panel notification. Lifecycle events a
	// human just triggered (created/deleted/suspended/enabled) would only add noise
	// to the bell, so they go to webhooks and subscribed channels only.
	InPanel bool
}

var catalog = []Meta{
	{NodeDown, SeverityCritical, true},
	{NodeRecovered, SeverityInfo, true},
	{AccountCreated, SeverityInfo, false},
	{AccountDeleted, SeverityInfo, false},
	{AccountSuspended, SeverityWarning, false},
	{AccountEnabled, SeverityInfo, false},
	{AccountQuota80, SeverityWarning, true},
	{AccountQuotaGone, SeverityWarning, true},
	{AccountExpiringSoon, SeverityWarning, true},
	{AccountExpired, SeverityWarning, true},
	{ResellerCreditLow, SeverityWarning, true},
	{ResellerPlanExpires, SeverityWarning, true},
	{BackupSucceeded, SeverityInfo, true},
	{BackupFailed, SeverityCritical, true},
	{AccountSharingSuspected, SeverityWarning, true},
	{AccountTorrentDetected, SeverityWarning, true},
}

// All returns every subscribable event type.
func All() []string {
	out := make([]string, len(catalog))
	for i, m := range catalog {
		out[i] = m.Type
	}
	return out
}

// Lookup returns the metadata of an event type.
func Lookup(t string) (Meta, bool) {
	for _, m := range catalog {
		if m.Type == t {
			return m, true
		}
	}
	return Meta{}, false
}

// Valid reports whether t is a subscribable event type.
func Valid(t string) bool { _, ok := Lookup(t); return ok }

// Event is one domain event. Namespace is "" for global events (nodes, backups) and
// the owner namespace ("reseller:<id>", or an API key id) for account/reseller events;
// resellers only ever see events whose Namespace is their own.
type Event struct {
	ID        string
	Type      string
	Namespace string
	CreatedAt time.Time
	// Data is the event-specific payload (ids, names, numbers) - never secrets.
	Data map[string]any
}

// NewID returns a random event id.
func NewID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "evt_" + hex.EncodeToString(b)
}

// Bus is a bounded, non-blocking event queue.
type Bus struct {
	ch     chan Event
	logger *slog.Logger
}

// NewBus returns a Bus buffering up to size events.
func NewBus(size int, logger *slog.Logger) *Bus {
	return &Bus{ch: make(chan Event, size), logger: logger}
}

// Emit enqueues e without ever blocking the caller; when the buffer is full the event
// is dropped and logged. Safe on a nil Bus (tests that build a bare Server).
func (b *Bus) Emit(typ, namespace string, data map[string]any) {
	if b == nil {
		return
	}
	ev := Event{ID: NewID(), Type: typ, Namespace: namespace, CreatedAt: time.Now().UTC(), Data: data}
	select {
	case b.ch <- ev:
	default:
		if b.logger != nil {
			b.logger.Warn("event_dropped_queue_full", "type", typ)
		}
	}
}

// C is the receive side, consumed by the dispatcher.
func (b *Bus) C() <-chan Event { return b.ch }
