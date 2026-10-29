package httpapi

import "wgpanel-api/internal/store"

// createAuditDetail records the lifecycle choices on the creation audit row (nil when
// there is nothing beyond the plain account, keeping old rows' shape).
func createAuditDetail(templateID *string, a store.Account) map[string]any {
	d := map[string]any{}
	if templateID != nil && *templateID != "" {
		d["template_id"] = *templateID
	}
	if a.OnHold() {
		d["on_hold"] = true
	}
	if a.ResetStrategy != store.ResetNone {
		d["reset_strategy"] = a.ResetStrategy
	}
	if len(d) == 0 {
		return nil
	}
	return d
}
