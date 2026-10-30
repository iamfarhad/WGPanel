package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"wgpanel-api/internal/events"
	"wgpanel-api/internal/store"
)

// The /api/user endpoints of the Marzban facade. Field mapping (see docs/marzban-compat.md):
//
//	username                ↔ accounts.external_ref (label is set to it on create)
//	status                  ↔ derived: active | disabled | limited | expired | on_hold
//	expire (unix s, 0=never) ↔ expiry_at
//	data_limit (bytes, 0=∞)  ↔ data_quota_bytes
//	data_limit_reset_strategy ↔ reset_strategy (no_reset ↔ none)
//	on_hold_expire_duration ↔ hold_duration_days (seconds, rounded to whole days)
//	on_hold_timeout         ↔ hold_deadline_at
//	used_traffic            ↔ data_used_bytes
//	online_at               ↔ newest last_handshake_at

// mzUsernameRE and mzUsernameMsg are Marzban's own rule (app/models/user.py): the first
// word-character run must be 3-32 long, and the whole name may contain a-z A-Z 0-9 - _ @ .
// with underscores only between segments. Marzban's column is 34 chars wide, so that cap
// applies too.
var (
	mzUsernameRE  = regexp.MustCompile(`^[a-zA-Z0-9\-_@.]+(?:_[a-zA-Z0-9\-_@.]+)*$`)
	mzLeadWordRE  = regexp.MustCompile(`^\w+`)
	mzUsernameMsg = "Username only can be 3 to 32 characters and contain a-z, 0-9, and underscores in between."
)

func mzValidUsername(u string) bool {
	if len(u) > 34 || !mzUsernameRE.MatchString(u) {
		return false
	}
	// Python's (?=\w{3,32}\b): the leading word run must be 3..32 long.
	lead := mzLeadWordRE.FindString(u)
	return len(lead) >= 3 && len(lead) <= 32
}

// mzTime is Marzban's datetime wire format: ISO 8601, naive UTC, no zone suffix.
func mzTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05") }

func mzTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := mzTime(*t)
	return &s
}

// mzParseTime accepts what Marzban clients send for a datetime: RFC 3339, or the naive
// forms (read as UTC), or a unix timestamp.
func mzParseTime(v string) (time.Time, bool) {
	v = strings.TrimSpace(v)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, v, time.UTC); err == nil {
			return t, true
		}
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
		return time.Unix(n, 0), true
	}
	return time.Time{}, false
}

// mzStatus derives Marzban's status from the account. The store already records WHY an
// account is suspended, so limited/expired fall out of suspend_reason; the active-but-
// past-due cases cover the instant before the next read reconciles them.
func mzStatus(a store.Account, now time.Time) string {
	if a.Status == "suspended" {
		if a.SuspendReason != nil {
			switch *a.SuspendReason {
			case "expired":
				return "expired"
			case "quota_exceeded":
				return "limited"
			}
		}
		return "disabled"
	}
	if a.OnHold() {
		return "on_hold"
	}
	if a.ExpiryAt != nil && !a.ExpiryAt.After(now) {
		return "expired"
	}
	if a.DataQuotaBytes != nil && a.DataUsedBytes >= *a.DataQuotaBytes {
		return "limited"
	}
	return "active"
}

var mzResetToOurs = map[string]string{"no_reset": store.ResetNone, "day": "day", "week": "week", "month": "month", "year": "year"}

func mzResetFromOurs(s string) string {
	if s == "" || s == store.ResetNone {
		return "no_reset"
	}
	return s
}

// mzUserResponse is Marzban's UserResponse. Fields we have no equivalent for are present
// with their neutral value, because bots index into the object without isset() checks.
type mzUserResponse struct {
	Proxies                map[string]any      `json:"proxies"`
	Expire                 int64               `json:"expire"`
	DataLimit              int64               `json:"data_limit"`
	DataLimitResetStrategy string              `json:"data_limit_reset_strategy"`
	Inbounds               map[string][]string `json:"inbounds"`
	Note                   *string             `json:"note"`
	SubUpdatedAt           *string             `json:"sub_updated_at"`
	SubLastUserAgent       *string             `json:"sub_last_user_agent"`
	OnlineAt               *string             `json:"online_at"`
	OnHoldExpireDuration   int64               `json:"on_hold_expire_duration"`
	OnHoldTimeout          *string             `json:"on_hold_timeout"`
	AutoDeleteInDays       *int                `json:"auto_delete_in_days"`
	NextPlan               any                 `json:"next_plan"`
	Username               string              `json:"username"`
	Status                 string              `json:"status"`
	UsedTraffic            int64               `json:"used_traffic"`
	LifetimeUsedTraffic    int64               `json:"lifetime_used_traffic"`
	CreatedAt              string              `json:"created_at"`
	Links                  []string            `json:"links"`
	SubscriptionURL        string              `json:"subscription_url"`
	ExcludedInbounds       map[string][]string `json:"excluded_inbounds"`
	Admin                  any                 `json:"admin"`
}

// mzUser renders an account. links are the per-node config download URLs (the
// subscription endpoint pinned to a node), one per node the account has a peer on.
func (s *Server) mzUser(ctx context.Context, a store.Account, ns *string, origin string) (mzUserResponse, error) {
	peers, err := s.Store.ListAccountPeersWithNode(ctx, a.ID, ns)
	if err != nil {
		return mzUserResponse{}, err
	}
	links := make([]string, 0, len(peers))
	for _, p := range peers {
		links = append(links, mzAbsoluteOrRelative(origin, "/api/v1/sub/"+a.SubscriptionToken+"?node_id="+url.QueryEscape(p.NodeID)))
	}
	u := mzUserResponse{
		Proxies:                map[string]any{"wireguard": map[string]any{}},
		DataLimitResetStrategy: mzResetFromOurs(a.ResetStrategy),
		Inbounds:               map[string][]string{"wireguard": {"wireguard"}},
		OnlineAt:               mzTimePtr(a.LastHandshakeAt),
		OnHoldTimeout:          mzTimePtr(a.HoldDeadlineAt),
		Username:               "",
		Status:                 mzStatus(a, time.Now()),
		UsedTraffic:            a.DataUsedBytes,
		LifetimeUsedTraffic:    a.DataUsedBytes, // we keep no separate lifetime counter
		CreatedAt:              mzTime(a.CreatedAt),
		Links:                  links,
		SubscriptionURL:        mzSubscriptionURL(origin, a.SubscriptionToken),
		ExcludedInbounds:       map[string][]string{},
	}
	if a.ExternalRef != nil {
		u.Username = *a.ExternalRef
	}
	if a.ExpiryAt != nil {
		u.Expire = a.ExpiryAt.Unix()
	}
	if a.DataQuotaBytes != nil {
		u.DataLimit = *a.DataQuotaBytes
	}
	if a.OnHold() {
		u.OnHoldExpireDuration = int64(*a.HoldDurationDays) * 86400
	}
	return u, nil
}

// mzRespondUser writes one user (status 200 - Marzban answers 200, not 201, on create).
func (s *Server) mzRespondUser(w http.ResponseWriter, r *http.Request, a store.Account, c mzCaller) {
	ctx := r.Context()
	u, err := s.mzUser(ctx, a, c.NS, s.mzOrigin(s.mzSettings(ctx)))
	if err != nil {
		s.Logger.Error("marzban_compat_render_user_failed", "error", err)
		mzDetail(w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	writeJSON(w, http.StatusOK, u)
}

// mzFindAccount resolves a username within the caller's namespace. A soft-deleted
// account is "not found" here even though its external_ref stays reserved (see
// store.FindAccountsByExternalRef), as is one owned by another key.
func (s *Server) mzFindAccount(ctx context.Context, username string, ns *string) (store.Account, error) {
	found, err := s.Store.FindAccountsByExternalRef(ctx, username, ns)
	if err != nil {
		return store.Account{}, err
	}
	for _, a := range found {
		if a.Status != "deleted" {
			return a, nil
		}
	}
	return store.Account{}, store.ErrAccountNotFound
}

// mzLoad finds the path's user or writes 404/500 and reports false.
func (s *Server) mzLoad(w http.ResponseWriter, r *http.Request, c mzCaller) (store.Account, bool) {
	a, err := s.mzFindAccount(r.Context(), r.PathValue("username"), c.NS)
	if err == nil {
		return a, true
	}
	if errors.Is(err, store.ErrAccountNotFound) {
		mzDetail(w, http.StatusNotFound, "User not found")
		return a, false
	}
	s.Logger.Error("marzban_compat_find_user_failed", "error", err)
	mzDetail(w, http.StatusInternalServerError, "Internal Server Error")
	return a, false
}

// mzStoreErr maps any store/apiError from an account operation to the Marzban response.
func (s *Server) mzStoreErr(w http.ResponseWriter, err error, op string) {
	mzAPIError(w, toAPIError(err, op))
}

func (s *Server) mzAudit(r *http.Request, c mzCaller, action string, a store.Account, detail map[string]any) {
	if detail == nil {
		detail = map[string]any{}
	}
	detail["via"] = "marzban_compat"
	if err := s.Store.InsertAuditLog(r.Context(), auditActor(c.Identity), action, a.ID, detail, r.RemoteAddr); err != nil {
		s.Logger.Error("audit_log_failed", "error", err)
	}
}

// ---- request body ----

// mzUserBody is the union of UserCreate and UserModify. Pointer fields tell "absent or
// null" (unchanged, as in Marzban's exclude_unset update) from a value. proxies,
// inbounds, group_ids, excluded_inbounds, next_plan, auto_delete_in_days are
// deliberately not declared: encoding/json drops unknown keys, which is exactly the
// "accept and ignore" the bots need (they always send proxies).
type mzUserBody struct {
	Username               *string  `json:"username"`
	Status                 *string  `json:"status"`
	Expire                 *float64 `json:"expire"`
	DataLimit              *float64 `json:"data_limit"`
	DataLimitResetStrategy *string  `json:"data_limit_reset_strategy"`
	OnHoldExpireDuration   *float64 `json:"on_hold_expire_duration"`
	OnHoldTimeout          *string  `json:"on_hold_timeout"`
	Note                   *string  `json:"note"`
}

const (
	mzMaxBody   = 1 << 20
	mzMaxExpire = 253402300799 // 9999-12-31, keeps time.Unix well inside its range
)

// mzDecodeBody parses and validates the JSON body. On failure it has already written the
// 422 and returns ok=false. Validation that depends on create-vs-modify is the caller's.
func mzDecodeBody(w http.ResponseWriter, r *http.Request) (mzUserBody, bool) {
	var b mzUserBody
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, mzMaxBody))
	if err != nil {
		mzValidation(w, mzFieldError{Loc: []string{"body"}, Msg: "could not read request body", Type: "value_error"})
		return b, false
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return b, true // an empty body is "no changes" for PUT; create rejects the missing username itself
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		var ute *json.UnmarshalTypeError
		if errors.As(err, &ute) {
			mzValidation(w, mzBodyError(ute.Field, "value is not a valid "+mzTypeName(ute.Type.String()), "type_error"))
			return b, false
		}
		mzValidation(w, mzFieldError{Loc: []string{"body", "0"}, Msg: "Expecting value", Type: "value_error.jsondecode"})
		return b, false
	}
	return b, true
}

func mzTypeName(goType string) string {
	switch {
	case strings.HasPrefix(goType, "float"), strings.HasPrefix(goType, "int"):
		return "integer"
	}
	return "string"
}

// mzValidate checks the fields shared by create and modify and returns the converted
// numbers. allowedStatus lists the status values this operation accepts.
type mzParsed struct {
	expire        *int64
	dataLimit     *int64
	resetStrategy *string
	holdDays      *int
	holdDeadline  *time.Time
}

func mzValidate(b mzUserBody, allowedStatus ...string) (mzParsed, []mzFieldError) {
	var p mzParsed
	var errs []mzFieldError
	integer := func(field string, v *float64, max float64) *int64 {
		if v == nil {
			return nil
		}
		if *v != math.Trunc(*v) {
			errs = append(errs, mzBodyError(field, "value is not a valid integer", "type_error.integer"))
			return nil
		}
		if *v < 0 {
			errs = append(errs, mzBodyError(field, "ensure this value is greater than or equal to 0", "value_error.number.not_ge"))
			return nil
		}
		if *v > max {
			errs = append(errs, mzBodyError(field, "ensure this value is less than or equal to "+strconv.FormatFloat(max, 'f', 0, 64), "value_error.number.not_le"))
			return nil
		}
		n := int64(*v)
		return &n
	}
	p.expire = integer("expire", b.Expire, mzMaxExpire)
	p.dataLimit = integer("data_limit", b.DataLimit, 1<<60)
	if d := integer("on_hold_expire_duration", b.OnHoldExpireDuration, 1<<40); d != nil {
		// Our hold is whole days; bots compute seconds as (now + N days) - now, which can
		// land a few seconds under N*86400, so round rather than truncate.
		days := int(math.Round(float64(*d) / 86400))
		if days < 1 {
			days = 1
		}
		if days > maxHoldDays {
			errs = append(errs, mzBodyError("on_hold_expire_duration", "on-hold duration is longer than 3650 days", "value_error"))
		} else {
			p.holdDays = &days
		}
	}
	if b.OnHoldTimeout != nil && *b.OnHoldTimeout != "" {
		if t, ok := mzParseTime(*b.OnHoldTimeout); ok {
			p.holdDeadline = &t
		} else {
			errs = append(errs, mzBodyError("on_hold_timeout", "invalid datetime format", "value_error.datetime"))
		}
	}
	if b.DataLimitResetStrategy != nil {
		if ours, ok := mzResetToOurs[*b.DataLimitResetStrategy]; ok {
			p.resetStrategy = &ours
		} else {
			errs = append(errs, mzBodyError("data_limit_reset_strategy", "value is not a valid enumeration member; permitted: 'no_reset', 'day', 'week', 'month', 'year'", "type_error.enum"))
		}
	}
	if b.Status != nil {
		ok := false
		for _, s := range allowedStatus {
			ok = ok || s == *b.Status
		}
		if !ok {
			errs = append(errs, mzBodyError("status", "unexpected value; permitted: '"+strings.Join(allowedStatus, "', '")+"'", "value_error.const"))
		}
	}
	return p, errs
}

// ---- POST /api/user ----

func (s *Server) handleMzCreateUser(w http.ResponseWriter, r *http.Request, c mzCaller) {
	b, ok := mzDecodeBody(w, r)
	if !ok {
		return
	}
	var errs []mzFieldError
	if b.Username == nil {
		errs = append(errs, mzBodyError("username", "field required", "value_error.missing"))
	} else if !mzValidUsername(*b.Username) {
		errs = append(errs, mzBodyError("username", mzUsernameMsg, "value_error"))
	}
	p, verrs := mzValidate(b, "active", "on_hold")
	errs = append(errs, verrs...)
	onHold := b.Status != nil && *b.Status == "on_hold"
	if onHold {
		if p.holdDays == nil {
			errs = append(errs, mzBodyError("on_hold_expire_duration", "on_hold_expire_duration is required when status is on_hold", "value_error"))
		}
		if p.expire != nil && *p.expire != 0 {
			errs = append(errs, mzBodyError("expire", "an on_hold user cannot have an expire date: its expiry starts at the first connection", "value_error"))
		}
	}
	if len(errs) > 0 {
		mzValidation(w, errs...)
		return
	}

	ctx := r.Context()
	username := *b.Username
	sp := accountSpec{ExternalRef: &username, ResetStrategy: store.ResetNone}
	if p.resetStrategy != nil {
		sp.ResetStrategy = *p.resetStrategy
	}
	if p.dataLimit != nil && *p.dataLimit > 0 {
		sp.Quota = p.dataLimit
	}
	if onHold {
		sp.HoldDays = p.holdDays
		sp.HoldDeadline = p.holdDeadline
	} else if p.expire != nil && *p.expire > 0 {
		t := time.Unix(*p.expire, 0)
		sp.Expiry = &t
	}

	account, aerr := s.createFromSpec(ctx, c.Identity, sp, username)
	if aerr != nil {
		mzAPIError(w, aerr)
		return
	}
	s.emitAccountEvent(events.AccountCreated, account)
	s.mzAudit(r, c, "account.created", account, createAuditDetail(nil, account))
	s.mzRespondUser(w, r, account, c)
}

// ---- GET /api/user/{username} ----

func (s *Server) handleMzGetUser(w http.ResponseWriter, r *http.Request, c mzCaller) {
	a, ok := s.mzLoad(w, r, c)
	if !ok {
		return
	}
	s.mzRespondUser(w, r, a, c)
}

// ---- PUT /api/user/{username} ----

// handleMzModifyUser applies Marzban's partial update. Every change is delegated to the
// store call the native API uses for the same intent, in this order:
//
//  1. expire        → RenewAccountHold with an absolute date (so an expired account
//     comes back to life exactly as on a renewal), ClearAccountExpiry for 0, or - for an
//     account on hold - UpdateAccount, which activates it with that fixed date;
//  2. data_limit, data_limit_reset_strategy, on_hold_expire_duration → one UpdateAccount
//     (which lifts a quota_exceeded suspension when the new cap is above the usage);
//  3. status active/disabled → EnableAccount / SuspendAccount.
//
// The steps are separate transactions, so a mid-way failure (say a reseller's credit
// refusing the new quota) leaves the earlier steps applied; the error is returned and
// the bot's retry is idempotent.
func (s *Server) handleMzModifyUser(w http.ResponseWriter, r *http.Request, c mzCaller) {
	b, ok := mzDecodeBody(w, r)
	if !ok {
		return
	}
	p, errs := mzValidate(b, "active", "disabled", "on_hold")
	if len(errs) > 0 {
		mzValidation(w, errs...)
		return
	}
	ctx := r.Context()
	cur, ok := s.mzLoad(w, r, c)
	if !ok {
		return
	}
	id := cur.ID
	fields := []string{}
	fail := func(err error) { s.mzStoreErr(w, err, "update") }

	// A status flip is a "suspend"-tier action in the native API; honour that split.
	if b.Status != nil {
		disabledNow := mzStatus(cur, time.Now()) == "disabled"
		if ((*b.Status == "disabled") != disabledNow) && *b.Status != "on_hold" && !stringSliceContains(c.Identity.Permissions, "suspend") {
			mzDetail(w, http.StatusForbidden, "This API key lacks the 'suspend' permission")
			return
		}
		if *b.Status == "on_hold" && !cur.OnHold() {
			mzDetail(w, http.StatusBadRequest, "Switching an existing user to on_hold is not supported; create it with status on_hold or set expire")
			return
		}
	}

	var upd store.UpdateAccountParams
	haveUpd := false

	if p.expire != nil {
		switch {
		case *p.expire == 0 && cur.OnHold():
			// Marzban reports expire 0 for an on-hold user; echoing it back is a no-op.
		case *p.expire == 0:
			if cur.ExpiryAt != nil {
				a, err := s.Store.ClearAccountExpiry(ctx, id, c.NS)
				if err != nil {
					fail(err)
					return
				}
				cur = a
				fields = append(fields, "expire")
			}
		case cur.OnHold():
			t := time.Unix(*p.expire, 0)
			upd.ExpiryAt, haveUpd = &t, true
			fields = append(fields, "expire")
		default:
			t := time.Unix(*p.expire, 0)
			a, err := s.renewAccountTo(ctx, id, c.NS, t)
			if err != nil {
				fail(err)
				return
			}
			cur = a
			fields = append(fields, "expire")
		}
	}
	if p.dataLimit != nil {
		if *p.dataLimit == 0 {
			upd.ClearDataQuota = true
		} else {
			upd.DataQuotaBytes = p.dataLimit
		}
		haveUpd = true
		fields = append(fields, "data_limit")
	}
	if p.resetStrategy != nil {
		upd.ResetStrategy, haveUpd = p.resetStrategy, true
		fields = append(fields, "data_limit_reset_strategy")
	}
	if p.holdDays != nil && cur.OnHold() {
		upd.HoldDurationDays, haveUpd = p.holdDays, true
		fields = append(fields, "on_hold_expire_duration")
	}
	if haveUpd {
		a, err := s.Store.UpdateAccount(ctx, id, c.NS, upd)
		if err != nil {
			fail(err)
			return
		}
		cur = a
	}

	if b.Status != nil {
		switch *b.Status {
		case "disabled":
			if cur.Status != "suspended" || mzStatus(cur, time.Now()) != "disabled" {
				a, err := s.Store.SuspendAccount(ctx, id, c.NS, "manual")
				if err != nil {
					fail(err)
					return
				}
				cur = a
				s.emitAccountEvent(events.AccountSuspended, cur)
				fields = append(fields, "status")
			}
		case "active":
			if cur.Status == "suspended" {
				a, err := s.Store.EnableAccount(ctx, id, c.NS)
				if err != nil {
					fail(err)
					return
				}
				cur = a
				s.emitAccountEvent(events.AccountEnabled, cur)
				fields = append(fields, "status")
			}
		}
	}

	// Re-read so the response reflects any limit enforcement the writes triggered.
	final, err := s.Store.GetAccount(ctx, id, c.NS)
	if err != nil {
		fail(err)
		return
	}
	s.mzAudit(r, c, "account.updated", final, map[string]any{"fields": fields})
	s.mzRespondUser(w, r, final, c)
}

// renewAccountTo sets the validity end to t through the renew path, so an expired (or
// quota-suspended-and-now-within-cap) account is reactivated the way a renewal does.
func (s *Server) renewAccountTo(ctx context.Context, id string, ns *string, t time.Time) (store.Account, error) {
	return s.Store.RenewAccountHold(ctx, id, ns, nil, &t, nil)
}

// ---- DELETE /api/user/{username} ----

func (s *Server) handleMzDeleteUser(w http.ResponseWriter, r *http.Request, c mzCaller) {
	a, ok := s.mzLoad(w, r, c)
	if !ok {
		return
	}
	deleted, err := s.Store.SoftDeleteAccount(r.Context(), a.ID, c.NS)
	if err != nil {
		s.mzStoreErr(w, err, "delete")
		return
	}
	s.emitAccountEvent(events.AccountDeleted, deleted)
	s.mzAudit(r, c, "account.deleted", deleted, nil)
	writeJSON(w, http.StatusOK, map[string]any{})
}

// ---- POST /api/user/{username}/reset, /revoke_sub, /active-next ----

func (s *Server) handleMzResetUser(w http.ResponseWriter, r *http.Request, c mzCaller) {
	a, ok := s.mzLoad(w, r, c)
	if !ok {
		return
	}
	reset, err := s.Store.ResetAccountUsage(r.Context(), a.ID, c.NS)
	if err != nil {
		s.mzStoreErr(w, err, "reset")
		return
	}
	s.mzAudit(r, c, "account.usage_reset", reset, nil)
	s.mzRespondUser(w, r, reset, c)
}

func (s *Server) handleMzRevokeSub(w http.ResponseWriter, r *http.Request, c mzCaller) {
	a, ok := s.mzLoad(w, r, c)
	if !ok {
		return
	}
	token, err := newSubscriptionToken()
	if err != nil {
		s.Logger.Error("generate_subscription_token_failed", "error", err)
		mzDetail(w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	rotated, err := s.Store.RotateSubscriptionToken(r.Context(), a.ID, c.NS, token)
	if err != nil {
		s.mzStoreErr(w, err, "update")
		return
	}
	s.mzAudit(r, c, "account.subscription_rotated", rotated, nil)
	s.mzRespondUser(w, r, rotated, c)
}

// handleMzActiveNext: Marzban's "next plan" queue has no counterpart here, so no user
// ever has one; the answer is Marzban's own for that case.
func (s *Server) handleMzActiveNext(w http.ResponseWriter, r *http.Request, c mzCaller) {
	if _, ok := s.mzLoad(w, r, c); !ok {
		return
	}
	mzDetail(w, http.StatusNotFound, "User doesn't have next plan")
}

// ---- GET /api/user/{username}/usage ----

// handleMzUserUsage: we meter per account, not per node, so the single "Master" entry
// carries the account's usage since its last reset. start/end are accepted and ignored.
func (s *Server) handleMzUserUsage(w http.ResponseWriter, r *http.Request, c mzCaller) {
	a, ok := s.mzLoad(w, r, c)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"username": *a.ExternalRef,
		"usages":   []map[string]any{{"node_id": nil, "node_name": "Master", "used_traffic": a.DataUsedBytes}},
	})
}

// ---- GET /api/users ----

const mzMaxListLimit = 1000

var mzValidStatuses = map[string]bool{"active": true, "disabled": true, "limited": true, "expired": true, "on_hold": true}

func (s *Server) handleMzListUsers(w http.ResponseWriter, r *http.Request, c mzCaller) {
	q := r.URL.Query()
	var errs []mzFieldError
	intParam := func(name string) *int {
		if !q.Has(name) {
			return nil
		}
		n, err := strconv.Atoi(q.Get(name))
		if err != nil || n < 0 {
			errs = append(errs, mzFieldError{Loc: []string{"query", name}, Msg: "value is not a valid non-negative integer", Type: "type_error.integer"})
			return nil
		}
		return &n
	}
	offset, limit := intParam("offset"), intParam("limit")
	statusFilter := q.Get("status")
	if statusFilter != "" && !mzValidStatuses[statusFilter] {
		errs = append(errs, mzFieldError{Loc: []string{"query", "status"}, Msg: "value is not a valid enumeration member; permitted: 'active', 'disabled', 'limited', 'expired', 'on_hold'", Type: "type_error.enum"})
	}
	sortKeys, serr := mzParseSort(q.Get("sort"))
	if serr != "" {
		errs = append(errs, mzFieldError{Loc: []string{"query", "sort"}, Msg: serr, Type: "value_error"})
	}
	if len(errs) > 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": errs})
		return
	}

	ctx := r.Context()
	accounts, err := s.Store.ListAccountsForCompat(ctx, c.NS, q.Get("search"), q["username"])
	if err != nil {
		s.Logger.Error("marzban_compat_list_failed", "error", err)
		mzDetail(w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	now := time.Now()
	if statusFilter != "" {
		kept := accounts[:0]
		for _, a := range accounts {
			if mzStatus(a, now) == statusFilter {
				kept = append(kept, a)
			}
		}
		accounts = kept
	}
	mzSortAccounts(accounts, sortKeys)
	total := len(accounts)

	lo, hi := 0, total
	if offset != nil {
		lo = min(*offset, total)
	}
	size := mzMaxListLimit
	if limit != nil && *limit < size {
		size = *limit
	}
	hi = min(lo+size, total)
	page := accounts[lo:hi]

	origin := s.mzOrigin(s.mzSettings(ctx))
	users := make([]mzUserResponse, 0, len(page))
	for _, a := range page {
		u, err := s.mzUser(ctx, a, c.NS, origin)
		if err != nil {
			s.Logger.Error("marzban_compat_render_user_failed", "error", err)
			mzDetail(w, http.StatusInternalServerError, "Internal Server Error")
			return
		}
		users = append(users, u)
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users, "total": total})
}

type mzSortKey struct {
	field string
	desc  bool
}

var mzSortFields = map[string]bool{"username": true, "used_traffic": true, "data_limit": true, "expire": true, "created_at": true}

// mzParseSort reads Marzban's comma-separated sort list ("-used_traffic,username").
func mzParseSort(v string) ([]mzSortKey, string) {
	if v == "" {
		return nil, ""
	}
	var keys []mzSortKey
	for _, part := range strings.Split(v, ",") {
		k := mzSortKey{field: strings.TrimPrefix(strings.TrimSpace(part), "-")}
		k.desc = strings.HasPrefix(strings.TrimSpace(part), "-")
		if !mzSortFields[k.field] {
			return nil, "invalid sort option '" + part + "'; permitted: username, used_traffic, data_limit, expire, created_at, each optionally prefixed with '-'"
		}
		keys = append(keys, k)
	}
	return keys, ""
}

func mzSortAccounts(list []store.Account, keys []mzSortKey) {
	if len(keys) == 0 {
		return // the store's order: oldest first
	}
	val := func(a store.Account, field string) (string, int64) {
		switch field {
		case "username":
			if a.ExternalRef != nil {
				return strings.ToLower(*a.ExternalRef), 0
			}
		case "used_traffic":
			return "", a.DataUsedBytes
		case "data_limit":
			if a.DataQuotaBytes != nil {
				return "", *a.DataQuotaBytes
			}
		case "expire":
			if a.ExpiryAt != nil {
				return "", a.ExpiryAt.Unix()
			}
		case "created_at":
			return "", a.CreatedAt.UnixNano()
		}
		return "", 0
	}
	sort.SliceStable(list, func(i, j int) bool {
		for _, k := range keys {
			si, ni := val(list[i], k.field)
			sj, nj := val(list[j], k.field)
			if si == sj && ni == nj {
				continue
			}
			less := si < sj || (si == sj && ni < nj)
			if k.desc {
				return !less
			}
			return less
		}
		return false
	})
}

// ---- GET/DELETE /api/users/expired ----

// mzExpiredUsers returns the usernames whose expire lies in [after, before] (both
// optional) and has passed. Marzban takes datetimes; unix seconds are accepted too.
func (s *Server) mzExpiredUsers(w http.ResponseWriter, r *http.Request, c mzCaller) ([]store.Account, bool) {
	q := r.URL.Query()
	var after, before *time.Time
	for name, dst := range map[string]**time.Time{"expired_after": &after, "expired_before": &before} {
		if v := q.Get(name); v != "" {
			t, ok := mzParseTime(v)
			if !ok {
				writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": []mzFieldError{{Loc: []string{"query", name}, Msg: "invalid datetime format", Type: "value_error.datetime"}}})
				return nil, false
			}
			*dst = &t
		}
	}
	accounts, err := s.Store.ListAccountsForCompat(r.Context(), c.NS, "", nil)
	if err != nil {
		s.Logger.Error("marzban_compat_list_failed", "error", err)
		mzDetail(w, http.StatusInternalServerError, "Internal Server Error")
		return nil, false
	}
	now := time.Now()
	var out []store.Account
	for _, a := range accounts {
		if a.ExpiryAt == nil || a.ExpiryAt.After(now) {
			continue
		}
		if (after != nil && a.ExpiryAt.Before(*after)) || (before != nil && a.ExpiryAt.After(*before)) {
			continue
		}
		out = append(out, a)
	}
	return out, true
}

func (s *Server) handleMzListExpired(w http.ResponseWriter, r *http.Request, c mzCaller) {
	expired, ok := s.mzExpiredUsers(w, r, c)
	if !ok {
		return
	}
	names := make([]string, 0, len(expired))
	for _, a := range expired {
		names = append(names, *a.ExternalRef)
	}
	writeJSON(w, http.StatusOK, names)
}

func (s *Server) handleMzDeleteExpired(w http.ResponseWriter, r *http.Request, c mzCaller) {
	expired, ok := s.mzExpiredUsers(w, r, c)
	if !ok {
		return
	}
	names := make([]string, 0, len(expired))
	for _, a := range expired {
		deleted, err := s.Store.SoftDeleteAccount(r.Context(), a.ID, c.NS)
		if errors.Is(err, store.ErrAccountNotFound) {
			continue // deleted meanwhile
		}
		if err != nil {
			s.mzStoreErr(w, err, "delete")
			return
		}
		s.emitAccountEvent(events.AccountDeleted, deleted)
		s.mzAudit(r, c, "account.deleted", deleted, map[string]any{"reason": "expired_cleanup"})
		names = append(names, *a.ExternalRef)
	}
	writeJSON(w, http.StatusOK, names)
}
