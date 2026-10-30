package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"wgpanel-api/internal/nodeinstall"
	"wgpanel-api/internal/sshprov"
	"wgpanel-api/internal/store"
)

// SSH node provisioning (super_admin only).
//
// Flow, deliberately split so the admin sees the host key before any credential is
// sent anywhere:
//
//  1. POST .../ssh/probe  - connect without credentials, return the host key
//     fingerprint. The admin compares it with the server's own (ssh-keygen -lf).
//  2. POST .../ssh/jobs   - with the confirmed fingerprint and the credentials:
//     creates the node (or reuses one from a failed attempt), mints a join token and
//     starts a background job that uploads the embedded install-node.sh and runs it
//     non-interactively. The job refuses a server whose key differs from the
//     confirmed fingerprint.
//  3. GET  .../ssh/jobs/{id} - poll for phase, redacted log and result.
//  4. POST .../ssh/jobs/{id}/cancel.
//
// Credentials exist only in this process's memory for the life of the job and are
// wiped when it ends (success, failure, cancel or timeout); they are never written to
// the database, the audit log or logs. The persisted job log is redacted against the
// credentials and the join token, and capped.

const (
	sshJobTimeout   = 15 * time.Minute
	sshLogCap       = 64 << 10
	sshMaxRunning   = 3
	sshKeepInMemory = 10 * time.Minute
)

type sshJob struct {
	mu         sync.Mutex
	id         string
	nodeID     string
	host       string
	status     string
	phase      string
	errCode    string
	errMsg     string
	log        *sshprov.Redactor
	cancel     context.CancelFunc
	createdAt  time.Time
	finishedAt *time.Time
}

var sshJobs = struct {
	mu sync.Mutex
	m  map[string]*sshJob
}{m: map[string]*sshJob{}}

func (j *sshJob) setPhase(p string) {
	j.mu.Lock()
	j.phase = p
	j.mu.Unlock()
}

type sshJobResponse struct {
	JobID      string     `json:"job_id"`
	NodeID     string     `json:"node_id"`
	Host       string     `json:"host"`
	Status     string     `json:"status"` // running | succeeded | failed | cancelled
	Phase      string     `json:"phase"`
	Log        string     `json:"log"`
	Error      *sshJobErr `json:"error"`
	CreatedAt  string     `json:"created_at"`
	FinishedAt *string    `json:"finished_at"`
}

type sshJobErr struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (j *sshJob) snapshot() sshJobResponse {
	j.mu.Lock()
	defer j.mu.Unlock()
	r := sshJobResponse{JobID: j.id, NodeID: j.nodeID, Host: j.host, Status: j.status, Phase: j.phase,
		Log: j.log.Text(), CreatedAt: j.createdAt.Format(time.RFC3339)}
	if j.errCode != "" {
		r.Error = &sshJobErr{Code: j.errCode, Message: j.errMsg}
	}
	if j.finishedAt != nil {
		t := j.finishedAt.Format(time.RFC3339)
		r.FinishedAt = &t
	}
	return r
}

var (
	sshHostRe   = regexp.MustCompile(`^[A-Za-z0-9._:\-\[\]]{1,253}$`)
	sshUserRe   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.\-]{0,31}$`)
	nodeNameBad = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
)

type sshTargetRequest struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
}

func (r *sshTargetRequest) validate() (sshprov.Target, string) {
	if r.Port == 0 {
		r.Port = 22
	}
	switch {
	case !sshHostRe.MatchString(r.Host):
		return sshprov.Target{}, "host is required and must be a hostname or IP"
	case r.Port < 1 || r.Port > 65535:
		return sshprov.Target{}, "port must be between 1 and 65535"
	case !sshUserRe.MatchString(r.Username):
		return sshprov.Target{}, "username is required"
	}
	return sshprov.Target{Host: strings.Trim(r.Host, "[]"), Port: r.Port, User: r.Username}, ""
}

func (s *Server) handleSSHProbe(w http.ResponseWriter, r *http.Request) {
	var req sshTargetRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	t, msg := req.validate()
	if msg != "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", msg)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	fp, err := sshprov.Probe(ctx, t)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "ssh_connect_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"host": t.Host, "port": t.Port, "fingerprint": fp})
}

type sshStartRequest struct {
	sshTargetRequest
	Password    string `json:"password"`
	PrivateKey  string `json:"private_key"`
	Passphrase  string `json:"passphrase"`
	Fingerprint string `json:"fingerprint"`
	// NodeID reuses a node created by an earlier failed attempt (a fresh join token
	// is issued); otherwise Node describes the node to create.
	NodeID string `json:"node_id"`
	Node   *struct {
		Name             string `json:"name"`
		NodeGroup        string `json:"node_group"`
		Region           string `json:"region"`
		PublicEndpoint   string `json:"public_endpoint"`
		WGSubnet         string `json:"wg_subnet"`
		CapacityMaxPeers int    `json:"capacity_max_peers"`
	} `json:"node"`
}

func (s *Server) handleSSHStartJob(w http.ResponseWriter, r *http.Request) {
	var req sshStartRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	target, msg := req.validate()
	if msg == "" && !strings.HasPrefix(req.Fingerprint, "SHA256:") {
		msg = "fingerprint (confirmed host key, SHA256:...) is required"
	}
	if msg == "" && req.Password == "" && strings.TrimSpace(req.PrivateKey) == "" {
		msg = "password or private_key is required"
	}
	if msg == "" && req.NodeID == "" && (req.Node == nil || req.Node.Name == "" || req.Node.PublicEndpoint == "" || req.Node.WGSubnet == "") {
		msg = "node_id, or node with name, public_endpoint and wg_subnet, is required"
	}
	if msg != "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", msg)
		return
	}
	ctx := r.Context()
	panelAddr := s.controlPlaneAddr(ctx, r)
	if panelAddr == nil {
		writeJSONError(w, http.StatusConflict, "panel_address_unknown", "set the panel domain in Settings first: the node needs an address to reach the panel")
		return
	}

	sshJobs.mu.Lock()
	running := 0
	for _, j := range sshJobs.m {
		j.mu.Lock()
		if j.status == "running" {
			running++
		}
		j.mu.Unlock()
	}
	sshJobs.mu.Unlock()
	if running >= sshMaxRunning {
		writeJSONError(w, http.StatusTooManyRequests, "too_many_jobs", "too many provisioning jobs are running; wait for one to finish")
		return
	}

	// Node: create or reuse.
	var node store.Node
	var err error
	if req.NodeID != "" {
		node, err = s.Store.GetNode(ctx, req.NodeID)
		if errors.Is(err, store.ErrNodeNotFound) {
			writeJSONError(w, http.StatusNotFound, "node_not_found", "no node with that id")
			return
		}
		if err == nil && node.LastHeartbeatAt != nil {
			writeJSONError(w, http.StatusConflict, "node_already_registered", "this node is already installed")
			return
		}
	} else {
		n := req.Node
		endpoint, e1 := normalizePublicEndpoint(n.PublicEndpoint)
		subnet, e2 := normalizeWGSubnet(n.WGSubnet)
		if e1 != nil || e2 != nil {
			m := ""
			if e1 != nil {
				m = e1.Error()
			} else {
				m = e2.Error()
			}
			writeJSONError(w, http.StatusBadRequest, "invalid_request", m)
			return
		}
		if n.NodeGroup == "" {
			n.NodeGroup = "default"
		}
		if n.CapacityMaxPeers <= 0 {
			n.CapacityMaxPeers = 250
		}
		node, err = s.Store.CreateNode(ctx, n.Name, n.NodeGroup, n.Region, endpoint, subnet, n.CapacityMaxPeers, nil)
		if errors.Is(err, store.ErrNodeNameTaken) {
			writeJSONError(w, http.StatusConflict, "node_name_taken", "a node with this name already exists")
			return
		}
	}
	if err != nil {
		s.Logger.Error("ssh_provision_node_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not prepare the node")
		return
	}

	// Join token, same shape and TTL rule as handleGenerateJoinToken.
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not generate token")
		return
	}
	joinToken := hex.EncodeToString(buf)
	ttl, terr := strconv.Atoi(s.NodeJoinTokenTTLMinutes)
	if terr != nil || ttl < 30 {
		ttl = 30 // the job can run for a quarter of an hour; never expire mid-install
	}
	if err := s.Store.SetJoinToken(ctx, node.ID, joinToken, time.Now().Add(time.Duration(ttl)*time.Minute)); err != nil {
		s.Logger.Error("set_join_token_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not set join token")
		return
	}

	creds := sshprov.Creds{Password: req.Password, PrivateKey: []byte(req.PrivateKey), Passphrase: req.Passphrase}
	secrets := []string{req.Password, req.Passphrase, joinToken}
	for _, line := range strings.Split(req.PrivateKey, "\n") {
		secrets = append(secrets, line) // every key line, so a key echoed back is masked
	}
	logw := sshprov.NewRedactor(sshLogCap, secrets...)

	jobCtx, cancel := context.WithTimeout(context.Background(), sshJobTimeout)
	job := &sshJob{id: newJobID(), nodeID: node.ID, host: target.Host, status: "running", phase: "queued",
		log: logw, cancel: cancel, createdAt: time.Now()}
	admin, _ := adminFromContext(ctx)
	actor := ""
	if admin != nil {
		actor = admin.Username
	}
	nodeID := node.ID
	if err := s.Store.InsertSSHJob(ctx, store.SSHJob{ID: job.id, NodeID: &nodeID, Host: target.Host, Port: target.Port,
		Username: target.User, Fingerprint: req.Fingerprint, Status: "running", Phase: "queued", CreatedBy: actor}); err != nil {
		cancel()
		creds.Wipe()
		s.Logger.Error("insert_ssh_job_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not start the job")
		return
	}
	sshJobs.mu.Lock()
	sshJobs.m[job.id] = job
	sshJobs.mu.Unlock()
	s.auditOps(r, "node.ssh_provision_started", map[string]any{"node": node.Name, "host": target.Host, "port": target.Port, "user": target.User})

	opts := sshprov.Options{
		Target: target, Creds: creds, Fingerprint: req.Fingerprint, Script: nodeinstall.Script, Log: logw,
		Phase: job.setPhase,
		Env: map[string]string{
			"NODE_PANEL_ADDR": *panelAddr,
			"NODE_JOIN_TOKEN": joinToken,
			"NODE_NAME":       strings.Trim(nodeNameBad.ReplaceAllString(node.Name, "-"), "-"),
		},
	}
	go s.runSSHJob(jobCtx, cancel, job, opts, actor)

	writeJSON(w, http.StatusAccepted, job.snapshot())
}

// runSSHJob owns the credentials from here on and always wipes them.
func (s *Server) runSSHJob(ctx context.Context, cancel context.CancelFunc, job *sshJob, opts sshprov.Options, actor string) {
	defer cancel()
	defer opts.Creds.Wipe()
	err := sshprov.Run(ctx, opts)

	job.mu.Lock()
	now := time.Now()
	job.finishedAt = &now
	switch {
	case err == nil:
		job.status, job.phase = "succeeded", "done"
	default:
		job.status = "failed"
		var e *sshprov.Error
		if errors.As(err, &e) {
			job.errCode, job.errMsg = e.Code, e.Message
			if e.Code == sshprov.CodeCancelled {
				job.status = "cancelled"
			}
		} else {
			job.errCode, job.errMsg = sshprov.CodeInstall, "unexpected error"
		}
	}
	status, phase, code, msg := job.status, job.phase, job.errCode, job.errMsg
	job.mu.Unlock()

	// Persist with a fresh context: the job's own may already be cancelled/expired.
	dbctx, dbcancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer dbcancel()
	if err := s.Store.FinishSSHJob(dbctx, job.id, status, phase, code, msg, job.log.Text()); err != nil {
		s.Logger.Error("finish_ssh_job_failed", "error", err)
	}
	if err := s.Store.InsertAuditLog(dbctx, actor, "node.ssh_provision_finished", job.host,
		map[string]string{"status": status, "error_code": code}, ""); err != nil {
		s.Logger.Error("audit_log_failed", "error", err)
	}
	time.AfterFunc(sshKeepInMemory, func() {
		sshJobs.mu.Lock()
		delete(sshJobs.m, job.id)
		sshJobs.mu.Unlock()
	})
}

func (s *Server) handleSSHGetJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("jobId")
	sshJobs.mu.Lock()
	job := sshJobs.m[id]
	sshJobs.mu.Unlock()
	if job != nil {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, job.snapshot())
		return
	}
	j, err := s.Store.GetSSHJob(r.Context(), id)
	if errors.Is(err, store.ErrSSHJobNotFound) {
		writeJSONError(w, http.StatusNotFound, "job_not_found", "no such job")
		return
	}
	if err != nil {
		s.Logger.Error("get_ssh_job_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not load job")
		return
	}
	resp := sshJobResponse{JobID: j.ID, Host: j.Host, Status: j.Status, Phase: j.Phase, Log: j.Log, CreatedAt: j.CreatedAt.Format(time.RFC3339)}
	if j.NodeID != nil {
		resp.NodeID = *j.NodeID
	}
	if j.FinishedAt != nil {
		t := j.FinishedAt.Format(time.RFC3339)
		resp.FinishedAt = &t
	}
	if j.Status == "running" {
		// Persisted as running but not in memory: the process that owned it restarted.
		resp.Status = "failed"
		resp.Error = &sshJobErr{Code: "interrupted", Message: "the panel restarted while the job was running"}
	} else if j.ErrorCode != "" {
		resp.Error = &sshJobErr{Code: j.ErrorCode, Message: j.ErrorMessage}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleSSHCancelJob(w http.ResponseWriter, r *http.Request) {
	sshJobs.mu.Lock()
	job := sshJobs.m[r.PathValue("jobId")]
	sshJobs.mu.Unlock()
	if job == nil {
		writeJSONError(w, http.StatusNotFound, "job_not_found", "no running job with that id")
		return
	}
	job.cancel()
	s.auditOps(r, "node.ssh_provision_cancelled", map[string]string{"host": job.host})
	writeJSON(w, http.StatusAccepted, job.snapshot())
}

// registerOpsRoutes mounts the ops package's routes (called once from Routes()).
func (s *Server) registerOpsRoutes(mux *http.ServeMux) {
	// Maintenance is operator-or-above, like editing a node.
	mux.Handle("PUT /api/v1/nodes/{id}/maintenance", s.requireAdmin(s.requireRole("operator", http.HandlerFunc(s.handleSetNodeMaintenance))))

	// SSH provisioning hands the panel a server login, so it is super_admin-only like
	// the other trust-minting actions. The path is outside /api/v1/nodes/{id}/... so it
	// cannot collide with that wildcard family.
	base := "/api/v1/node-provisioning/ssh"
	mux.Handle("POST "+base+"/probe", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleSSHProbe))))
	mux.Handle("POST "+base+"/jobs", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleSSHStartJob))))
	mux.Handle("GET "+base+"/jobs/{jobId}", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleSSHGetJob))))
	mux.Handle("POST "+base+"/jobs/{jobId}/cancel", s.requireAdmin(s.requireRole("super_admin", http.HandlerFunc(s.handleSSHCancelJob))))
}

// auditOps writes an audit entry for the calling admin; failures are logged only.
func (s *Server) auditOps(r *http.Request, action string, detail any) {
	if admin, ok := adminFromContext(r.Context()); ok {
		if err := s.Store.InsertAuditLog(r.Context(), admin.Username, action, "ssh-provisioning", detail, clientIP(r)); err != nil {
			s.Logger.Error("audit_log_failed", "error", err)
		}
	}
}

// newJobID returns a random UUIDv4 (the column is a UUID; no extra dependency needed).
func newJobID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
