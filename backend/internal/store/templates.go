package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Plan templates (migration 0025): named bundles of account settings. Staff templates
// (OwnerNamespace nil) are global; a reseller (or bot key) owns templates in its own
// namespace. Visibility for a namespaced caller: its own templates plus global ones
// flagged AvailableToResellers. Staff (nil namespace) see everything.

var ErrTemplateNotFound = errors.New("template not found")

type PlanTemplate struct {
	ID                   string
	Name                 string
	OwnerNamespace       *string
	AvailableToResellers bool
	DataQuotaBytes       *int64
	DurationDays         *int
	DeviceLimit          *int
	BandwidthLimitMbps   *int
	NodeGroup            *string
	ResetStrategy        string
	OnHold               bool
	HoldDeadlineDays     *int
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

const templateColumns = `id, name, owner_namespace, available_to_resellers, data_quota_bytes, duration_days, device_limit, bandwidth_limit_mbps, node_group, reset_strategy, on_hold, hold_deadline_days, created_at, updated_at`

func scanTemplate(row pgx.Row, t *PlanTemplate) error {
	return row.Scan(&t.ID, &t.Name, &t.OwnerNamespace, &t.AvailableToResellers, &t.DataQuotaBytes, &t.DurationDays,
		&t.DeviceLimit, &t.BandwidthLimitMbps, &t.NodeGroup, &t.ResetStrategy, &t.OnHold, &t.HoldDeadlineDays, &t.CreatedAt, &t.UpdatedAt)
}

// templateVisible is the read-scope fragment; $1 is the caller namespace (NULL = staff).
const templateVisible = `($1::text IS NULL OR owner_namespace = $1 OR (owner_namespace IS NULL AND available_to_resellers))`

// templateWritable is the write-scope fragment: staff may change any template, a
// namespaced caller only its own.
const templateWritable = `($1::text IS NULL OR owner_namespace = $1)`

type TemplateParams struct {
	Name                 string
	AvailableToResellers bool
	DataQuotaBytes       *int64
	DurationDays         *int
	DeviceLimit          *int
	BandwidthLimitMbps   *int
	NodeGroup            *string
	ResetStrategy        string
	OnHold               bool
	HoldDeadlineDays     *int
}

// CreateTemplate stores a template owned by ownerNamespace (nil = global/staff).
// AvailableToResellers only means something on a global template and is forced false
// otherwise.
func (s *Store) CreateTemplate(ctx context.Context, ownerNamespace *string, p TemplateParams) (PlanTemplate, error) {
	if !ValidResetStrategy(p.ResetStrategy) {
		return PlanTemplate{}, ErrInvalidResetStrategy
	}
	if ownerNamespace != nil {
		p.AvailableToResellers = false
	}
	var t PlanTemplate
	err := scanTemplate(s.pool.QueryRow(ctx, `
		INSERT INTO plan_templates (name, owner_namespace, available_to_resellers, data_quota_bytes, duration_days, device_limit,
		                            bandwidth_limit_mbps, node_group, reset_strategy, on_hold, hold_deadline_days)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING `+templateColumns,
		p.Name, ownerNamespace, p.AvailableToResellers, p.DataQuotaBytes, p.DurationDays, p.DeviceLimit,
		p.BandwidthLimitMbps, p.NodeGroup, p.ResetStrategy, p.OnHold, p.HoldDeadlineDays), &t)
	return t, err
}

func (s *Store) ListTemplates(ctx context.Context, callerNamespace *string) ([]PlanTemplate, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+templateColumns+` FROM plan_templates WHERE `+templateVisible+` ORDER BY lower(name), id`, callerNamespace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PlanTemplate{}
	for rows.Next() {
		var t PlanTemplate
		if err := scanTemplate(rows, &t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetTemplate is scoped like ListTemplates; invisible and missing are both not found.
func (s *Store) GetTemplate(ctx context.Context, id string, callerNamespace *string) (PlanTemplate, error) {
	var t PlanTemplate
	err := scanTemplate(s.pool.QueryRow(ctx, `SELECT `+templateColumns+` FROM plan_templates WHERE id = $2 AND `+templateVisible,
		callerNamespace, id), &t)
	if errors.Is(err, pgx.ErrNoRows) {
		return PlanTemplate{}, ErrTemplateNotFound
	}
	return t, err
}

// UpdateTemplate replaces every editable field (a template is small enough that PUT-like
// semantics beat a patch with clear flags for each nullable column).
func (s *Store) UpdateTemplate(ctx context.Context, id string, callerNamespace *string, p TemplateParams) (PlanTemplate, error) {
	if !ValidResetStrategy(p.ResetStrategy) {
		return PlanTemplate{}, ErrInvalidResetStrategy
	}
	var t PlanTemplate
	err := scanTemplate(s.pool.QueryRow(ctx, `
		UPDATE plan_templates SET name = $3,
			available_to_resellers = CASE WHEN owner_namespace IS NULL THEN $4 ELSE false END,
			data_quota_bytes = $5, duration_days = $6, device_limit = $7, bandwidth_limit_mbps = $8,
			node_group = $9, reset_strategy = $10, on_hold = $11, hold_deadline_days = $12, updated_at = now()
		WHERE id = $2 AND `+templateWritable+`
		RETURNING `+templateColumns,
		callerNamespace, id, p.Name, p.AvailableToResellers, p.DataQuotaBytes, p.DurationDays, p.DeviceLimit,
		p.BandwidthLimitMbps, p.NodeGroup, p.ResetStrategy, p.OnHold, p.HoldDeadlineDays), &t)
	if errors.Is(err, pgx.ErrNoRows) {
		return PlanTemplate{}, ErrTemplateNotFound
	}
	return t, err
}

func (s *Store) DeleteTemplate(ctx context.Context, id string, callerNamespace *string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM plan_templates WHERE id = $2 AND `+templateWritable, callerNamespace, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrTemplateNotFound
	}
	return nil
}
