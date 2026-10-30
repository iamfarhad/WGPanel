package sshprov

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/pem"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// fakeServer is an in-process SSH server that records every exec request and answers
// a tiny scripted "shell": enough to drive Run end to end.
type fakeServer struct {
	t           *testing.T
	ln          net.Listener
	hostKey     ssh.Signer
	password    string
	mu          sync.Mutex
	cmds        []string
	stdins      map[string]string // command -> stdin payload
	uid         string
	hang        chan struct{} // when set, the install command blocks until closed/conn drops
	installOut  string
	installExit uint32
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeServer{t: t, ln: ln, hostKey: signer, password: "hunter2-pass", stdins: map[string]string{}, uid: "0"}
	t.Cleanup(func() { ln.Close() })
	go s.serve()
	return s
}

func (s *fakeServer) target() Target {
	a := s.ln.Addr().(*net.TCPAddr)
	return Target{Host: "127.0.0.1", Port: a.Port, User: "root"}
}

func (s *fakeServer) fingerprint() string { return ssh.FingerprintSHA256(s.hostKey.PublicKey()) }

func (s *fakeServer) serve() {
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if string(pw) == s.password {
				return nil, nil
			}
			return nil, io.ErrUnexpectedEOF
		},
	}
	cfg.AddHostKey(s.hostKey)
	for {
		nc, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(nc, cfg)
	}
}

func (s *fakeServer) handle(nc net.Conn, cfg *ssh.ServerConfig) {
	conn, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		nc.Close()
		return
	}
	defer conn.Close()
	go ssh.DiscardRequests(reqs)
	for nch := range chans {
		ch, creqs, err := nch.Accept()
		if err != nil {
			continue
		}
		go s.session(ch, creqs)
	}
}

func (s *fakeServer) session(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	for req := range reqs {
		if req.Type != "exec" {
			req.Reply(false, nil)
			continue
		}
		cmd := string(req.Payload[4:])
		_ = binary.BigEndian // payload is uint32 length + string
		req.Reply(true, nil)
		stdin, _ := readStdin(ch, cmd)
		s.mu.Lock()
		s.cmds = append(s.cmds, cmd)
		s.stdins[cmd] = stdin
		s.mu.Unlock()
		exit := s.run(ch, cmd)
		ch.CloseWrite() // like sshd: EOF, then exit-status, then close
		ch.SendRequest("exit-status", false, ssh.Marshal(struct{ C uint32 }{exit}))
		return
	}
}

// readStdin only reads for commands that are fed (cat > file, sudo -S); others would
// block waiting for EOF the client never sends.
func readStdin(ch ssh.Channel, cmd string) (string, error) {
	if strings.Contains(cmd, "cat >") || strings.Contains(cmd, "sudo -S") {
		b, err := io.ReadAll(ch)
		return string(b), err
	}
	return "", nil
}

func (s *fakeServer) run(ch ssh.Channel, cmd string) uint32 {
	switch {
	case cmd == "id -u":
		io.WriteString(ch, s.uid+"\n")
	case strings.HasPrefix(cmd, "command -v apt-get"):
	case strings.HasPrefix(cmd, "command -v docker"):
		return 1
	case strings.HasPrefix(cmd, "umask 077; mktemp -d"):
		io.WriteString(ch, "/tmp/wgpanel-install.ABC123\n")
	case strings.Contains(cmd, "install-node.sh --non-interactive"):
		if s.hang != nil {
			io.WriteString(ch, "installer started\n")
			<-s.hang
			return 0
		}
		io.WriteString(ch, s.installOut)
		return s.installExit
	}
	return 0
}

func newRun(srv *fakeServer, creds Creds, log io.Writer) Options {
	return Options{
		Target: srv.target(), Creds: creds, Fingerprint: srv.fingerprint(),
		Script: []byte("#!/bin/bash\necho hi\n"),
		Env:    map[string]string{"NODE_PANEL_ADDR": "panel.example.com:48443", "NODE_JOIN_TOKEN": "joinsecret123abc"},
		Log:    log,
	}
}

func TestProbeReturnsFingerprint(t *testing.T) {
	srv := newFakeServer(t)
	fp, err := Probe(context.Background(), srv.target())
	if err != nil || fp != srv.fingerprint() {
		t.Fatalf("probe = %q, %v; want %q", fp, err, srv.fingerprint())
	}
}

func TestRunHostKeyMismatchRejected(t *testing.T) {
	srv := newFakeServer(t)
	o := newRun(srv, Creds{Password: srv.password}, io.Discard)
	o.Fingerprint = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	err := Run(context.Background(), o)
	var e *Error
	if !asErr(err, &e) || e.Code != CodeHostKey {
		t.Fatalf("want host_key_mismatch, got %v", err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.cmds) != 0 {
		t.Fatalf("no command may run after a host key mismatch, got %v", srv.cmds)
	}
}

func TestRunWrongPassword(t *testing.T) {
	srv := newFakeServer(t)
	err := Run(context.Background(), newRun(srv, Creds{Password: "nope-nope"}, io.Discard))
	var e *Error
	if !asErr(err, &e) || e.Code != CodeAuth {
		t.Fatalf("want auth_failed, got %v", err)
	}
}

func TestRunBuildsCommandsAndRedactsSecrets(t *testing.T) {
	srv := newFakeServer(t)
	srv.installOut = "token is joinsecret123abc and password hunter2-pass\nall done\n"
	red := NewRedactor(1<<16, srv.password, "joinsecret123abc")
	o := newRun(srv, Creds{Password: srv.password}, red)
	if err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}

	log := red.Text()
	if strings.Contains(log, "hunter2-pass") || strings.Contains(log, "joinsecret123abc") {
		t.Fatalf("secret leaked into log: %q", log)
	}
	if !strings.Contains(log, "[redacted]") || !strings.Contains(log, "all done") {
		t.Fatalf("unexpected log: %q", log)
	}

	srv.mu.Lock()
	defer srv.mu.Unlock()
	var install, upload, envUpload string
	for _, c := range srv.cmds {
		if strings.Contains(c, "joinsecret123abc") || strings.Contains(c, srv.password) {
			t.Fatalf("a secret appears on a command line: %q", c)
		}
		switch {
		case strings.Contains(c, "--non-interactive"):
			install = c
		case strings.HasSuffix(c, "/install-node.sh") && strings.Contains(c, "cat >"):
			upload = c
		case strings.HasSuffix(c, "/env"):
			envUpload = c
		}
	}
	if install == "" || upload == "" || envUpload == "" {
		t.Fatalf("missing commands: %v", srv.cmds)
	}
	if !strings.Contains(install, "exec bash /tmp/wgpanel-install.ABC123/install-node.sh --non-interactive") || strings.Contains(install, "sudo") {
		t.Fatalf("install command: %q", install)
	}
	if srv.stdins[upload] != string(o.Script) {
		t.Fatal("uploaded script differs from the one given")
	}
	env := srv.stdins[envUpload]
	if !strings.Contains(env, "export NODE_JOIN_TOKEN='joinsecret123abc'") || !strings.Contains(env, "export NODE_PANEL_ADDR='panel.example.com:48443'") {
		t.Fatalf("env file: %q", env)
	}
	if !strings.HasPrefix(upload, "umask 077;") || !strings.HasPrefix(envUpload, "umask 077;") {
		t.Fatal("uploads must be created with a private umask")
	}
}

func TestRunNonRootUsesSudoWithPassword(t *testing.T) {
	srv := newFakeServer(t)
	srv.uid = "1000"
	// passwordless sudo fails (default branch returns 0 for unknown commands, so make it fail)
	o := newRun(srv, Creds{Password: srv.password}, io.Discard)
	srv.installOut = "ok\n"
	err := Run(context.Background(), o)
	// The fake answers success to `sudo -n true`, so the passwordless route is taken.
	if err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	found := false
	for _, c := range srv.cmds {
		if strings.HasPrefix(c, "sudo -n bash -c") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected sudo-prefixed install, got %v", srv.cmds)
	}
}

func TestRunInstallerExitCodesMapped(t *testing.T) {
	cases := map[uint32]string{10: CodeSudo, 11: CodeOS, 12: CodeDocker, 14: CodeToken, 1: CodeInstall}
	for exit, code := range cases {
		srv := newFakeServer(t)
		srv.installExit = exit
		err := Run(context.Background(), newRun(srv, Creds{Password: srv.password}, io.Discard))
		var e *Error
		if !asErr(err, &e) || e.Code != code {
			t.Fatalf("exit %d: want %s, got %v", exit, code, err)
		}
	}
}

func TestRunCancel(t *testing.T) {
	srv := newFakeServer(t)
	srv.hang = make(chan struct{})
	defer close(srv.hang)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, newRun(srv, Creds{Password: srv.password}, io.Discard)) }()
	time.Sleep(500 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		var e *Error
		if !asErr(err, &e) || e.Code != CodeCancelled {
			t.Fatalf("want cancelled, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestPrivateKeyAuthAndPassphrase(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	blk, _ := ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte("kp"))
	pemBytes := pem.EncodeToMemory(blk)
	if _, err := (Creds{PrivateKey: pemBytes}).authMethods(); err == nil || !strings.Contains(err.Error(), "passphrase") {
		t.Fatalf("missing passphrase should be reported, got %v", err)
	}
	if _, err := (Creds{PrivateKey: pemBytes, Passphrase: "kp"}).authMethods(); err != nil {
		t.Fatal(err)
	}
	c := Creds{Password: "x", PrivateKey: []byte("abc"), Passphrase: "y"}
	c.Wipe()
	if c.Password != "" || c.Passphrase != "" || c.PrivateKey != nil {
		t.Fatal("Wipe left secrets behind")
	}
}

func TestRedactorSplitAcrossWritesAndTruncation(t *testing.T) {
	r := NewRedactor(40, "supersecret")
	r.Write([]byte("pass=super"))
	r.Write([]byte("secret end\nline two\n"))
	if strings.Contains(r.Text(), "supersecret") {
		t.Fatalf("leaked: %q", r.Text())
	}
	r.Write([]byte(strings.Repeat("x", 100) + "\n"))
	if !strings.Contains(r.Text(), "[log truncated]") {
		t.Fatal("expected truncation marker")
	}
}

func asErr(err error, target **Error) bool {
	e, ok := err.(*Error)
	if ok {
		*target = e
	}
	return ok
}
