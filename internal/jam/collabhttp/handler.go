// Package collabhttp serves studio collaboration over HTTP: SSH cert issuing
// and the SSH tunnel upgrade, the same routes on the operator plane (/admin)
// and the participant plane (/me). Every allowed request is audited — user,
// session, reason, port — never a key, token or cert.
package collabhttp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/attach"
	"github.com/aethons-tools/cove/internal/jam/collab"
	"github.com/aethons-tools/cove/internal/jam/sshca"
)

// The errors a Deps.Principal returns for a caller it cannot name.
var (
	ErrNoCredentials = errors.New("no credentials")                         // 401
	ErrInvalidToken  = errors.New("invalid or expired token")               // 401, WWW-Authenticate invalid_token
	ErrUnbound       = errors.New("this login is not linked to a Jam user") // 403
)

const (
	// SSHPort is the studio's loopback sshd port a tunnel reaches.
	SSHPort int32 = 2222
	// UpgradeProtocol is the Upgrade token of the SSH tunnel request.
	UpgradeProtocol = "jam-tunnel"

	maxBody     = 16 << 10
	openTimeout = 15 * time.Second
)

// Deps is what the routes need. Principal names the caller (and a display
// name for notices and audit), or fails with one of the Err* above.
type Deps struct {
	Store   jam.Store
	CA      *sshca.CA // nil = feature off: every route 404s
	Tunnels interface {
		Open(ctx context.Context, actorID string, port int32) (net.Conn, error)
	}
	Notify    func(inst jam.Instance, body string)
	Now       func() time.Time
	Log       *slog.Logger
	Principal func(*http.Request) (collab.Principal, string, error)
}

// Routes serves POST {prefix}/studios/{id}/ssh/cert and GET
// {prefix}/studios/{id}/ssh. Mount it at {prefix}/studios/.
func Routes(prefix string, d Deps) http.Handler {
	if d.CA == nil {
		return http.NotFoundHandler()
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if d.Notify == nil {
		d.Notify = func(jam.Instance, string) {}
	}
	h := &handler{d: d}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+prefix+"/studios/{id}/ssh/cert", h.cert)
	mux.HandleFunc("GET "+prefix+"/studios/{id}/ssh", h.ssh)
	return mux
}

type handler struct{ d Deps }

// access is an allowed caller on a live studio.
type access struct {
	p      collab.Principal
	name   string // display name
	inst   jam.Instance
	reason collab.Reason
}

// auditUser is how the caller appears in audit lines and the cert key-id.
func (a access) auditUser() string {
	if a.p.Operator != "" {
		return "op:" + a.p.Operator
	}
	return string(a.p.UserID)
}

// keyID is the cert's key-id, which sshd logs on every login.
func (a access) keyID() string { return "user=" + a.auditUser() + " studio=" + a.inst.ActorID }

// authorize resolves the caller and the studio and checks right r, writing
// the refusal and returning false when it fails.
func (h *handler) authorize(w http.ResponseWriter, r *http.Request, right collab.Right) (access, bool) {
	p, name, err := h.d.Principal(r)
	switch {
	case err == nil:
	case errors.Is(err, ErrNoCredentials):
		http.Error(w, "sign in required", http.StatusUnauthorized)
		return access{}, false
	case errors.Is(err, ErrInvalidToken):
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		http.Error(w, "invalid or expired token; sign in: at-jam login --participant", http.StatusUnauthorized)
		return access{}, false
	case errors.Is(err, ErrUnbound):
		http.Error(w, ErrUnbound.Error(), http.StatusForbidden)
		return access{}, false
	default:
		h.d.Log.Error("collab: resolving caller failed", "err", err.Error())
		http.Error(w, "could not resolve caller", http.StatusInternalServerError)
		return access{}, false
	}
	id := r.PathValue("id")
	inst, ok := h.d.Store.GetInstance(id)
	if !ok {
		http.Error(w, "no such studio", http.StatusNotFound)
		return access{}, false
	}
	reason, ok := collab.Decide(h.d.Store, h.d.Now(), p, inst, right)
	if !ok {
		http.Error(w, "you may not use "+string(right)+" on this studio", http.StatusForbidden)
		return access{}, false
	}
	// Only a live studio takes certs or tunnels: the supervisor closes a
	// studio's tunnels before pausing or tearing it down, and none may open
	// after.
	if inst.Phase != jam.PhaseLive {
		http.Error(w, "studio is not running", http.StatusConflict)
		return access{}, false
	}
	a := access{p: p, name: name, inst: inst, reason: reason}
	if a.name == "" {
		a.name = a.auditUser()
	}
	return a, true
}

func (h *handler) cert(w http.ResponseWriter, r *http.Request) {
	a, ok := h.authorize(w, r, collab.SSH)
	if !ok {
		return
	}
	var in struct {
		PublicKey string `json:"public_key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&in); err != nil {
		if _, tooBig := errors.AsType[*http.MaxBytesError](err); tooBig {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	cert, exp, err := h.d.CA.Issue([]byte(in.PublicKey), a.keyID(), h.d.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest) // sshca errors never echo key bytes
		return
	}
	h.d.Log.Info("ssh cert issued", "user", a.auditUser(), "name", a.name, "session", a.inst.ActorID, "reason", string(a.reason), "expires", exp.UTC().Format(time.RFC3339))
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Certificate string `json:"certificate"`
		ExpiresAt   string `json:"expires_at"`
	}{string(cert), exp.UTC().Format(time.RFC3339)})
}

// headerHasToken reports whether a comma-separated header lists tok.
func headerHasToken(r *http.Request, name, tok string) bool {
	for _, v := range r.Header.Values(name) {
		for t := range strings.SplitSeq(v, ",") {
			if strings.EqualFold(strings.TrimSpace(t), tok) {
				return true
			}
		}
	}
	return false
}

func (h *handler) ssh(w http.ResponseWriter, r *http.Request) {
	a, ok := h.authorize(w, r, collab.SSH)
	if !ok {
		return
	}
	if !headerHasToken(r, "Connection", "upgrade") || !headerHasToken(r, "Upgrade", UpgradeProtocol) {
		w.Header().Set("Upgrade", UpgradeProtocol)
		http.Error(w, "expected Connection: Upgrade, Upgrade: "+UpgradeProtocol, http.StatusUpgradeRequired)
		return
	}
	// Open before answering 101, so a failure is still an HTTP status. The
	// dial-back wait outlives a dropped request only up to openTimeout.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), openTimeout)
	studio, err := h.d.Tunnels.Open(ctx, a.inst.ActorID, SSHPort)
	cancel()
	if err != nil {
		code, msg := http.StatusBadGateway, "could not open the tunnel"
		switch {
		case errors.Is(err, attach.ErrStudioNotConnected):
			code, msg = http.StatusServiceUnavailable, "studio not connected"
		case errors.Is(err, attach.ErrDialBackTimeout):
			code, msg = http.StatusGatewayTimeout, err.Error()
		case errors.Is(err, context.DeadlineExceeded):
			code, msg = http.StatusGatewayTimeout, "studio did not open the tunnel in time"
		case errors.Is(err, attach.ErrTooManyTunnels):
			code, msg = http.StatusTooManyRequests, err.Error()
		}
		h.d.Log.Warn("ssh tunnel refused", "user", a.auditUser(), "session", a.inst.ActorID, "port", SSHPort, "err", err.Error())
		http.Error(w, msg, code)
		return
	}
	conn, brw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		_ = studio.Close()
		http.Error(w, "connection cannot be upgraded", http.StatusInternalServerError)
		return
	}
	_, _ = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: " + UpgradeProtocol + "\r\n\r\n")
	if err := brw.Flush(); err != nil {
		_ = conn.Close()
		_ = studio.Close()
		return
	}
	h.d.Notify(a.inst, a.name+" connected over SSH")
	start := h.d.Now()
	h.d.Log.Info("ssh tunnel opened", "user", a.auditUser(), "name", a.name, "session", a.inst.ActorID, "reason", string(a.reason), "port", SSHPort)
	splice(conn, brw.Reader, studio)
	h.d.Log.Info("ssh tunnel closed", "user", a.auditUser(), "session", a.inst.ActorID, "port", SSHPort, "duration", h.d.Now().Sub(start).Round(time.Second).String())
}

// splice copies both ways until either side ends, then closes both. human's
// reads go through hr, which may hold bytes buffered past the request.
func splice(human net.Conn, hr io.Reader, studio net.Conn) {
	var once sync.Once
	closeBoth := func() { once.Do(func() { _ = human.Close(); _ = studio.Close() }) }
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(studio, hr); closeBoth() }()
	go func() { defer wg.Done(); _, _ = io.Copy(human, studio); closeBoth() }()
	wg.Wait()
}
