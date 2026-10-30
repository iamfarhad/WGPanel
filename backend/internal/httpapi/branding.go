package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"wgpanel-api/internal/store"
)

// Branding endpoints (see store/branding.go for the model). Three editors share one
// request/response shape: the global default (super admin), a reseller's own profile,
// and - for staff - any reseller's profile. The public side (subscription page) gets a
// stripped, resolved view built in subinfo.go; nothing here is reachable by customers
// except the logo bytes served by handleSubscriptionLogo.

// maxBrandingBody bounds the JSON body: a 64 KiB logo is ~88 KiB of base64.
const maxBrandingBody = 128 << 10

func (s *Server) registerBrandingRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/branding", s.requireAdmin(http.HandlerFunc(s.handleGetGlobalBranding)))
	mux.Handle("PUT /api/v1/branding", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handlePutGlobalBranding))))
	mux.Handle("GET /api/v1/reseller/branding", s.requireAdmin(http.HandlerFunc(s.handleGetOwnBranding)))
	mux.Handle("PUT /api/v1/reseller/branding", s.requireAdmin(http.HandlerFunc(s.handlePutOwnBranding)))
	mux.Handle("GET /api/v1/resellers/{id}/branding", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleGetResellerBranding))))
	mux.Handle("PUT /api/v1/resellers/{id}/branding", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handlePutResellerBranding))))
	// Public, capability-URL scoped like the rest of /sub/{token}: serves the logo bytes of
	// the brand resolved for that account (never a logo chosen by anyone else).
	mux.HandleFunc("GET /api/v1/sub/{token}/logo", s.subRateGate(s.handleSubscriptionLogo))
}

type brandingResponse struct {
	BrandName    string `json:"brand_name"`
	SupportLink  string `json:"support_link"`
	Announcement string `json:"announcement"`
	AccentColor  string `json:"accent_color"`
	LogoURL      string `json:"logo_url"`
	// HasLogoUpload / LogoPreview: an uploaded logo lives in the DB; the preview is a
	// data URL (<= ~88 KB) so the editor can show it without another route.
	HasLogoUpload bool    `json:"has_logo_upload"`
	LogoPreview   *string `json:"logo_preview"`
	// InheritedFrom is "parent" or "global" when this profile is empty and customers
	// currently see another one; "" when this profile is what they see.
	InheritedFrom string  `json:"inherited_from"`
	UpdatedAt     *string `json:"updated_at"`
}

func toBrandingResponse(b store.BrandProfile) brandingResponse {
	out := brandingResponse{
		BrandName: b.BrandName, SupportLink: b.SupportLink, Announcement: b.Announcement,
		AccentColor: b.AccentColor, LogoURL: b.LogoURL, HasLogoUpload: b.HasUploadedLogo(),
	}
	if b.HasUploadedLogo() {
		v := "data:" + b.LogoType + ";base64," + base64.StdEncoding.EncodeToString(b.LogoData)
		out.LogoPreview = &v
	}
	if !b.UpdatedAt.IsZero() {
		v := b.UpdatedAt.Format("2006-01-02T15:04:05Z07:00")
		out.UpdatedAt = &v
	}
	return out
}

// putBrandingRequest replaces the text fields; the logo changes only with logo_base64
// (a PNG/JPEG/WebP, raw base64 or a data: URL) or remove_logo.
type putBrandingRequest struct {
	BrandName    string  `json:"brand_name"`
	SupportLink  string  `json:"support_link"`
	Announcement string  `json:"announcement"`
	AccentColor  string  `json:"accent_color"`
	LogoURL      string  `json:"logo_url"`
	LogoBase64   *string `json:"logo_base64"`
	RemoveLogo   bool    `json:"remove_logo"`
}

func (s *Server) decodeBrandingRequest(w http.ResponseWriter, r *http.Request) (store.BrandUpdate, bool) {
	var req putBrandingRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBrandingBody)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "malformed or oversized JSON body")
		return store.BrandUpdate{}, false
	}
	u := store.BrandUpdate{
		BrandName: req.BrandName, SupportLink: req.SupportLink, Announcement: req.Announcement,
		AccentColor: req.AccentColor, LogoURL: req.LogoURL, RemoveLogo: req.RemoveLogo,
	}
	if req.LogoBase64 != nil && *req.LogoBase64 != "" {
		raw := *req.LogoBase64
		if i := strings.Index(raw, ","); strings.HasPrefix(raw, "data:") && i > 0 {
			raw = raw[i+1:]
		}
		data, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "logo_base64 is not valid base64")
			return store.BrandUpdate{}, false
		}
		ct, err := store.ValidateLogo(data)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_logo", err.Error())
			return store.BrandUpdate{}, false
		}
		u.NewLogo, u.NewLogoType = data, ct
	}
	return u, true
}

// saveBranding is the shared write path; adminID nil = the global default.
func (s *Server) saveBranding(w http.ResponseWriter, r *http.Request, adminID *string, target string) {
	u, ok := s.decodeBrandingRequest(w, r)
	if !ok {
		return
	}
	b, err := s.Store.SaveBrand(r.Context(), adminID, u)
	var verr *store.BrandValidationError
	if errors.As(err, &verr) {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", verr.Msg)
		return
	}
	if err != nil {
		s.Logger.Error("save_branding_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not save branding")
		return
	}
	s.auditReseller(r, "branding.updated", target, map[string]any{
		"logo_changed": u.RemoveLogo || len(u.NewLogo) > 0, "has_name": b.BrandName != "",
	})
	resp := toBrandingResponse(b)
	resp.InheritedFrom = s.brandInheritedFrom(r, adminID, b)
	writeJSON(w, http.StatusOK, resp)
}

// brandInheritedFrom tells an editor whose own profile is empty what customers see
// instead. It never names the other brand - just where it comes from.
func (s *Server) brandInheritedFrom(r *http.Request, adminID *string, own store.BrandProfile) string {
	if adminID == nil || !own.IsEmpty() {
		return ""
	}
	_, src, err := s.Store.ResolveBrand(r.Context(), store.ResellerNamespace(*adminID))
	if err != nil {
		return ""
	}
	if src == store.BrandSourceGlobal {
		return "global"
	}
	return "parent"
}

func (s *Server) loadBranding(w http.ResponseWriter, r *http.Request, adminID *string) {
	b, err := s.Store.GetBrand(r.Context(), adminID)
	if err != nil {
		s.Logger.Error("get_branding_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not load branding")
		return
	}
	resp := toBrandingResponse(b)
	resp.InheritedFrom = s.brandInheritedFrom(r, adminID, b)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleGetGlobalBranding(w http.ResponseWriter, r *http.Request) {
	// Readable by every staff role (Settings display); a reseller has its own editor
	// and must not see the owner's profile through this route.
	if identity, ok := callerIdentityFromContext(r.Context()); ok && identity.ResellerID != "" {
		writeJSONError(w, http.StatusForbidden, "forbidden", "this route is not available to reseller accounts")
		return
	}
	s.loadBranding(w, r, nil)
}

func (s *Server) handlePutGlobalBranding(w http.ResponseWriter, r *http.Request) {
	s.saveBranding(w, r, nil, "global")
}

func (s *Server) handleGetOwnBranding(w http.ResponseWriter, r *http.Request) {
	id, ok := resellerCaller(w, r)
	if !ok {
		return
	}
	s.loadBranding(w, r, &id)
}

func (s *Server) handlePutOwnBranding(w http.ResponseWriter, r *http.Request) {
	id, ok := resellerCaller(w, r)
	if !ok {
		return
	}
	s.saveBranding(w, r, &id, "own")
}

func (s *Server) handleGetResellerBranding(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.Store.GetResellerLimits(r.Context(), id); err != nil {
		writeJSONError(w, http.StatusNotFound, "reseller_not_found", "no reseller with that id")
		return
	}
	s.loadBranding(w, r, &id)
}

func (s *Server) handlePutResellerBranding(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	res, err := s.Store.GetReseller(r.Context(), id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "reseller_not_found", "no reseller with that id")
		return
	}
	s.saveBranding(w, r, &id, res.Username)
}

// publicBrand is what the customer page may know about its brand: no ids, no source, no
// other reseller's data. Logo is a same-origin path (uploaded) or the https URL the
// reseller chose.
type publicBrand struct {
	Name         string  `json:"name"`
	LogoURL      *string `json:"logo_url"`
	SupportLabel string  `json:"support_label"`
	SupportURL   string  `json:"support_url"`
	Announcement string  `json:"announcement"`
	AccentColor  string  `json:"accent_color"`
}

// brandForAccount resolves and shapes the brand for an account. On any failure it
// returns nil: the page then renders unbranded rather than failing the whole response.
func (s *Server) brandForAccount(r *http.Request, account store.Account) (*publicBrand, store.BrandProfile, string) {
	ns := ""
	if account.OwnerKeyNamespace != nil {
		ns = *account.OwnerKeyNamespace
	}
	b, src, err := s.Store.ResolveBrand(r.Context(), ns)
	if err != nil {
		s.Logger.Warn("resolve_brand_failed", "error", err)
		return nil, store.BrandProfile{}, ""
	}
	if b.IsEmpty() {
		return nil, b, src
	}
	out := &publicBrand{
		Name: b.BrandName, SupportLabel: b.SupportLink, SupportURL: b.SupportURL(),
		Announcement: b.Announcement, AccentColor: b.AccentColor,
	}
	switch {
	case b.HasUploadedLogo():
		// ?v= busts the browser cache when the logo changes.
		p := "/api/v1/sub/" + url.PathEscape(account.SubscriptionToken) + "/logo?v=" + strconv.FormatInt(b.UpdatedAt.Unix(), 10)
		out.LogoURL = &p
	case b.LogoURL != "":
		u := b.LogoURL
		out.LogoURL = &u
	}
	return out, b, src
}

func (s *Server) handleSubscriptionLogo(w http.ResponseWriter, r *http.Request) {
	account, ok := s.lookupSubscriptionAccount(w, r)
	if !ok {
		return
	}
	_, b, _ := s.brandForAccount(r, account)
	if !b.HasUploadedLogo() {
		writeJSONError(w, http.StatusNotFound, "not_found", "no logo")
		return
	}
	// Defence in depth for user-supplied bytes on the panel's origin: the type was
	// sniffed at upload (png/jpeg/webp only), and nosniff + a locked-down CSP stop a
	// browser reinterpreting or running anything if that ever regressed.
	w.Header().Set("Content-Type", b.LogoType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b.LogoData)
}
