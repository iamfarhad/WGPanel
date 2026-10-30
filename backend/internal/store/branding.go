package store

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Branding (migration 0032): what a customer sees on the public subscription page -
// brand name, logo, support link, an announcement and an accent color. One profile per
// reseller plus one global default (the owner's own brand, used for admin-owned
// accounts and as the fallback for resellers with no branding of their own).

const (
	MaxLogoBytes        = 64 << 10 // keep in sync with the CHECK in migration 0032
	MaxBrandNameRunes   = 40
	MaxSupportLinkRunes = 200
	MaxAnnouncementRune = 280
	MaxLogoURLRunes     = 300
)

// BrandSource says where a resolved profile came from (never shown to customers).
const (
	BrandSourceReseller = "reseller"
	BrandSourceGlobal   = "global"
)

// BrandValidationError is a refusal caused by the request's content (HTTP 400).
type BrandValidationError struct{ Msg string }

func (e *BrandValidationError) Error() string { return e.Msg }

func brandInvalid(msg string) error { return &BrandValidationError{Msg: msg} }

type BrandProfile struct {
	AdminID      *string // nil = the global default
	BrandName    string
	SupportLink  string // "@handle" or an https URL, already normalised
	Announcement string
	AccentColor  string // "" or #rrggbb
	LogoURL      string // external https logo; empty when an upload is set
	LogoType     string // content type of LogoData
	LogoData     []byte
	UpdatedAt    time.Time
}

// HasUploadedLogo reports whether the logo bytes live in the database.
func (b BrandProfile) HasUploadedLogo() bool { return len(b.LogoData) > 0 }

// IsEmpty: nothing to show. An empty profile does not count as "branding" during
// resolution, so a reseller that saved a blank form still inherits its parent's brand.
func (b BrandProfile) IsEmpty() bool {
	return b.BrandName == "" && b.SupportLink == "" && b.Announcement == "" &&
		b.AccentColor == "" && b.LogoURL == "" && !b.HasUploadedLogo()
}

// SupportURL turns the stored support link into something an <a href> can use:
// a Telegram handle becomes its t.me URL, an https URL is returned as is.
func (b BrandProfile) SupportURL() string {
	if h, ok := strings.CutPrefix(b.SupportLink, "@"); ok {
		return "https://t.me/" + h
	}
	return b.SupportLink
}

var (
	telegramHandleRE = regexp.MustCompile(`^@[A-Za-z][A-Za-z0-9_]{4,31}$`)
	accentColorRE    = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
)

// NormalizeSupportLink accepts a Telegram handle ("@name" or "name"), a t.me link or any
// https URL and returns the canonical stored form. Only https (and the handle form) is
// allowed: the value ends up in an href on a public page, so javascript:, data: and
// plain http are out.
func NormalizeSupportLink(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if utf8.RuneCountInString(s) > MaxSupportLinkRunes {
		return "", brandInvalid("support link is too long")
	}
	if strings.HasPrefix(s, "@") {
		if !telegramHandleRE.MatchString(s) {
			return "", brandInvalid("support link must be a Telegram handle like @name (5-32 letters, digits or underscores)")
		}
		return s, nil
	}
	low := strings.ToLower(s)
	switch {
	case strings.HasPrefix(low, "t.me/"), strings.HasPrefix(low, "telegram.me/"):
		s = "https://" + s
	case !strings.Contains(s, "/") && !strings.Contains(s, "."):
		// A bare handle typed without the @.
		if !telegramHandleRE.MatchString("@" + s) {
			return "", brandInvalid("support link must be a Telegram handle or an https URL")
		}
		return "@" + s, nil
	}
	if err := checkHTTPSURL(s); err != nil {
		return "", brandInvalid("support link must be a Telegram handle or an https URL")
	}
	return s, nil
}

func checkHTTPSURL(s string) error {
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return errors.New("not an https URL")
	}
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return errors.New("illegal character")
		}
	}
	return nil
}

// ValidateLogo checks an uploaded logo and returns its content type. The type comes
// from sniffing the bytes, never from the client: only PNG, JPEG and WebP pass (no SVG
// - it can carry script and would be served from the panel's own origin).
func ValidateLogo(data []byte) (string, error) {
	if len(data) == 0 {
		return "", brandInvalid("logo is empty")
	}
	if len(data) > MaxLogoBytes {
		return "", brandInvalid("logo is too large (max 64 KB)")
	}
	switch ct := http.DetectContentType(data); ct {
	case "image/png", "image/jpeg", "image/webp":
		return ct, nil
	}
	return "", brandInvalid("logo must be a PNG, JPEG or WebP image")
}

// Normalize trims and validates every text field in place.
func (b *BrandProfile) Normalize() error {
	b.BrandName = strings.TrimSpace(b.BrandName)
	b.Announcement = strings.TrimSpace(b.Announcement)
	b.AccentColor = strings.ToLower(strings.TrimSpace(b.AccentColor))
	b.LogoURL = strings.TrimSpace(b.LogoURL)

	if utf8.RuneCountInString(b.BrandName) > MaxBrandNameRunes {
		return brandInvalid("brand name is too long (max 40 characters)")
	}
	for _, r := range b.BrandName {
		if unicode.IsControl(r) {
			return brandInvalid("brand name must be a single line of plain text")
		}
	}
	if utf8.RuneCountInString(b.Announcement) > MaxAnnouncementRune {
		return brandInvalid("announcement is too long (max 280 characters)")
	}
	for _, r := range b.Announcement {
		if unicode.IsControl(r) && r != '\n' {
			return brandInvalid("announcement must be plain text")
		}
	}
	if b.AccentColor != "" && !accentColorRE.MatchString(b.AccentColor) {
		return brandInvalid("accent color must look like #1a73e8")
	}
	if b.LogoURL != "" {
		if utf8.RuneCountInString(b.LogoURL) > MaxLogoURLRunes || checkHTTPSURL(b.LogoURL) != nil {
			return brandInvalid("logo URL must be an https URL")
		}
	}
	link, err := NormalizeSupportLink(b.SupportLink)
	if err != nil {
		return err
	}
	b.SupportLink = link
	return nil
}

const brandColumns = `brand_name, support_link, announcement, accent_color, logo_url, logo_type, logo_data, updated_at`

func scanBrand(row pgx.Row, b *BrandProfile) error {
	return row.Scan(&b.BrandName, &b.SupportLink, &b.Announcement, &b.AccentColor, &b.LogoURL, &b.LogoType, &b.LogoData, &b.UpdatedAt)
}

// GetBrand returns one profile: adminID nil = the global default. A profile that was
// never saved is an empty one (not an error), so editors always have something to bind.
func (s *Store) GetBrand(ctx context.Context, adminID *string) (BrandProfile, error) {
	b := BrandProfile{AdminID: adminID}
	err := scanBrand(s.pool.QueryRow(ctx,
		`SELECT `+brandColumns+` FROM brand_profiles WHERE admin_id IS NOT DISTINCT FROM $1::uuid`, adminID), &b)
	if errors.Is(err, pgx.ErrNoRows) {
		return BrandProfile{AdminID: adminID}, nil
	}
	return b, err
}

// BrandUpdate replaces the text fields of a profile; the logo is changed only through
// NewLogo (an upload, already validated) or RemoveLogo. An external LogoURL and an
// uploaded logo are mutually exclusive: setting one clears the other, so the page never
// has two candidates to choose between.
type BrandUpdate struct {
	BrandName, SupportLink, Announcement, AccentColor, LogoURL string
	NewLogo                                                    []byte
	NewLogoType                                                string
	RemoveLogo                                                 bool
}

// SaveBrand validates and upserts a profile (adminID nil = global) and returns it.
func (s *Store) SaveBrand(ctx context.Context, adminID *string, u BrandUpdate) (BrandProfile, error) {
	cur, err := s.GetBrand(ctx, adminID)
	if err != nil {
		return BrandProfile{}, err
	}
	next := BrandProfile{
		AdminID: adminID, BrandName: u.BrandName, SupportLink: u.SupportLink, Announcement: u.Announcement,
		AccentColor: u.AccentColor, LogoURL: u.LogoURL, LogoType: cur.LogoType, LogoData: cur.LogoData,
	}
	if err := next.Normalize(); err != nil {
		return BrandProfile{}, err
	}
	switch {
	case u.RemoveLogo:
		next.LogoURL, next.LogoData, next.LogoType = "", nil, ""
	case len(u.NewLogo) > 0:
		ct, err := ValidateLogo(u.NewLogo)
		if err != nil {
			return BrandProfile{}, err
		}
		next.LogoURL, next.LogoData, next.LogoType = "", u.NewLogo, ct
	case next.LogoURL != "":
		next.LogoData, next.LogoType = nil, ""
	}

	conflict := `ON CONFLICT (admin_id) WHERE admin_id IS NOT NULL`
	if adminID == nil {
		conflict = `ON CONFLICT ((true)) WHERE admin_id IS NULL`
	}
	var out BrandProfile
	out.AdminID = adminID
	err = scanBrand(s.pool.QueryRow(ctx, `
		INSERT INTO brand_profiles (admin_id, brand_name, support_link, announcement, accent_color, logo_url, logo_type, logo_data)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		`+conflict+` DO UPDATE SET
			brand_name = EXCLUDED.brand_name, support_link = EXCLUDED.support_link,
			announcement = EXCLUDED.announcement, accent_color = EXCLUDED.accent_color,
			logo_url = EXCLUDED.logo_url, logo_type = EXCLUDED.logo_type, logo_data = EXCLUDED.logo_data,
			updated_at = now()
		RETURNING `+brandColumns,
		adminID, next.BrandName, next.SupportLink, next.Announcement, next.AccentColor, next.LogoURL, next.LogoType, nullableBytes(next.LogoData),
	), &out)
	return out, err
}

func nullableBytes(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	return b
}

// ResolveBrand returns the profile the public subscription page renders for an account
// owned by `namespace` ("" or an API-key id = admin-owned): the reseller's own profile
// if it has any content, else the nearest ancestor's that does, else the global
// default (falling back to the legacy panel_settings.support_contact for its support
// link). Whole profiles are never merged field by field, so a customer never sees one
// reseller's name next to another's support link.
func (s *Store) ResolveBrand(ctx context.Context, namespace string) (BrandProfile, string, error) {
	if id, ok := resellerIDFromNamespace(&namespace); ok {
		var ancestors []string
		err := s.pool.QueryRow(ctx, `SELECT ancestors FROM reseller_limits WHERE admin_id = $1::uuid`, id).Scan(&ancestors)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return BrandProfile{}, "", err
		}
		// Nearest first: self, parent, grandparent, ...
		chain := []string{id}
		for i := len(ancestors) - 1; i >= 0; i-- {
			if aid, ok := strings.CutPrefix(ancestors[i], ResellerNamespacePrefix); ok {
				chain = append(chain, aid)
			}
		}
		rows, err := s.pool.Query(ctx,
			`SELECT admin_id::text, `+brandColumns+` FROM brand_profiles WHERE admin_id = ANY($1::uuid[])`, chain)
		if err != nil {
			return BrandProfile{}, "", err
		}
		defer rows.Close()
		byID := map[string]BrandProfile{}
		for rows.Next() {
			var aid string
			var b BrandProfile
			if err := rows.Scan(&aid, &b.BrandName, &b.SupportLink, &b.Announcement, &b.AccentColor, &b.LogoURL, &b.LogoType, &b.LogoData, &b.UpdatedAt); err != nil {
				return BrandProfile{}, "", err
			}
			b.AdminID = &aid
			byID[aid] = b
		}
		if err := rows.Err(); err != nil {
			return BrandProfile{}, "", err
		}
		for _, aid := range chain {
			if b, ok := byID[aid]; ok && !b.IsEmpty() {
				return b, BrandSourceReseller, nil
			}
		}
	}
	g, err := s.GetBrand(ctx, nil)
	if err != nil {
		return BrandProfile{}, "", err
	}
	if g.SupportLink == "" {
		var legacy *string
		if err := s.pool.QueryRow(ctx, `SELECT support_contact FROM panel_settings WHERE id = 1`).Scan(&legacy); err == nil && legacy != nil {
			if link, err := NormalizeSupportLink(*legacy); err == nil {
				g.SupportLink = link
			} else {
				// Legacy free text (an email, a phone number) is shown verbatim as text
				// by the page's support_contact field, never as a link.
				g.SupportLink = ""
			}
		}
	}
	return g, BrandSourceGlobal, nil
}
