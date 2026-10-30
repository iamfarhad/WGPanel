package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Account notes and tags (migration 0033). They are deliberately NOT part of the Account
// struct / accountColumns: every package that adds an account column edits those, and
// notes/tags are read in one batch per response (AccountMetaByIDs) anyway, so a list
// page costs one extra query instead of a wider row on every account query.

const (
	MaxTagLength   = 32
	MaxTagsPerAcct = 20
	MaxNoteLength  = 2000
)

var (
	ErrInvalidTag  = errors.New("invalid tag")
	ErrNoteTooLong = errors.New("note is too long")
)

// NormalizeTags trims, lowercases, turns inner whitespace into '-', drops empties and
// duplicates, and returns the tags sorted (a stable order makes the stored value and
// API output deterministic). Allowed characters are letters, digits and _ - : . (so
// Persian tags work). The client's spelling is never stored as given: "VIP" and " vip "
// are the same tag, which is what makes ?tag= filtering and bulk removal reliable.
func NormalizeTags(in []string) ([]string, error) {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, raw := range in {
		t := strings.ToLower(strings.Join(strings.Fields(raw), "-"))
		if t == "" {
			continue
		}
		if utf8.RuneCountInString(t) > MaxTagLength {
			return nil, fmt.Errorf("%w: %q is longer than %d characters", ErrInvalidTag, raw, MaxTagLength)
		}
		for _, r := range t {
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("_-:.", r) {
				return nil, fmt.Errorf("%w: %q contains %q (letters, digits and _ - : . only)", ErrInvalidTag, raw, r)
			}
		}
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	if len(out) > MaxTagsPerAcct {
		return nil, fmt.Errorf("%w: at most %d tags per account", ErrInvalidTag, MaxTagsPerAcct)
	}
	sort.Strings(out)
	return out, nil
}

// AccountMeta is the per-account abuse/organization data returned next to an account.
type AccountMeta struct {
	Note               string
	Tags               []string
	SharingSuspectedAt *time.Time
	TorrentHits24h     int64
}

// AccountMetaByIDs loads notes, tags, the sharing flag and the 24h torrent hit count for
// a page of accounts in two queries. Missing ids are simply absent from the map.
func (s *Store) AccountMetaByIDs(ctx context.Context, ids []string) (map[string]AccountMeta, error) {
	out := make(map[string]AccountMeta, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, note, tags, sharing_suspected_at FROM accounts WHERE id = ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var m AccountMeta
		if err := rows.Scan(&id, &m.Note, &m.Tags, &m.SharingSuspectedAt); err != nil {
			rows.Close()
			return nil, err
		}
		if m.Tags == nil {
			m.Tags = []string{}
		}
		out[id] = m
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	hrows, err := s.pool.Query(ctx, `
		SELECT account_id, sum(hits)::bigint FROM account_torrent_hits
		WHERE account_id = ANY($1::uuid[]) AND ts > now() - interval '24 hours'
		GROUP BY account_id`, ids)
	if err != nil {
		return nil, err
	}
	defer hrows.Close()
	for hrows.Next() {
		var id string
		var n int64
		if err := hrows.Scan(&id, &n); err != nil {
			return nil, err
		}
		if m, ok := out[id]; ok {
			m.TorrentHits24h = n
			out[id] = m
		}
	}
	return out, hrows.Err()
}

// SetAccountMeta replaces the note and/or tags of one account (nil = unchanged), scoped
// to callerNamespace like every account write. Tags must already be normalized.
func (s *Store) SetAccountMeta(ctx context.Context, id string, callerNamespace *string, note *string, tags *[]string) error {
	if note != nil && utf8.RuneCountInString(*note) > MaxNoteLength {
		return ErrNoteTooLong
	}
	var tagArg any
	if tags != nil {
		t := *tags
		if t == nil {
			t = []string{}
		}
		tagArg = t
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE accounts SET note = COALESCE($3, note), tags = COALESCE($4::text[], tags), updated_at = now()
		WHERE id = $2 AND status != 'deleted' AND `+namespaceFilter,
		callerNamespace, id, note, tagArg)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrAccountNotFound
	}
	return nil
}

// ModifyAccountTags adds and removes (already normalized) tags in one statement, so a
// bulk edit never loses a concurrent change to the account's other tags. The 20-tag cap
// is enforced on the result.
func (s *Store) ModifyAccountTags(ctx context.Context, id string, callerNamespace *string, add, remove []string) error {
	if add == nil {
		add = []string{}
	}
	if remove == nil {
		remove = []string{}
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE accounts SET updated_at = now(), tags = (
			SELECT COALESCE(array_agg(t ORDER BY t), '{}') FROM (
				SELECT DISTINCT t FROM unnest(tags || $3::text[]) AS t WHERE NOT (t = ANY($4::text[]))
			) x
		)
		WHERE id = $2 AND status != 'deleted' AND `+namespaceFilter+`
		  AND (SELECT count(DISTINCT t) FROM unnest(tags || $3::text[]) AS t WHERE NOT (t = ANY($4::text[]))) <= `+fmt.Sprint(MaxTagsPerAcct),
		callerNamespace, id, add, remove)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		// Either not found or the cap was hit; tell them apart so the API can answer
		// 404 vs 400.
		var exists bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM accounts WHERE id = $2 AND status != 'deleted' AND `+namespaceFilter+`)`,
			callerNamespace, id).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrAccountNotFound
		}
		return fmt.Errorf("%w: at most %d tags per account", ErrInvalidTag, MaxTagsPerAcct)
	}
	return nil
}

// AccountFilter narrows ListAccounts / ListAccountsAfter. It travels in the context
// (WithAccountFilter) rather than as another parameter so the two list functions keep
// their signatures - they are edited by several features at once.
type AccountFilter struct {
	Tags  []string // account must carry ALL of these (already normalized)
	Query string   // case-insensitive substring of label, external_ref, note or a tag
}

type accountFilterKey struct{}

func WithAccountFilter(ctx context.Context, f AccountFilter) context.Context {
	return context.WithValue(ctx, accountFilterKey{}, f)
}

// accountFilterClause returns an " AND (...)" fragment (empty when no filter is set) with
// placeholders numbered from firstParam, and the matching args. Substring matching uses
// position() instead of LIKE so user text needs no wildcard escaping.
func accountFilterClause(ctx context.Context, firstParam int) (string, []any) {
	f, _ := ctx.Value(accountFilterKey{}).(AccountFilter)
	var sb strings.Builder
	var args []any
	if len(f.Tags) > 0 {
		args = append(args, f.Tags)
		fmt.Fprintf(&sb, ` AND tags @> $%d::text[]`, firstParam+len(args)-1)
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		args = append(args, strings.ToLower(q))
		n := firstParam + len(args) - 1
		fmt.Fprintf(&sb, ` AND (position($%[1]d in lower(label)) > 0 OR position($%[1]d in lower(COALESCE(external_ref, ''))) > 0
			OR position($%[1]d in lower(note)) > 0 OR EXISTS (SELECT 1 FROM unnest(tags) tg WHERE position($%[1]d in tg) > 0))`, n)
	}
	return sb.String(), args
}
