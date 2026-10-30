// Package sshprov installs a WGPanel node on a remote server over SSH.
//
// Trust model: host keys use TOFU in two explicit steps. Probe connects without
// credentials just to learn the server's key fingerprint; a human compares it and
// confirms; Run then refuses to proceed unless the server presents exactly that
// fingerprint. Credentials live only in the Creds value the caller passes in - this
// package never writes them anywhere, and everything it logs goes through a Redactor
// that knows them.
package sshprov

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Error codes reported to the UI.
const (
	CodeConnect      = "connect_failed"
	CodeHostKey      = "host_key_mismatch"
	CodeAuth         = "auth_failed"
	CodeSudo         = "sudo_required"
	CodeOS           = "unsupported_os"
	CodeDocker       = "no_docker"
	CodeConfig       = "invalid_config"
	CodeToken        = "join_token_rejected"
	CodeInstall      = "install_failed"
	CodeTimeout      = "timeout"
	CodeCancelled    = "cancelled"
	dialTimeout      = 15 * time.Second
	installExitRoot  = 10
	installExitOS    = 11
	installExitDock  = 12
	installExitConf  = 13
	installExitToken = 14
)

// Error is a provisioning failure with a stable machine-readable code.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

func fail(code, format string, a ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, a...)}
}

// Target is the SSH endpoint.
type Target struct {
	Host string
	Port int
	User string
}

func (t Target) addr() string { return net.JoinHostPort(t.Host, strconv.Itoa(t.Port)) }

// Creds are the one-shot credentials. Zero them (Wipe) when the job ends.
type Creds struct {
	Password   string
	PrivateKey []byte
	Passphrase string
}

// Wipe overwrites the secrets in memory as far as Go allows.
func (c *Creds) Wipe() {
	for i := range c.PrivateKey {
		c.PrivateKey[i] = 0
	}
	c.PrivateKey, c.Password, c.Passphrase = nil, "", ""
}

func (c Creds) authMethods() ([]ssh.AuthMethod, error) {
	var out []ssh.AuthMethod
	if len(c.PrivateKey) > 0 {
		var signer ssh.Signer
		var err error
		if c.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(c.PrivateKey, []byte(c.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey(c.PrivateKey)
		}
		var missing *ssh.PassphraseMissingError
		switch {
		case errors.As(err, &missing):
			return nil, fail(CodeAuth, "the private key is encrypted: enter its passphrase")
		case err != nil:
			return nil, fail(CodeAuth, "the private key could not be read (wrong passphrase or unsupported format)")
		}
		out = append(out, ssh.PublicKeys(signer))
	}
	if c.Password != "" {
		pw := c.Password
		out = append(out, ssh.Password(pw), ssh.KeyboardInteractive(func(_, _ string, qs []string, _ []bool) ([]string, error) {
			ans := make([]string, len(qs))
			for i := range ans {
				ans[i] = pw
			}
			return ans, nil
		}))
	}
	if len(out) == 0 {
		return nil, fail(CodeAuth, "provide a password or a private key")
	}
	return out, nil
}

// Probe learns the host key fingerprint (SHA256:...) without authenticating.
func Probe(ctx context.Context, t Target) (string, error) {
	var fp string
	cfg := &ssh.ClientConfig{
		User: t.User,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			fp = ssh.FingerprintSHA256(key)
			return nil
		},
		Timeout: dialTimeout,
	}
	conn, err := dialContext(ctx, t, cfg)
	if conn != nil {
		conn.Close()
	}
	if fp != "" {
		return fp, nil // the handshake got as far as the key; auth failing is expected here
	}
	if err == nil {
		return "", fail(CodeConnect, "the server did not present a host key")
	}
	return "", fail(CodeConnect, "could not connect to %s: %v", t.addr(), err)
}

func dialContext(ctx context.Context, t Target, cfg *ssh.ClientConfig) (*ssh.Client, error) {
	d := net.Dialer{Timeout: dialTimeout}
	nc, err := d.DialContext(ctx, "tcp", t.addr())
	if err != nil {
		return nil, err
	}
	// Bound the handshake too; ssh.NewClientConn has no context of its own.
	stop := context.AfterFunc(ctx, func() { nc.Close() })
	defer stop()
	_ = nc.SetDeadline(time.Now().Add(dialTimeout))
	c, chans, reqs, err := ssh.NewClientConn(nc, t.addr(), cfg)
	if err != nil {
		nc.Close()
		return nil, err
	}
	_ = nc.SetDeadline(time.Time{})
	return ssh.NewClient(c, chans, reqs), nil
}

// Redactor is an io.Writer that masks secrets line by line and caps the total size.
// Lines are buffered until complete so a secret never straddles two Write calls
// unmasked.
type Redactor struct {
	mu      sync.Mutex
	secrets []string
	partial []byte
	buf     bytes.Buffer
	max     int
	dropped bool
}

// NewRedactor masks every secret (empty ones are ignored) and keeps at most max bytes.
func NewRedactor(max int, secrets ...string) *Redactor {
	r := &Redactor{max: max}
	r.AddSecret(secrets...)
	return r
}

func (r *Redactor) AddSecret(s ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, v := range s {
		v = strings.TrimSpace(v)
		if len(v) >= 3 {
			r.secrets = append(r.secrets, v)
		}
	}
}

func (r *Redactor) mask(line string) string {
	for _, s := range r.secrets {
		line = strings.ReplaceAll(line, s, "[redacted]")
	}
	return line
}

func (r *Redactor) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.partial = append(r.partial, p...)
	for {
		i := bytes.IndexByte(r.partial, '\n')
		if i < 0 {
			break
		}
		r.appendLine(string(r.partial[:i+1]))
		r.partial = r.partial[i+1:]
	}
	// A never-ending line must not grow without bound.
	if len(r.partial) > 8192 {
		r.appendLine(string(r.partial) + "\n")
		r.partial = nil
	}
	return len(p), nil
}

func (r *Redactor) appendLine(l string) {
	l = r.mask(l)
	if r.buf.Len()+len(l) > r.max {
		r.dropped = true
		return
	}
	r.buf.WriteString(l)
}

// Text returns the redacted log so far (flushing an unterminated last line).
func (r *Redactor) Text() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.buf.String()
	if len(r.partial) > 0 {
		s += r.mask(string(r.partial))
	}
	if r.dropped {
		s += "\n[log truncated]\n"
	}
	return s
}

// Options configure one Run.
type Options struct {
	Target      Target
	Creds       Creds
	Fingerprint string            // the confirmed SHA256:... host key fingerprint
	Script      []byte            // install-node.sh
	Env         map[string]string // NODE_PANEL_ADDR, NODE_JOIN_TOKEN, ...
	Log         io.Writer         // must redact; see Redactor
	Phase       func(string)
}

type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// envFile renders the variables as a sourceable file. Secrets travel in this file
// (written 0600 over stdin), never on a command line where `ps` could show them.
func envFile(env map[string]string) string {
	var b strings.Builder
	for k, v := range env {
		fmt.Fprintf(&b, "export %s=%s\n", k, shQuote(v))
	}
	return b.String()
}

// installCommand builds the remote command that sources the env file, deletes it, and
// execs the installer in non-interactive mode. sudo is a prefix like "sudo -n" or "".
func installCommand(sudo, dir string) string {
	inner := fmt.Sprintf(`set -a; . %s/env; set +a; rm -f %s/env; exec bash %s/install-node.sh --non-interactive`, dir, dir, dir)
	return strings.TrimSpace(sudo+" bash -c "+shQuote(inner)) + " 2>&1"
}

func (o *Options) phase(p string) {
	if o.Phase != nil {
		o.Phase(p)
	}
}

func (o *Options) logf(format string, a ...any) {
	if o.Log != nil {
		fmt.Fprintf(o.Log, format+"\n", a...)
	}
}

// remote runs one command, streaming combined output to out, feeding stdin.
func remote(client *ssh.Client, cmd, stdin string, out io.Writer) (int, error) {
	sess, err := client.NewSession()
	if err != nil {
		return -1, err
	}
	defer sess.Close()
	if stdin != "" {
		sess.Stdin = strings.NewReader(stdin)
	}
	if out != nil {
		// Stdout and Stderr are copied by separate goroutines. Handing both the same
		// bytes.Buffer races (io.Copy takes its ReadFrom path and can drop data), so
		// they share one mutex-guarded Write-only wrapper.
		w := &syncWriter{w: out}
		sess.Stdout, sess.Stderr = w, w
	}
	err = sess.Run(cmd)
	var ee *ssh.ExitError
	if errors.As(err, &ee) {
		return ee.ExitStatus(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}

func output(client *ssh.Client, cmd string) (string, int, error) {
	var b bytes.Buffer
	code, err := remote(client, cmd, "", &b)
	return strings.TrimSpace(b.String()), code, err
}

// Run connects, checks the server, uploads the installer and runs it. ctx cancellation
// (cancel button or timeout) tears the connection down, which kills the remote
// command's session.
func Run(ctx context.Context, o Options) (runErr error) {
	defer func() {
		if runErr != nil && ctx.Err() != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				runErr = fail(CodeTimeout, "the installation timed out")
			} else {
				runErr = fail(CodeCancelled, "cancelled")
			}
		}
	}()

	auth, err := o.Creds.authMethods()
	if err != nil {
		return err
	}
	o.phase("connecting")
	var mismatch bool
	cfg := &ssh.ClientConfig{
		User: o.Target.User,
		Auth: auth,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if ssh.FingerprintSHA256(key) != o.Fingerprint {
				mismatch = true
				return errors.New("host key does not match the confirmed fingerprint")
			}
			return nil
		},
		Timeout: dialTimeout,
	}
	client, err := dialContext(ctx, o.Target, cfg)
	if err != nil {
		switch {
		case mismatch:
			return fail(CodeHostKey, "the server's host key changed since you confirmed it - possible interception; aborted")
		case strings.Contains(err.Error(), "unable to authenticate"):
			return fail(CodeAuth, "authentication failed: wrong username, password or key")
		}
		return fail(CodeConnect, "could not connect to %s: %v", o.Target.addr(), err)
	}
	defer client.Close()
	// Closing the client is how a cancel interrupts a blocked remote command.
	stop := context.AfterFunc(ctx, func() { client.Close() })
	defer stop()
	o.logf("connected to %s as %s", o.Target.addr(), o.Target.User)

	o.phase("checking")
	sudo, sudoStdin, err := o.checkServer(client)
	if err != nil {
		return err
	}

	o.phase("uploading")
	dir, code, err := output(client, "umask 077; mktemp -d /tmp/wgpanel-install.XXXXXX")
	if err != nil || code != 0 || !strings.HasPrefix(dir, "/tmp/wgpanel-install.") {
		return fail(CodeInstall, "could not create a temporary directory on the server")
	}
	defer func() { _, _, _ = output(client, "rm -rf "+shQuote(dir)) }()
	if c, err := remote(client, "umask 077; cat > "+dir+"/install-node.sh", string(o.Script), nil); err != nil || c != 0 {
		return fail(CodeInstall, "could not upload the installer")
	}
	if c, err := remote(client, "umask 077; cat > "+dir+"/env", envFile(o.Env), nil); err != nil || c != 0 {
		return fail(CodeInstall, "could not upload the installer settings")
	}
	o.logf("uploaded installer (%d bytes)", len(o.Script))

	o.phase("installing")
	o.logf("running install-node.sh --non-interactive%s", map[bool]string{true: " (via sudo)", false: ""}[sudo != ""])
	exit, err := remote(client, installCommand(sudo, dir), sudoStdin, o.Log)
	if err != nil {
		return fail(CodeInstall, "the connection broke during installation: %v", err)
	}
	switch exit {
	case 0:
		return nil
	case installExitRoot:
		return fail(CodeSudo, "the installer needs root")
	case installExitOS:
		return fail(CodeOS, "unsupported operating system: only Debian/Ubuntu (apt) is supported")
	case installExitDock:
		return fail(CodeDocker, "Docker is missing and could not be installed")
	case installExitConf:
		return fail(CodeConfig, "the installer rejected its settings")
	case installExitToken:
		return fail(CodeToken, "the panel rejected the join token")
	}
	return fail(CodeInstall, "the installer failed (exit status %d); see the log", exit)
}

// checkServer works out how to get root and verifies the OS. It returns the sudo
// command prefix ("" when already root) and what to feed sudo on stdin.
func (o *Options) checkServer(client *ssh.Client) (sudo, stdin string, err error) {
	uid, _, err := output(client, "id -u")
	if err != nil {
		return "", "", fail(CodeInstall, "could not run commands on the server: %v", err)
	}
	if _, code, _ := output(client, "command -v apt-get >/dev/null 2>&1"); code != 0 {
		return "", "", fail(CodeOS, "unsupported operating system: only Debian/Ubuntu (apt) is supported")
	}
	if _, code, _ := output(client, "command -v docker >/dev/null 2>&1"); code == 0 {
		o.logf("Docker is already installed")
	} else {
		o.logf("Docker not found - the installer will install it")
	}
	if uid == "0" {
		o.logf("logged in as root")
		return "", "", nil
	}
	if _, code, _ := output(client, "sudo -n true"); code == 0 {
		o.logf("using passwordless sudo")
		return "sudo -n", "", nil
	}
	if o.Creds.Password != "" {
		var sink bytes.Buffer
		if code, _ := remote(client, "sudo -S -p '' true 2>&1", o.Creds.Password+"\n", &sink); code == 0 {
			o.logf("using sudo with the login password")
			return "sudo -S -p ''", o.Creds.Password + "\n", nil
		}
	}
	return "", "", fail(CodeSudo, "user %q is not root and cannot use sudo (needs root or sudo with the login password / passwordless sudo)", o.Target.User)
}
