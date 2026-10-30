package store

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"

	"wgpanel-api/internal/obfuscation"
)

// AmneziaWG obfuscation profiles and per-node obfuscation state (migration 0028).
// Kept in its own file, and deliberately not threaded through Node/scanNode: every
// other node feature adds columns there, and the obfuscation state is only needed by
// the handlers that ask for it.

var (
	ErrObfuscationProfileNotFound = errors.New("obfuscation profile not found")
	ErrObfuscationNameTaken       = errors.New("obfuscation profile name already exists")
	ErrObfuscationProfileInUse    = errors.New("obfuscation profile is assigned to nodes")
)

// ObfuscationProfile is one stored profile.
type ObfuscationProfile struct {
	ID        string
	Name      string
	Compat    string
	Params    obfuscation.Params
	Revision  int
	CreatedAt time.Time
	UpdatedAt time.Time
	NodeCount int // nodes the profile is assigned to; filled by the list/get queries
}

const obfProfileColumns = `id, name, compat, jc, jmin, jmax, s1, s2, s3, s4, h1, h2, h3, h4, i1, i2, i3, i4, i5, revision, created_at, updated_at,
	(SELECT count(*) FROM nodes n WHERE n.obfuscation_profile_id = obfuscation_profiles.id)`

func scanObfProfile(row pgx.Row, p *ObfuscationProfile) error {
	q := &p.Params
	return row.Scan(&p.ID, &p.Name, &p.Compat, &q.Jc, &q.Jmin, &q.Jmax, &q.S1, &q.S2, &q.S3, &q.S4,
		&q.H1, &q.H2, &q.H3, &q.H4, &q.I1, &q.I2, &q.I3, &q.I4, &q.I5, &p.Revision, &p.CreatedAt, &p.UpdatedAt, &p.NodeCount)
}

func (s *Store) ListObfuscationProfiles(ctx context.Context) ([]ObfuscationProfile, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+obfProfileColumns+` FROM obfuscation_profiles ORDER BY created_at, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ObfuscationProfile
	for rows.Next() {
		var p ObfuscationProfile
		if err := scanObfProfile(rows, &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) GetObfuscationProfile(ctx context.Context, id string) (ObfuscationProfile, error) {
	var p ObfuscationProfile
	err := scanObfProfile(s.pool.QueryRow(ctx, `SELECT `+obfProfileColumns+` FROM obfuscation_profiles WHERE id = $1`, id), &p)
	if errors.Is(err, pgx.ErrNoRows) || isInvalidUUID(err) {
		return ObfuscationProfile{}, ErrObfuscationProfileNotFound
	}
	return p, err
}

// CreateObfuscationProfile inserts a profile at revision 1. The caller validates params.
func (s *Store) CreateObfuscationProfile(ctx context.Context, name, compat string, q obfuscation.Params) (ObfuscationProfile, error) {
	var p ObfuscationProfile
	err := scanObfProfile(s.pool.QueryRow(ctx, `
		INSERT INTO obfuscation_profiles (name, compat, jc, jmin, jmax, s1, s2, s3, s4, h1, h2, h3, h4, i1, i2, i3, i4, i5)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
		RETURNING `+obfProfileColumns,
		name, compat, q.Jc, q.Jmin, q.Jmax, q.S1, q.S2, q.S3, q.S4, q.H1, q.H2, q.H3, q.H4, q.I1, q.I2, q.I3, q.I4, q.I5), &p)
	if err != nil && isUniqueViolation(err) {
		return ObfuscationProfile{}, ErrObfuscationNameTaken
	}
	return p, err
}

// UpdateObfuscationProfile replaces name/compat/params (nil = unchanged). The revision
// is bumped only when compat or parameters actually change: a rename must not make
// every node reconfigure and every client re-import.
func (s *Store) UpdateObfuscationProfile(ctx context.Context, id string, name, compat *string, params *obfuscation.Params) (ObfuscationProfile, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ObfuscationProfile{}, err
	}
	defer tx.Rollback(ctx)

	var cur ObfuscationProfile
	err = scanObfProfile(tx.QueryRow(ctx, `SELECT `+obfProfileColumns+` FROM obfuscation_profiles WHERE id = $1 FOR UPDATE`, id), &cur)
	if errors.Is(err, pgx.ErrNoRows) || isInvalidUUID(err) {
		return ObfuscationProfile{}, ErrObfuscationProfileNotFound
	}
	if err != nil {
		return ObfuscationProfile{}, err
	}
	next := cur
	if name != nil {
		next.Name = *name
	}
	if compat != nil {
		next.Compat = *compat
	}
	if params != nil {
		next.Params = *params
	}
	if next.Compat != cur.Compat || next.Params != cur.Params {
		next.Revision = cur.Revision + 1
	}
	if err := writeObfProfile(ctx, tx, next); err != nil {
		return ObfuscationProfile{}, err
	}
	next.UpdatedAt = time.Now()
	return next, tx.Commit(ctx)
}

// RotateObfuscationProfile regenerates the profile's parameters (see obfuscation.Rotate)
// and bumps its revision under a row lock, so two concurrent rotations can't both
// derive their result from the same old revision.
func (s *Store) RotateObfuscationProfile(ctx context.Context, id, cps string) (ObfuscationProfile, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ObfuscationProfile{}, err
	}
	defer tx.Rollback(ctx)

	var cur ObfuscationProfile
	err = scanObfProfile(tx.QueryRow(ctx, `SELECT `+obfProfileColumns+` FROM obfuscation_profiles WHERE id = $1 FOR UPDATE`, id), &cur)
	if errors.Is(err, pgx.ErrNoRows) || isInvalidUUID(err) {
		return ObfuscationProfile{}, ErrObfuscationProfileNotFound
	}
	if err != nil {
		return ObfuscationProfile{}, err
	}
	rotated, err := obfuscation.Rotate(cur.Compat, cur.Params, cps)
	if err != nil {
		return ObfuscationProfile{}, err
	}
	cur.Params = rotated
	cur.Revision++
	if err := writeObfProfile(ctx, tx, cur); err != nil {
		return ObfuscationProfile{}, err
	}
	cur.UpdatedAt = time.Now()
	return cur, tx.Commit(ctx)
}

func writeObfProfile(ctx context.Context, tx pgx.Tx, p ObfuscationProfile) error {
	q := p.Params
	_, err := tx.Exec(ctx, `
		UPDATE obfuscation_profiles SET name = $2, compat = $3, jc = $4, jmin = $5, jmax = $6, s1 = $7, s2 = $8, s3 = $9, s4 = $10,
			h1 = $11, h2 = $12, h3 = $13, h4 = $14, i1 = $15, i2 = $16, i3 = $17, i4 = $18, i5 = $19, revision = $20, updated_at = now()
		WHERE id = $1`,
		p.ID, p.Name, p.Compat, q.Jc, q.Jmin, q.Jmax, q.S1, q.S2, q.S3, q.S4, q.H1, q.H2, q.H3, q.H4, q.I1, q.I2, q.I3, q.I4, q.I5, p.Revision)
	if err != nil && isUniqueViolation(err) {
		return ErrObfuscationNameTaken
	}
	return err
}

// DeleteObfuscationProfile refuses while nodes use the profile: silently flipping those
// nodes back to plain WireGuard would break every client config issued for them.
func (s *Store) DeleteObfuscationProfile(ctx context.Context, id string) error {
	p, err := s.GetObfuscationProfile(ctx, id)
	if err != nil {
		return err
	}
	if p.NodeCount > 0 {
		return ErrObfuscationProfileInUse
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM obfuscation_profiles WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrObfuscationProfileNotFound
	}
	return nil
}

// SetNodeObfuscationProfile assigns (or, with nil, clears) a node's profile.
func (s *Store) SetNodeObfuscationProfile(ctx context.Context, nodeID string, profileID *string) error {
	if profileID != nil {
		if _, err := s.GetObfuscationProfile(ctx, *profileID); err != nil {
			return err
		}
	}
	tag, err := s.pool.Exec(ctx, `UPDATE nodes SET obfuscation_profile_id = $2 WHERE id = $1`, nodeID, profileID)
	if err != nil {
		if isInvalidUUID(err) {
			return ErrNodeNotFound
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNodeNotFound
	}
	return nil
}

// SetGroupObfuscationProfile is the bulk form: every node in nodeGroup. It returns how
// many nodes were assigned; zero means no node has that group.
func (s *Store) SetGroupObfuscationProfile(ctx context.Context, nodeGroup string, profileID *string) (int64, error) {
	if profileID != nil {
		if _, err := s.GetObfuscationProfile(ctx, *profileID); err != nil {
			return 0, err
		}
	}
	tag, err := s.pool.Exec(ctx, `UPDATE nodes SET obfuscation_profile_id = $2 WHERE node_group = $1`, nodeGroup, profileID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// NodeObfuscationReport is what the agent says it runs, sent on every heartbeat.
type NodeObfuscationReport struct {
	Mode      string // "wireguard" or "awg"
	ProfileID string // the profile the agent applied; "" when Mode is wireguard
	Revision  int
	Supported bool
	Error     string
}

// RecordNodeObfuscation stores the agent's report. An unknown/deleted profile id is
// stored as NULL rather than failing the heartbeat.
func (s *Store) RecordNodeObfuscation(ctx context.Context, nodeID string, r NodeObfuscationReport) error {
	mode := r.Mode
	if mode != "awg" {
		mode = "wireguard"
	}
	var profileID *string
	var revision *int
	if mode == "awg" && uuidPattern.MatchString(r.ProfileID) {
		profileID, revision = &r.ProfileID, &r.Revision
	}
	errText := r.Error
	if len(errText) > 500 {
		errText = errText[:500]
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE nodes SET
			obfuscation_mode = $2,
			obfuscation_applied_profile_id = (SELECT id FROM obfuscation_profiles WHERE id = $3::uuid),
			obfuscation_applied_revision = $4,
			obfuscation_supported = $5,
			obfuscation_error = $6
		WHERE id = $1`, nodeID, mode, profileID, revision, r.Supported, errText)
	return err
}

// NodeObfuscation is a node's obfuscation state: the assigned profile (intent) and
// what the agent reports applying.
type NodeObfuscation struct {
	NodeID           string
	ProfileID        *string
	ProfileName      *string
	ProfileCompat    *string
	ProfileRevision  *int
	Mode             string
	AppliedProfileID *string
	AppliedRevision  *int
	Supported        bool
	Error            string
}

// Applied reports whether the node really runs its assigned profile at the profile's
// current revision - the only state in which client configs may carry AWG lines.
func (n NodeObfuscation) Applied() bool {
	return n.Mode == "awg" && n.ProfileID != nil && n.AppliedProfileID != nil &&
		*n.ProfileID == *n.AppliedProfileID && n.ProfileRevision != nil && n.AppliedRevision != nil &&
		*n.ProfileRevision == *n.AppliedRevision
}

const nodeObfSelect = `
	SELECT n.id, n.obfuscation_profile_id, p.name, p.compat, p.revision, n.obfuscation_mode,
		n.obfuscation_applied_profile_id, n.obfuscation_applied_revision, n.obfuscation_supported, n.obfuscation_error
	FROM nodes n LEFT JOIN obfuscation_profiles p ON p.id = n.obfuscation_profile_id`

func scanNodeObf(row pgx.Row, n *NodeObfuscation) error {
	return row.Scan(&n.NodeID, &n.ProfileID, &n.ProfileName, &n.ProfileCompat, &n.ProfileRevision, &n.Mode,
		&n.AppliedProfileID, &n.AppliedRevision, &n.Supported, &n.Error)
}

func (s *Store) GetNodeObfuscation(ctx context.Context, nodeID string) (NodeObfuscation, error) {
	var n NodeObfuscation
	err := scanNodeObf(s.pool.QueryRow(ctx, nodeObfSelect+` WHERE n.id = $1`, nodeID), &n)
	if errors.Is(err, pgx.ErrNoRows) || isInvalidUUID(err) {
		return NodeObfuscation{}, ErrNodeNotFound
	}
	return n, err
}

// ListNodeObfuscation returns every node's state keyed by node id (one query for the
// node list instead of one per row).
func (s *Store) ListNodeObfuscation(ctx context.Context) (map[string]NodeObfuscation, error) {
	rows, err := s.pool.Query(ctx, nodeObfSelect)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]NodeObfuscation{}
	for rows.Next() {
		var n NodeObfuscation
		if err := scanNodeObf(rows, &n); err != nil {
			return nil, err
		}
		out[n.NodeID] = n
	}
	return out, rows.Err()
}

// AppliedObfuscationForNode returns the node's profile only if the node really runs it
// (see NodeObfuscation.Applied); nil otherwise. This is the gate client configs use.
func (s *Store) AppliedObfuscationForNode(ctx context.Context, nodeID string) (*ObfuscationProfile, error) {
	state, err := s.GetNodeObfuscation(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	if !state.Applied() {
		return nil, nil
	}
	p, err := s.GetObfuscationProfile(ctx, *state.ProfileID)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// isInvalidUUID reports Postgres 22P02 (a malformed uuid literal in a path or body):
// to callers that is simply "no such row", not a server error.
func isInvalidUUID(err error) bool {
	type pgErr interface{ SQLState() string }
	var pe pgErr
	return errors.As(err, &pe) && pe.SQLState() == "22P02"
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// DesiredObfuscationForNode is the node's assigned profile (what the heartbeat
// response tells the agent to run), nil for plain WireGuard.
func (s *Store) DesiredObfuscationForNode(ctx context.Context, nodeID string) (*ObfuscationProfile, error) {
	state, err := s.GetNodeObfuscation(ctx, nodeID)
	if err != nil || state.ProfileID == nil {
		return nil, err
	}
	p, err := s.GetObfuscationProfile(ctx, *state.ProfileID)
	if err != nil {
		return nil, err
	}
	return &p, nil
}
