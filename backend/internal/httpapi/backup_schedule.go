package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"wgpanel-api/internal/backupcrypto"
	"wgpanel-api/internal/events"
	"wgpanel-api/internal/s3lite"
	"wgpanel-api/internal/store"
	"wgpanel-api/internal/wgkeys"
)

const backupFilePrefix = "wgpanel-backup-"

type backupRunResponse struct {
	RunID       string    `json:"run_id"`
	Destination string    `json:"destination"`
	Trigger     string    `json:"trigger"`
	Status      string    `json:"status"`
	Error       string    `json:"error"`
	Filename    string    `json:"filename"`
	SizeBytes   int64     `json:"size_bytes"`
	CreatedAt   time.Time `json:"created_at"`
}

func toBackupRunResponse(r store.BackupRun) backupRunResponse {
	return backupRunResponse{RunID: r.RunID, Destination: r.Destination, Trigger: r.Trigger, Status: r.Status, Error: r.Error, Filename: r.Filename, SizeBytes: r.SizeBytes, CreatedAt: r.CreatedAt}
}

type backupScheduleBody struct {
	Enabled        bool   `json:"enabled"`
	IntervalHours  int    `json:"interval_hours"`
	RetentionCount int    `json:"retention_count"`
	Password       string `json:"password,omitempty"` // write-only; empty on update = keep
	HasPassword    bool   `json:"has_password"`
	Telegram       struct {
		Enabled  bool   `json:"enabled"`
		BotToken string `json:"bot_token,omitempty"` // write-only
		HasToken bool   `json:"has_token"`
		ChatID   string `json:"chat_id"`
	} `json:"telegram"`
	S3 struct {
		Enabled   bool   `json:"enabled"`
		Endpoint  string `json:"endpoint"`
		Region    string `json:"region"`
		Bucket    string `json:"bucket"`
		Prefix    string `json:"prefix"`
		AccessKey string `json:"access_key"`
		SecretKey string `json:"secret_key,omitempty"` // write-only
		HasSecret bool   `json:"has_secret"`
		PathStyle bool   `json:"path_style"`
	} `json:"s3"`
}

type backupScheduleResponse struct {
	backupScheduleBody
	LastRunAt *time.Time                   `json:"last_run_at"`
	Running   bool                         `json:"running"`
	Last      map[string]backupRunResponse `json:"last"`
	Runs      []backupRunResponse          `json:"runs"`
}

func (s *Server) scheduleResponse(ctx context.Context) (backupScheduleResponse, error) {
	cfg, err := s.Store.GetBackupSchedule(ctx)
	if err != nil {
		return backupScheduleResponse{}, err
	}
	var out backupScheduleResponse
	b := &out.backupScheduleBody
	b.Enabled, b.IntervalHours, b.RetentionCount, b.HasPassword = cfg.Enabled, cfg.IntervalHours, cfg.RetentionCount, cfg.PasswordEncrypted != ""
	b.Telegram.Enabled, b.Telegram.HasToken, b.Telegram.ChatID = cfg.TelegramEnabled, cfg.TelegramTokenEncrypted != "", cfg.TelegramChatID
	b.S3.Enabled, b.S3.Endpoint, b.S3.Region, b.S3.Bucket, b.S3.Prefix = cfg.S3Enabled, cfg.S3Endpoint, cfg.S3Region, cfg.S3Bucket, cfg.S3Prefix
	b.S3.AccessKey, b.S3.HasSecret, b.S3.PathStyle = cfg.S3AccessKey, cfg.S3SecretEncrypted != "", cfg.S3PathStyle
	out.LastRunAt = cfg.LastRunAt
	out.Running = s.NotifyRT.backupRunning.Load()
	out.Last = map[string]backupRunResponse{}
	last, err := s.Store.LastBackupRunPerDestination(ctx)
	if err != nil {
		return out, err
	}
	for k, v := range last {
		out.Last[k] = toBackupRunResponse(v)
	}
	runs, err := s.Store.ListBackupRuns(ctx, 20)
	if err != nil {
		return out, err
	}
	out.Runs = make([]backupRunResponse, 0, len(runs))
	for _, r := range runs {
		out.Runs = append(out.Runs, toBackupRunResponse(r))
	}
	return out, nil
}

func (s *Server) handleGetBackupSchedule(w http.ResponseWriter, r *http.Request) {
	out, err := s.scheduleResponse(r.Context())
	if err != nil {
		s.Logger.Error("get_backup_schedule_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not load the backup schedule")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleUpdateBackupSchedule(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req backupScheduleBody
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	cur, err := s.Store.GetBackupSchedule(ctx)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not load the backup schedule")
		return
	}
	bad := func(msg string) { writeJSONError(w, http.StatusBadRequest, "invalid_request", msg) }
	if req.IntervalHours < 1 || req.IntervalHours > 24*365 {
		bad("interval_hours must be between 1 and 8760")
		return
	}
	if req.RetentionCount < 1 || req.RetentionCount > 365 {
		bad("retention_count must be between 1 and 365")
		return
	}
	enc := func(plain, current string) (string, error) {
		if plain == "" {
			return current, nil
		}
		return wgkeys.Encrypt(s.AccountKeyEncryptionKey, plain)
	}
	next := cur
	next.Enabled, next.IntervalHours, next.RetentionCount = req.Enabled, req.IntervalHours, req.RetentionCount
	if req.Password != "" && len(req.Password) < backupPasswordMinLen {
		bad(fmt.Sprintf("the backup password must be at least %d characters - it is the ONLY way to open the backups", backupPasswordMinLen))
		return
	}
	var e1, e2, e3 error
	next.PasswordEncrypted, e1 = enc(req.Password, cur.PasswordEncrypted)
	next.TelegramEnabled, next.TelegramChatID = req.Telegram.Enabled, strings.TrimSpace(req.Telegram.ChatID)
	next.TelegramTokenEncrypted, e2 = enc(req.Telegram.BotToken, cur.TelegramTokenEncrypted)
	next.S3Enabled, next.S3Endpoint, next.S3Region = req.S3.Enabled, strings.TrimSpace(req.S3.Endpoint), strings.TrimSpace(req.S3.Region)
	next.S3Bucket, next.S3Prefix, next.S3AccessKey = strings.TrimSpace(req.S3.Bucket), strings.Trim(strings.TrimSpace(req.S3.Prefix), "/"), strings.TrimSpace(req.S3.AccessKey)
	next.S3PathStyle = req.S3.PathStyle
	next.S3SecretEncrypted, e3 = enc(req.S3.SecretKey, cur.S3SecretEncrypted)
	if e1 != nil || e2 != nil || e3 != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not store credentials")
		return
	}
	if next.S3Region == "" {
		next.S3Region = "us-east-1"
	}
	if (next.Enabled || next.TelegramEnabled || next.S3Enabled) && next.PasswordEncrypted == "" {
		bad("set a backup password first - backups are encrypted with it")
		return
	}
	if next.TelegramEnabled && (next.TelegramTokenEncrypted == "" || next.TelegramChatID == "") {
		bad("the Telegram destination needs a bot token and a chat id")
		return
	}
	if next.S3Enabled {
		if next.S3Bucket == "" || next.S3AccessKey == "" || next.S3SecretEncrypted == "" {
			bad("the S3 destination needs a bucket, an access key and a secret key")
			return
		}
		if next.S3Endpoint != "" {
			u, err := url.Parse(next.S3Endpoint)
			if err != nil || (u.Scheme != "" && u.Scheme != "http" && u.Scheme != "https") || (u.Scheme != "" && u.Host == "") {
				bad("the S3 endpoint must be a http(s) URL")
				return
			}
		}
	}
	if next.Enabled && !next.TelegramEnabled && !next.S3Enabled {
		bad("enable at least one destination (Telegram or S3) for scheduled backups")
		return
	}
	if err := s.Store.SaveBackupSchedule(ctx, next); err != nil {
		s.Logger.Error("save_backup_schedule_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not save the backup schedule")
		return
	}
	s.auditAdmin(r, "backup_schedule.updated", "panel", nil)
	out, err := s.scheduleResponse(ctx)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not load the backup schedule")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleRunBackupNow starts a backup in the background and answers 202; the page polls
// GET /backup-schedule for the per-destination result.
func (s *Server) handleRunBackupNow(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.Store.GetBackupSchedule(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not load the backup schedule")
		return
	}
	if cfg.PasswordEncrypted == "" || (!cfg.TelegramEnabled && !cfg.S3Enabled) {
		writeJSONError(w, http.StatusBadRequest, "backup_not_configured", "set a backup password and enable a destination first")
		return
	}
	if !s.NotifyRT.backupRunning.CompareAndSwap(false, true) {
		writeJSONError(w, http.StatusConflict, "backup_in_progress", "a backup is already running")
		return
	}
	s.auditAdmin(r, "backup_schedule.run_now", "panel", nil)
	_ = s.Store.TouchBackupRun(r.Context())
	go func() {
		defer s.NotifyRT.backupRunning.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		s.runBackup(ctx, "manual")
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{"started": true})
}

func (s *Server) runScheduledBackupIfDue(ctx context.Context) {
	if s.NotifyRT.backupRunning.Load() {
		return
	}
	ok, err := s.Store.ClaimScheduledBackup(ctx)
	if err != nil {
		s.Logger.Error("claim_scheduled_backup_failed", "error", err)
		return
	}
	if !ok || !s.NotifyRT.backupRunning.CompareAndSwap(false, true) {
		return
	}
	defer s.NotifyRT.backupRunning.Store(false)
	bctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	s.runBackup(bctx, "schedule")
}

// runBackup builds one encrypted backup (the same envelope the manual download makes,
// restorable through the same restore path) and pushes it to every enabled
// destination, recording a result row and a backup.succeeded / backup.failed event per
// destination. Returns the per-destination errors for tests.
func (s *Server) runBackup(ctx context.Context, trigger string) map[string]error {
	results := map[string]error{}
	cfg, err := s.Store.GetBackupSchedule(ctx)
	if err != nil {
		s.Logger.Error("backup_run_load_failed", "error", err)
		return results
	}
	var dests []string
	if cfg.TelegramEnabled {
		dests = append(dests, "telegram")
	}
	if cfg.S3Enabled {
		dests = append(dests, "s3")
	}
	runID := newUUID()
	createdAt := time.Now().UTC()
	filename := backupFilePrefix + createdAt.Format("20060102T150405Z") + ".json"

	record := func(dest string, size int, err error) {
		results[dest] = err
		row := store.BackupRun{RunID: runID, Destination: dest, Trigger: trigger, Status: "success", Filename: filename, SizeBytes: int64(size)}
		data := map[string]any{"destination": dest, "filename": filename, "size_bytes": size, "trigger": trigger}
		typ := events.BackupSucceeded
		if err != nil {
			row.Status, row.Error = "failed", err.Error()
			data["error"] = err.Error()
			typ = events.BackupFailed
		}
		if ierr := s.Store.InsertBackupRun(ctx, row); ierr != nil {
			s.Logger.Error("insert_backup_run_failed", "error", ierr)
		}
		s.emit(typ, "", data)
	}

	file, err := s.sealScheduledBackup(ctx, cfg, createdAt)
	if err != nil {
		s.Logger.Error("scheduled_backup_build_failed", "error", err)
		for _, d := range dests {
			record(d, 0, err)
		}
		return results
	}
	for _, d := range dests {
		switch d {
		case "telegram":
			record(d, len(file), s.uploadBackupTelegram(ctx, cfg, filename, file))
		case "s3":
			err := s.uploadBackupS3(ctx, cfg, filename, file)
			record(d, len(file), err)
		}
	}
	return results
}

func (s *Server) sealScheduledBackup(ctx context.Context, cfg store.BackupSchedule, createdAt time.Time) ([]byte, error) {
	password, err := s.decryptSecret(cfg.PasswordEncrypted)
	if err != nil || password == "" {
		return nil, errors.New("the backup password is missing or could not be decrypted - set it again")
	}
	payload, err := s.buildBackupPayload(ctx)
	if err != nil {
		return nil, err
	}
	env, err := backupcrypto.Seal(password, payload, createdAt.Format(time.RFC3339))
	if err != nil {
		return nil, fmt.Errorf("encrypt backup: %w", err)
	}
	return json.Marshal(env)
}

func (s *Server) uploadBackupTelegram(ctx context.Context, cfg store.BackupSchedule, filename string, file []byte) error {
	token, err := s.decryptSecret(cfg.TelegramTokenEncrypted)
	if err != nil || token == "" {
		return errors.New("the Telegram bot token could not be decrypted - set it again")
	}
	return s.sendTelegramDocument(ctx, token, cfg.TelegramChatID, filename, "Jirjirak WireGuard backup "+filename, file)
}

func (s *Server) s3Client(cfg store.BackupSchedule) (*s3lite.Client, error) {
	secret, err := s.decryptSecret(cfg.S3SecretEncrypted)
	if err != nil || secret == "" {
		return nil, errors.New("the S3 secret key could not be decrypted - set it again")
	}
	return s3lite.New(s3lite.Config{
		Endpoint: cfg.S3Endpoint, Region: cfg.S3Region, Bucket: cfg.S3Bucket,
		AccessKey: cfg.S3AccessKey, SecretKey: secret, PathStyle: cfg.S3PathStyle,
	}, s.NotifyRT.HTTP), nil
}

func (s *Server) uploadBackupS3(ctx context.Context, cfg store.BackupSchedule, filename string, file []byte) error {
	c, err := s.s3Client(cfg)
	if err != nil {
		return err
	}
	prefix := ""
	if cfg.S3Prefix != "" {
		prefix = cfg.S3Prefix + "/"
	}
	if err := c.Put(ctx, prefix+filename, file); err != nil {
		return err
	}
	// Retention: keep the newest N backups under the prefix. A failure to prune must
	// not fail the (already successful) upload, so it is only logged.
	if err := pruneS3Backups(ctx, c, prefix, cfg.RetentionCount); err != nil {
		s.Logger.Warn("backup_retention_failed", "error", err)
	}
	return nil
}

// pruneS3Backups deletes the oldest wgpanel-backup-*.json objects under prefix beyond
// keep. File names embed a UTC timestamp, so lexical order is chronological order.
func pruneS3Backups(ctx context.Context, c *s3lite.Client, prefix string, keep int) error {
	objs, err := c.List(ctx, prefix+backupFilePrefix)
	if err != nil {
		return err
	}
	var keys []string
	for _, o := range objs {
		if strings.HasSuffix(o.Key, ".json") {
			keys = append(keys, o.Key)
		}
	}
	sort.Strings(keys)
	for len(keys) > keep {
		if err := c.Delete(ctx, keys[0]); err != nil {
			return err
		}
		keys = keys[1:]
	}
	return nil
}

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
