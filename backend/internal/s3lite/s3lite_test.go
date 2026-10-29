package s3lite

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"
)

// fakeS3 is an in-memory path-style S3 that verifies the SigV4 signature of every
// request by recomputing it from what it received.
type fakeS3 struct {
	t       *testing.T
	secret  string
	objects map[string][]byte
	mod     map[string]time.Time
}

func (f *fakeS3) verify(r *http.Request, body []byte) {
	f.t.Helper()
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKID/") {
		f.t.Errorf("bad auth header %q", auth)
		return
	}
	c := &Client{cfg: Config{Region: "ir-thr-at1", SecretKey: f.secret, AccessKey: "AKID"}}
	t, err := time.Parse("20060102T150405Z", r.Header.Get("x-amz-date"))
	if err != nil {
		f.t.Errorf("bad date: %v", err)
		return
	}
	if r.Header.Get("x-amz-content-sha256") != sha256Hex(body) {
		f.t.Errorf("payload hash mismatch")
	}
	probe, _ := http.NewRequest(r.Method, "http://"+r.Host+r.URL.EscapedPath(), nil)
	c.sign(probe, r.URL.Query(), r.Header.Get("x-amz-content-sha256"), t)
	if probe.Header.Get("Authorization") != auth {
		f.t.Errorf("signature mismatch:\n got  %s\n want %s", auth, probe.Header.Get("Authorization"))
	}
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.verify(r, body)
	key := strings.TrimPrefix(r.URL.Path, "/bkt/")
	switch {
	case r.Method == http.MethodPut:
		f.objects[key] = body
		f.mod[key] = time.Now().UTC()
	case r.Method == http.MethodDelete:
		delete(f.objects, key)
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && r.URL.Path == "/bkt":
		type content struct {
			Key          string
			Size         int64
			LastModified string
		}
		var res struct {
			XMLName   xml.Name `xml:"ListBucketResult"`
			Truncated bool     `xml:"IsTruncated"`
			Contents  []content
		}
		var keys []string
		for k := range f.objects {
			if strings.HasPrefix(k, r.URL.Query().Get("prefix")) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			res.Contents = append(res.Contents, content{k, int64(len(f.objects[k])), f.mod[k].Format(time.RFC3339)})
		}
		_ = xml.NewEncoder(w).Encode(res)
	default:
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `<Error><Code>AccessDenied</Code><Message>nope</Message></Error>`)
	}
}

func TestPutListDelete(t *testing.T) {
	f := &fakeS3{t: t, secret: "sek", objects: map[string][]byte{}, mod: map[string]time.Time{}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	c := New(Config{Endpoint: srv.URL, Region: "ir-thr-at1", Bucket: "bkt", AccessKey: "AKID", SecretKey: "sek", PathStyle: true}, nil)
	ctx := context.Background()

	for _, k := range []string{"pre fix/a b.json", "pre fix/b.json", "other/c.json"} {
		if err := c.Put(ctx, k, []byte("data-"+k)); err != nil {
			t.Fatalf("put %s: %v", k, err)
		}
	}
	objs, err := c.List(ctx, "pre fix/")
	if err != nil || len(objs) != 2 || objs[0].Key != "pre fix/a b.json" {
		t.Fatalf("list = %+v err=%v", objs, err)
	}
	if err := c.Delete(ctx, "pre fix/a b.json"); err != nil {
		t.Fatal(err)
	}
	if objs, _ = c.List(ctx, "pre fix/"); len(objs) != 1 {
		t.Fatalf("after delete: %+v", objs)
	}
}

func TestWrongSecretAndErrors(t *testing.T) {
	f := &fakeS3{t: &testing.T{}, secret: "right", objects: map[string][]byte{}, mod: map[string]time.Time{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `<Error><Code>SignatureDoesNotMatch</Code><Message>bad sig</Message></Error>`)
	}))
	defer srv.Close()
	_ = f
	c := New(Config{Endpoint: srv.URL, Bucket: "bkt", AccessKey: "a", SecretKey: "b", PathStyle: true}, nil)
	err := c.Put(context.Background(), "k", []byte("x"))
	e, ok := err.(*Error)
	if !ok || e.Status != 403 || e.Code != "SignatureDoesNotMatch" {
		t.Fatalf("err = %#v", err)
	}
}

func TestTargetAddressing(t *testing.T) {
	c := New(Config{Endpoint: "https://s3.ir-thr-at1.arvanstorage.ir", Bucket: "b", PathStyle: true}, nil)
	u, _ := c.target("p/x.json")
	if u.String() != "https://s3.ir-thr-at1.arvanstorage.ir/b/p/x.json" {
		t.Fatalf("path style: %s", u)
	}
	c = New(Config{Region: "eu-west-1", Bucket: "b"}, nil)
	u, _ = c.target("p/x.json")
	if u.String() != "https://b.s3.eu-west-1.amazonaws.com/p/x.json" {
		t.Fatalf("virtual host: %s", u)
	}
	c = New(Config{Endpoint: "minio.local:9000", Bucket: "b", PathStyle: true}, nil)
	u, _ = c.target("")
	if u.String() != "https://minio.local:9000/b" {
		t.Fatalf("scheme default: %s", u)
	}
}

func TestSigningKeyKnownVector(t *testing.T) {
	// From the AWS SigV4 documentation (GET Object example, examplebucket).
	secret := "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	k := hmacSHA256([]byte("AWS4"+secret), "20130524")
	k = hmacSHA256(k, "us-east-1")
	k = hmacSHA256(k, "s3")
	k = hmacSHA256(k, "aws4_request")
	sts := "AWS4-HMAC-SHA256\n20130524T000000Z\n20130524/us-east-1/s3/aws4_request\n7344ae5b7ee6c3e7e6b0fe0640412a37625d1fbfff95c48bbb2dc43964946972"
	got := strings.ToLower(hexOf(hmacSHA256(k, sts)))
	if got != "f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41" {
		t.Fatalf("signature = %s", got)
	}
}

func hexOf(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&15])
	}
	return string(out)
}
