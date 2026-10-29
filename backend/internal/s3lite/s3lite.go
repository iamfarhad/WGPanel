// Package s3lite is a deliberately tiny S3-compatible client (AWS SigV4) covering
// exactly what scheduled backups need: put, list and delete objects. It works with
// AWS S3, MinIO and ArvanCloud (path-style or virtual-hosted addressing).
package s3lite

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Config describes one bucket.
type Config struct {
	Endpoint  string // e.g. https://s3.example.com ; empty = AWS (https://s3.<region>.amazonaws.com)
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
	PathStyle bool
}

// Client talks to one bucket.
type Client struct {
	cfg  Config
	http *http.Client
	now  func() time.Time
}

// New builds a client; hc may be nil.
func New(cfg Config, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Minute}
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	return &Client{cfg: cfg, http: hc, now: time.Now}
}

// Object is one listed key.
type Object struct {
	Key          string
	Size         int64
	LastModified time.Time
}

// Error is a non-2xx S3 response.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("s3: %s (%s), HTTP %d", e.Message, e.Code, e.Status)
	}
	return fmt.Sprintf("s3: HTTP %d", e.Status)
}

// target resolves the request URL and Host header for a key.
func (c *Client) target(key string) (*url.URL, error) {
	ep := c.cfg.Endpoint
	if ep == "" {
		ep = "https://s3." + c.cfg.Region + ".amazonaws.com"
	}
	if !strings.Contains(ep, "://") {
		ep = "https://" + ep
	}
	u, err := url.Parse(ep)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("invalid s3 endpoint %q", c.cfg.Endpoint)
	}
	key = strings.TrimPrefix(key, "/")
	if c.cfg.PathStyle {
		u.Path = "/" + c.cfg.Bucket
		if key != "" {
			u.Path += "/" + key
		}
	} else {
		u.Host = c.cfg.Bucket + "." + u.Host
		u.Path = "/" + key
	}
	return u, nil
}

// uriEncode is the SigV4 encoding (RFC 3986 unreserved kept; "/" kept unless it is a query value).
func uriEncode(s string, keepSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch >= 'A' && ch <= 'Z', ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9', ch == '-', ch == '_', ch == '.', ch == '~':
			b.WriteByte(ch)
		case ch == '/' && keepSlash:
			b.WriteByte(ch)
		default:
			fmt.Fprintf(&b, "%%%02X", ch)
		}
	}
	return b.String()
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// sign computes the Authorization header for req (body already hashed).
func (c *Client) sign(req *http.Request, query url.Values, payloadHash string, t time.Time) {
	amzDate := t.UTC().Format("20060102T150405Z")
	date := t.UTC().Format("20060102")
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payloadHash)

	keys := make([]string, 0, len(query))
	for k := range query {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var qs []string
	for _, k := range keys {
		vs := append([]string(nil), query[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			qs = append(qs, uriEncode(k, false)+"="+uriEncode(v, false))
		}
	}

	canonicalHeaders := "host:" + req.URL.Host + "\n" +
		"x-amz-content-sha256:" + payloadHash + "\n" +
		"x-amz-date:" + amzDate + "\n"
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	canonicalRequest := strings.Join([]string{
		req.Method,
		uriEncode(req.URL.Path, true),
		strings.Join(qs, "&"),
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	scope := date + "/" + c.cfg.Region + "/s3/aws4_request"
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256Hex([]byte(canonicalRequest))
	k := hmacSHA256([]byte("AWS4"+c.cfg.SecretKey), date)
	k = hmacSHA256(k, c.cfg.Region)
	k = hmacSHA256(k, "s3")
	k = hmacSHA256(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(k, stringToSign))
	req.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		c.cfg.AccessKey, scope, signedHeaders, sig))
}

func (c *Client) do(ctx context.Context, method, key string, query url.Values, body []byte) ([]byte, error) {
	u, err := c.target(key)
	if err != nil {
		return nil, err
	}
	u.RawPath = ""
	if len(query) > 0 {
		// RawQuery mirrors the canonical form so the server computes the same string.
		keys := make([]string, 0, len(query))
		for k := range query {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var qs []string
		for _, k := range keys {
			for _, v := range query[k] {
				qs = append(qs, uriEncode(k, false)+"="+uriEncode(v, false))
			}
		}
		u.RawQuery = strings.Join(qs, "&")
	}
	// Build with the encoded path so what is sent equals what is signed.
	reqURL := *u
	reqURL.RawPath = uriEncode(u.Path, true)
	req, err := http.NewRequestWithContext(ctx, method, reqURL.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.ContentLength = int64(len(body))
	c.sign(req, query, sha256Hex(body), c.now())
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode/100 != 2 {
		var e struct {
			Code    string `xml:"Code"`
			Message string `xml:"Message"`
		}
		_ = xml.Unmarshal(data, &e)
		return nil, &Error{Status: resp.StatusCode, Code: e.Code, Message: e.Message}
	}
	return data, nil
}

// Put uploads body as key.
func (c *Client) Put(ctx context.Context, key string, body []byte) error {
	_, err := c.do(ctx, http.MethodPut, key, nil, body)
	return err
}

// Delete removes key (S3 answers 204 for missing keys too).
func (c *Client) Delete(ctx context.Context, key string) error {
	_, err := c.do(ctx, http.MethodDelete, key, nil, nil)
	return err
}

// List returns every object under prefix (following continuation tokens).
func (c *Client) List(ctx context.Context, prefix string) ([]Object, error) {
	var out []Object
	token := ""
	for {
		q := url.Values{"list-type": {"2"}, "prefix": {prefix}}
		if token != "" {
			q.Set("continuation-token", token)
		}
		data, err := c.do(ctx, http.MethodGet, "", q, nil)
		if err != nil {
			return nil, err
		}
		var res struct {
			Truncated bool   `xml:"IsTruncated"`
			NextToken string `xml:"NextContinuationToken"`
			Contents  []struct {
				Key          string    `xml:"Key"`
				Size         int64     `xml:"Size"`
				LastModified time.Time `xml:"LastModified"`
			} `xml:"Contents"`
		}
		if err := xml.Unmarshal(data, &res); err != nil {
			return nil, fmt.Errorf("s3: bad list response: %w", err)
		}
		for _, o := range res.Contents {
			out = append(out, Object{Key: o.Key, Size: o.Size, LastModified: o.LastModified})
		}
		if !res.Truncated || res.NextToken == "" {
			return out, nil
		}
		token = res.NextToken
	}
}
