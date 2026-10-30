package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Pricing (migration 0030). Prices sit in their own table keyed by template id, so the
// plan_templates schema and its handlers are unchanged. Amounts are integer Toman.

var ErrInvalidPrice = errors.New("price must be a non-negative whole number of Toman")

// TemplatePrice is the retail price row of one template.
type TemplatePrice struct {
	TemplateID  string
	PriceToman  int64
	Purchasable bool
	UpdatedAt   time.Time
}

// ListTemplatePrices returns the price rows of every template visible to callerNamespace
// (same visibility rule as ListTemplates; nil = staff sees all).
func (s *Store) ListTemplatePrices(ctx context.Context, callerNamespace *string) ([]TemplatePrice, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT p.template_id, p.price_toman, p.purchasable, p.updated_at
		FROM template_prices p JOIN plan_templates t ON t.id = p.template_id
		WHERE `+templateVisibleAs("t")+`
		ORDER BY p.template_id`, callerNamespace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TemplatePrice{}
	for rows.Next() {
		var p TemplatePrice
		if err := rows.Scan(&p.TemplateID, &p.PriceToman, &p.Purchasable, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// templateVisibleAs is templateVisible with a table alias, for joins.
func templateVisibleAs(alias string) string {
	return `($1::text IS NULL OR ` + alias + `.owner_namespace = $1 OR (` + alias + `.owner_namespace IS NULL AND ` + alias + `.available_to_resellers))`
}

// SetTemplatePrice upserts a template's price. Write scope follows templateWritable:
// staff may price any template, a namespaced caller only its own - so a reseller can
// price its own plans for its customers but never a global one.
func (s *Store) SetTemplatePrice(ctx context.Context, templateID string, callerNamespace *string, priceToman int64, purchasable bool) (TemplatePrice, error) {
	if priceToman < 0 {
		return TemplatePrice{}, ErrInvalidPrice
	}
	var ok bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM plan_templates WHERE id = $2 AND `+templateWritable+`)`,
		callerNamespace, templateID).Scan(&ok); err != nil {
		return TemplatePrice{}, err
	}
	if !ok {
		return TemplatePrice{}, ErrTemplateNotFound
	}
	var p TemplatePrice
	err := s.pool.QueryRow(ctx, `
		INSERT INTO template_prices (template_id, price_toman, purchasable) VALUES ($1, $2, $3)
		ON CONFLICT (template_id) DO UPDATE SET price_toman = EXCLUDED.price_toman,
			purchasable = EXCLUDED.purchasable, updated_at = now()
		RETURNING template_id, price_toman, purchasable, updated_at`,
		templateID, priceToman, purchasable).Scan(&p.TemplateID, &p.PriceToman, &p.Purchasable, &p.UpdatedAt)
	return p, err
}

// ---- per-reseller pricing ---------------------------------------------------------

type ResellerPricing struct {
	AdminID         string
	DiscountPercent int
	ChargeWallet    bool
	SelfService     bool
	// Overrides maps template id -> this reseller's price; it wins over the discount.
	Overrides map[string]int64
}

// GetResellerPricing never fails for a reseller without a row: it returns the defaults
// (no discount, no wallet charging, no self-service).
func (s *Store) GetResellerPricing(ctx context.Context, adminID string) (ResellerPricing, error) {
	rp := ResellerPricing{AdminID: adminID, Overrides: map[string]int64{}}
	err := s.pool.QueryRow(ctx, `SELECT discount_percent, charge_wallet, self_service FROM reseller_pricing WHERE admin_id = $1`,
		adminID).Scan(&rp.DiscountPercent, &rp.ChargeWallet, &rp.SelfService)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return rp, err
	}
	rows, err := s.pool.Query(ctx, `SELECT template_id, price_toman FROM reseller_template_prices WHERE admin_id = $1`, adminID)
	if err != nil {
		return rp, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var p int64
		if err := rows.Scan(&id, &p); err != nil {
			return rp, err
		}
		rp.Overrides[id] = p
	}
	return rp, rows.Err()
}

// SaveResellerPricing replaces a reseller's pricing (settings row and every override)
// in one transaction. The caller has verified adminID is a reseller.
func (s *Store) SaveResellerPricing(ctx context.Context, rp ResellerPricing) error {
	if rp.DiscountPercent < 0 || rp.DiscountPercent > 100 {
		return ErrInvalidPrice
	}
	for _, p := range rp.Overrides {
		if p < 0 {
			return ErrInvalidPrice
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `
		INSERT INTO reseller_pricing (admin_id, discount_percent, charge_wallet, self_service) VALUES ($1, $2, $3, $4)
		ON CONFLICT (admin_id) DO UPDATE SET discount_percent = EXCLUDED.discount_percent,
			charge_wallet = EXCLUDED.charge_wallet, self_service = EXCLUDED.self_service, updated_at = now()`,
		rp.AdminID, rp.DiscountPercent, rp.ChargeWallet, rp.SelfService); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM reseller_template_prices WHERE admin_id = $1`, rp.AdminID); err != nil {
		return err
	}
	for id, p := range rp.Overrides {
		// A template deleted meanwhile (or never global) simply cannot be priced.
		if _, err := tx.Exec(ctx, `
			INSERT INTO reseller_template_prices (admin_id, template_id, price_toman)
			SELECT $1, id, $3 FROM plan_templates WHERE id = $2 AND owner_namespace IS NULL`,
			rp.AdminID, id, p); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ResellerPriceFor is what a reseller pays for one global template: its override if
// there is one, else the base price less its discount (rounded to the nearest Toman).
// charged is false - and nothing must be debited - when the reseller does not have wallet
// charging switched on, the template is not global (a reseller's own plans are priced
// for ITS customers, charging itself for them would be nonsense) or it has no price.
func (s *Store) ResellerPriceFor(ctx context.Context, adminID, templateID string) (price int64, charged bool, err error) {
	rp, err := s.GetResellerPricing(ctx, adminID)
	if err != nil || !rp.ChargeWallet {
		return 0, false, err
	}
	var global bool
	if err := s.pool.QueryRow(ctx, `SELECT owner_namespace IS NULL FROM plan_templates WHERE id = $1`, templateID).Scan(&global); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}
	if !global {
		return 0, false, nil
	}
	if o, ok := rp.Overrides[templateID]; ok {
		return o, o > 0, nil
	}
	var base int64
	if err := s.pool.QueryRow(ctx, `SELECT price_toman FROM template_prices WHERE template_id = $1`, templateID).Scan(&base); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}
	price = DiscountedPrice(base, rp.DiscountPercent)
	return price, price > 0, nil
}

// DiscountedPrice applies a percentage discount in integer arithmetic, rounding half up.
func DiscountedPrice(base int64, percent int) int64 {
	if percent <= 0 {
		return base
	}
	return (base*int64(100-percent) + 50) / 100
}

// ---- what customers can buy -------------------------------------------------------

// PricedTemplate is a template together with its retail price.
type PricedTemplate struct {
	Template   PlanTemplate
	PriceToman int64
}

// PurchasableTemplates lists what a customer of ownerNamespace may buy: for staff-owned
// accounts (nil) the global templates marked purchasable; for a reseller's customers
// the reseller's OWN purchasable templates, and only while the reseller has enabled
// self-service. Anything else (API-key namespaces) sells nothing.
func (s *Store) PurchasableTemplates(ctx context.Context, ownerNamespace *string) ([]PricedTemplate, error) {
	var rows pgx.Rows
	var err error
	if ownerNamespace == nil {
		rows, err = s.pool.Query(ctx, `
			SELECT `+prefixedTemplateColumns+`, p.price_toman
			FROM plan_templates t JOIN template_prices p ON p.template_id = t.id
			WHERE t.owner_namespace IS NULL AND p.purchasable AND p.price_toman > 0
			ORDER BY lower(t.name), t.id`)
	} else {
		adminID, ok := resellerIDFromNamespace(ownerNamespace)
		if !ok {
			return []PricedTemplate{}, nil
		}
		rows, err = s.pool.Query(ctx, `
			SELECT `+prefixedTemplateColumns+`, p.price_toman
			FROM plan_templates t JOIN template_prices p ON p.template_id = t.id
			WHERE t.owner_namespace = $1 AND p.purchasable AND p.price_toman > 0
			  AND EXISTS (SELECT 1 FROM reseller_pricing rp WHERE rp.admin_id = $2 AND rp.self_service)
			ORDER BY lower(t.name), t.id`, *ownerNamespace, adminID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PricedTemplate{}
	for rows.Next() {
		var pt PricedTemplate
		t := &pt.Template
		if err := rows.Scan(&t.ID, &t.Name, &t.OwnerNamespace, &t.AvailableToResellers, &t.DataQuotaBytes, &t.DurationDays,
			&t.DeviceLimit, &t.BandwidthLimitMbps, &t.NodeGroup, &t.ResetStrategy, &t.OnHold, &t.HoldDeadlineDays,
			&t.CreatedAt, &t.UpdatedAt, &pt.PriceToman); err != nil {
			return nil, err
		}
		out = append(out, pt)
	}
	return out, rows.Err()
}

const prefixedTemplateColumns = `t.id, t.name, t.owner_namespace, t.available_to_resellers, t.data_quota_bytes, t.duration_days, t.device_limit, t.bandwidth_limit_mbps, t.node_group, t.reset_strategy, t.on_hold, t.hold_deadline_days, t.created_at, t.updated_at`

// GetPurchasableTemplate returns one template from PurchasableTemplates' set, with its
// price read from the database now - the amount of an order is always decided here,
// never taken from the buyer.
func (s *Store) GetPurchasableTemplate(ctx context.Context, templateID string, ownerNamespace *string) (PricedTemplate, error) {
	list, err := s.PurchasableTemplates(ctx, ownerNamespace)
	if err != nil {
		return PricedTemplate{}, err
	}
	for _, pt := range list {
		if pt.Template.ID == templateID {
			return pt, nil
		}
	}
	return PricedTemplate{}, ErrTemplateNotFound
}
