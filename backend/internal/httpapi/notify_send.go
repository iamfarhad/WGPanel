package httpapi

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"wgpanel-api/internal/events"
	"wgpanel-api/internal/store"
	"wgpanel-api/internal/wgkeys"
)

// NotifyRuntime holds the overridable bits of the notification/webhook/backup
// machinery. The zero value is production behaviour; tests point these at httptest
// servers so no real Telegram/S3/webhook traffic is ever sent.
type NotifyRuntime struct {
	// TelegramAPIBase defaults to https://api.telegram.org.
	TelegramAPIBase string
	// HTTP is used for Telegram and S3 calls; nil = a client with a 30s timeout.
	HTTP *http.Client
	// StaffWebhookHTTP / ResellerWebhookHTTP replace the SSRF-guarded webhook clients.
	StaffWebhookHTTP    *http.Client
	ResellerWebhookHTTP *http.Client
	// SendEmail replaces the SMTP sender in tests.
	SendEmail func(cfg emailConfig, password, subject, body string) error

	backupRunning     atomic.Bool
	mu                sync.Mutex
	once              sync.Once
	staff, reseller   *http.Client
	allowPrivateAt    time.Time
	allowPrivateValue bool
}

type telegramConfig struct {
	ChatIDs []string `json:"chat_ids"`
}

type emailConfig struct {
	Host     string   `json:"host"`
	Port     int      `json:"port"`
	Username string   `json:"username"`
	From     string   `json:"from"`
	To       []string `json:"to"`
	TLS      string   `json:"tls"` // starttls | tls | none
}

func (s *Server) telegramBase() string {
	if s.NotifyRT.TelegramAPIBase != "" {
		return strings.TrimRight(s.NotifyRT.TelegramAPIBase, "/")
	}
	return "https://api.telegram.org"
}

func (s *Server) outboundHTTP() *http.Client {
	if s.NotifyRT.HTTP != nil {
		return s.NotifyRT.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// scrubSecret removes a secret from an error message (Go's http errors embed the
// request URL, which for Telegram contains the bot token).
func scrubSecret(err error, secret string) error {
	if err == nil {
		return nil
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = fmt.Errorf("%s: %w", ue.Op, ue.Err)
	}
	if secret != "" {
		return errors.New(strings.ReplaceAll(err.Error(), secret, "***"))
	}
	return err
}

func (s *Server) decryptSecret(enc string) (string, error) {
	if enc == "" {
		return "", nil
	}
	return wgkeys.Decrypt(s.AccountKeyEncryptionKey, enc)
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// sendTelegramMessage posts one message to every chat id; all must succeed.
func (s *Server) sendTelegramMessage(ctx context.Context, token string, chatIDs []string, text string) error {
	var firstErr error
	for _, chat := range chatIDs {
		body, _ := json.Marshal(map[string]any{
			"chat_id": chat, "text": text, "parse_mode": "HTML", "disable_web_page_preview": true,
		})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.telegramBase()+"/bot"+token+"/sendMessage", bytes.NewReader(body))
		if err != nil {
			return scrubSecret(err, token)
		}
		req.Header.Set("Content-Type", "application/json")
		if err := s.doTelegram(req, token); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (s *Server) doTelegram(req *http.Request, token string) error {
	resp, err := s.outboundHTTP().Do(req)
	if err != nil {
		return scrubSecret(err, token)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var res struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	_ = json.Unmarshal(data, &res)
	if resp.StatusCode/100 != 2 || !res.OK {
		desc := res.Description
		if desc == "" {
			desc = "HTTP " + strconv.Itoa(resp.StatusCode)
		}
		return fmt.Errorf("telegram: %s", scrubSecret(errors.New(desc), token))
	}
	return nil
}

// telegramMaxDocumentBytes is the Bot API's upload ceiling for sendDocument.
const telegramMaxDocumentBytes = 50 << 20

var errTelegramTooLarge = fmt.Errorf("backup is larger than Telegram's %d MB bot upload limit - use an S3 destination for backups this big", telegramMaxDocumentBytes>>20)

// sendTelegramDocument uploads a file to one chat.
func (s *Server) sendTelegramDocument(ctx context.Context, token, chatID, filename, caption string, data []byte) error {
	if len(data) > telegramMaxDocumentBytes {
		return errTelegramTooLarge
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("chat_id", chatID)
	_ = mw.WriteField("caption", caption)
	fw, err := mw.CreateFormFile("document", filename)
	if err != nil {
		return err
	}
	_, _ = fw.Write(data)
	_ = mw.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.telegramBase()+"/bot"+token+"/sendDocument", &buf)
	if err != nil {
		return scrubSecret(err, token)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return s.doTelegram(req, token)
}

// buildEmailMessage renders an RFC 5322 message with a UTF-8 subject and body.
func buildEmailMessage(from string, to []string, subject, body string) []byte {
	var b bytes.Buffer
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + strings.Join(to, ", ") + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", subject) + "\r\n")
	b.WriteString("Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	b.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	b.WriteString("\r\n")
	return b.Bytes()
}

func sendSMTP(cfg emailConfig, password, subject, body string) error {
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	var conn net.Conn
	var err error
	tlsCfg := &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}
	if cfg.TLS == "tls" {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsCfg)
	} else {
		conn, err = dialer.Dial("tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("smtp connect: %w", err)
	}
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	c, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("smtp: %w", err)
	}
	defer c.Close()
	if cfg.TLS == "starttls" {
		if err := c.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("smtp starttls: %w", err)
		}
	}
	if cfg.Username != "" {
		if err := c.Auth(plainAuthAnyTLS{user: cfg.Username, pass: password, host: cfg.Host, secure: cfg.TLS != "none"}); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := c.Mail(cfg.From); err != nil {
		return fmt.Errorf("smtp: %w", err)
	}
	for _, rcpt := range cfg.To {
		if err := c.Rcpt(rcpt); err != nil {
			return fmt.Errorf("smtp rcpt %s: %w", rcpt, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp: %w", err)
	}
	if _, err := w.Write(buildEmailMessage(cfg.From, cfg.To, subject, body)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// plainAuthAnyTLS is PLAIN auth that refuses to send credentials over an unencrypted
// connection unless the admin explicitly chose tls=none.
type plainAuthAnyTLS struct {
	user, pass, host string
	secure           bool
}

func (a plainAuthAnyTLS) Start(*smtp.ServerInfo) (string, []byte, error) {
	return "PLAIN", []byte("\x00" + a.user + "\x00" + a.pass), nil
}

func (a plainAuthAnyTLS) Next(_ []byte, more bool) ([]byte, error) {
	if more {
		return nil, errors.New("unexpected server challenge")
	}
	return nil, nil
}

// sendToChannel delivers one rendered notification through a channel and records the
// outcome (failure streak / last error) on it.
func (s *Server) sendToChannel(ctx context.Context, ch store.NotificationChannel, sev string, r rendered) error {
	err := s.sendToChannelRaw(ctx, ch, sev, r)
	if rerr := s.Store.RecordChannelResult(ctx, ch.ID, err); rerr != nil {
		s.Logger.Error("record_channel_result_failed", "error", rerr)
	}
	return err
}

func (s *Server) sendToChannelRaw(ctx context.Context, ch store.NotificationChannel, sev string, r rendered) error {
	secret, err := s.decryptSecret(ch.SecretEncrypted)
	if err != nil {
		return errors.New("stored credentials could not be decrypted")
	}
	switch ch.Kind {
	case "telegram":
		var cfg telegramConfig
		if err := json.Unmarshal(ch.Config, &cfg); err != nil || len(cfg.ChatIDs) == 0 {
			return errors.New("no chat ids configured")
		}
		text := "<b>" + htmlEscape(severityTag(sev, ch.Lang)+" "+r.Title) + "</b>\n" + htmlEscape(r.Body)
		return s.sendTelegramMessage(ctx, secret, cfg.ChatIDs, text)
	case "email":
		var cfg emailConfig
		if err := json.Unmarshal(ch.Config, &cfg); err != nil || cfg.Host == "" || len(cfg.To) == 0 {
			return errors.New("email channel is not fully configured")
		}
		subject := "[Jirjirak] " + r.Title
		send := sendSMTP
		if s.NotifyRT.SendEmail != nil {
			send = s.NotifyRT.SendEmail
		}
		return send(cfg, secret, subject, severityTag(sev, ch.Lang)+" "+r.Title+"\n\n"+r.Body)
	}
	return errors.New("unknown channel kind")
}

// deliverToChannels fans an event out to every enabled channel subscribed to it. A
// critical event whose Telegram delivery failed additionally falls back to every
// enabled email channel not already tried.
func (s *Server) deliverToChannels(ctx context.Context, ev events.Event, sev string) {
	channels, err := s.Store.ListChannels(ctx)
	if err != nil {
		s.Logger.Error("list_channels_failed", "error", err)
		return
	}
	tried := map[string]bool{}
	telegramFailed := false
	for _, ch := range channels {
		if !ch.Enabled || !stringSliceContains(ch.Events, ev.Type) {
			continue
		}
		tried[ch.ID] = true
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := s.sendToChannel(cctx, ch, sev, renderEvent(ev, ch.Lang))
		cancel()
		if err != nil {
			s.Logger.Warn("channel_delivery_failed", "channel", ch.ID, "kind", ch.Kind, "error", err)
			if ch.Kind == "telegram" {
				telegramFailed = true
			}
		}
	}
	if sev == events.SeverityCritical && telegramFailed {
		for _, ch := range channels {
			if ch.Enabled && ch.Kind == "email" && !tried[ch.ID] {
				cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
				_ = s.sendToChannel(cctx, ch, sev, renderEvent(ev, ch.Lang))
				cancel()
			}
		}
	}
}
