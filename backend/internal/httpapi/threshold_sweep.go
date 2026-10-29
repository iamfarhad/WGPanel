package httpapi

import (
	"context"
	"time"

	"wgpanel-api/internal/events"
)

const (
	expiringSoonLead      = 3 * 24 * time.Hour
	resellerExpiringLead  = 7 * 24 * time.Hour
	resellerCreditLowPct  = 90 // allocated share of the data credit at which it is "low"
	quotaWarnPct          = 80
	pruneEveryNSweeps     = 60
	notificationRetention = 30 * 24 * time.Hour
	deliveryRetention     = 14 * 24 * time.Hour
)

func (s *Server) runThresholdSweepLoop(ctx context.Context) {
	ticker := time.NewTicker(thresholdSweepEvery)
	defer ticker.Stop()
	n := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.runThresholdSweep(ctx, time.Now()); err != nil {
				s.Logger.Error("threshold_sweep_failed", "error", err)
			}
			s.runScheduledBackupIfDue(ctx)
			n++
			if n%pruneEveryNSweeps == 0 {
				_ = s.Store.PruneNotifications(ctx, notificationRetention)
				_ = s.Store.PruneDeliveries(ctx, deliveryRetention)
				_ = s.Store.PruneEventState(ctx, 90*24*time.Hour)
			}
		}
	}
}

// transition applies one condition to the edge-trigger state, but only touches the
// database when the condition differs from what is recorded (almost always it does
// not), and returns whether an event should fire / a recovery happened.
func (s *Server) transition(ctx context.Context, active map[string]bool, key string, cond bool) (rose, fell bool) {
	if cond == active[key] {
		return false, false
	}
	rose, fell, err := s.Store.EventTransition(ctx, key, cond, eventDedupeWindow)
	if err != nil {
		s.Logger.Error("event_transition_failed", "key", key, "error", err)
		return false, false
	}
	return rose, fell
}

// runThresholdSweep evaluates every threshold condition once and emits events for
// the ones that crossed since the previous sweep: node down/recovered, account quota
// 80%/exhausted, expiring soon/expired, reseller credit low / plan expiring.
func (s *Server) runThresholdSweep(ctx context.Context, now time.Time) error {
	active, err := s.Store.ActiveEventKeys(ctx)
	if err != nil {
		return err
	}

	nodes, err := s.Store.ListNodeHealth(ctx)
	if err != nil {
		return err
	}
	for _, n := range nodes {
		rose, fell := s.transition(ctx, active, "node:"+n.ID+":down", n.Status == "offline")
		data := map[string]any{"node_id": n.ID, "node_name": n.Name}
		if rose {
			s.emit(events.NodeDown, "", data)
		}
		if fell {
			s.emit(events.NodeRecovered, "", data)
		}
	}

	accounts, err := s.Store.ListAccountThresholdRows(ctx)
	if err != nil {
		return err
	}
	for _, a := range accounts {
		data := map[string]any{"account_id": a.ID, "label": a.Label}
		if a.ExternalRef != nil {
			data["external_ref"] = *a.ExternalRef
		}
		if a.DataQuotaBytes != nil && *a.DataQuotaBytes > 0 {
			q := *a.DataQuotaBytes
			data["quota_bytes"], data["used_bytes"] = q, a.DataUsedBytes
			exhausted := a.DataUsedBytes >= q
			warn := !exhausted && a.DataUsedBytes*100 >= q*quotaWarnPct
			if rose, _ := s.transition(ctx, active, "account:"+a.ID+":quota80", warn); rose {
				s.emit(events.AccountQuota80, a.Namespace, cloneData(data))
			}
			if rose, _ := s.transition(ctx, active, "account:"+a.ID+":quota100", exhausted); rose {
				s.emit(events.AccountQuotaGone, a.Namespace, cloneData(data))
			}
		}
		if a.ExpiryAt != nil {
			data["expiry_at"] = a.ExpiryAt.UTC().Format(time.RFC3339)
			expired := !a.ExpiryAt.After(now)
			soon := !expired && a.ExpiryAt.Before(now.Add(expiringSoonLead))
			if rose, _ := s.transition(ctx, active, "account:"+a.ID+":expiring", soon); rose {
				s.emit(events.AccountExpiringSoon, a.Namespace, cloneData(data))
			}
			if rose, _ := s.transition(ctx, active, "account:"+a.ID+":expired", expired); rose {
				s.emit(events.AccountExpired, a.Namespace, cloneData(data))
			}
		}
	}

	resellers, err := s.Store.ListResellers(ctx)
	if err != nil {
		return err
	}
	for _, r := range resellers {
		ns := "reseller:" + r.AdminID
		data := map[string]any{"reseller_id": r.AdminID, "reseller_username": r.Username}
		if r.DataLimitBytes != nil && *r.DataLimitBytes > 0 {
			pct := r.Usage.DataAllocatedBytes * 100 / *r.DataLimitBytes
			low := !r.Disabled && pct >= resellerCreditLowPct
			if rose, _ := s.transition(ctx, active, "reseller:"+r.AdminID+":credit", low); rose {
				d := cloneData(data)
				d["percent"] = pct
				d["allocated_bytes"], d["limit_bytes"] = r.Usage.DataAllocatedBytes, *r.DataLimitBytes
				s.emit(events.ResellerCreditLow, ns, d)
			}
		}
		if r.ExpiresAt != nil {
			soon := !r.Disabled && r.ExpiresAt.After(now) && r.ExpiresAt.Before(now.Add(resellerExpiringLead))
			if rose, _ := s.transition(ctx, active, "reseller:"+r.AdminID+":plan", soon); rose {
				d := cloneData(data)
				d["expires_at"] = r.ExpiresAt.UTC().Format(time.RFC3339)
				s.emit(events.ResellerPlanExpires, ns, d)
			}
		}
	}
	return nil
}

func cloneData(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
