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
	"github.com/aethons-tools/cove/internal/jam/collabwire"
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
	// DefaultWakeWait bounds how long a request on an idled studio waits for
	// it to resume and reconnect (no longer than a tunnel's dial-back).
	DefaultWakeWait = 10 * time.Second
	// idleMsg refuses a request on an idled studio when there is no wake path.
	idleMsg = "studio is idle; it resumes when it gets a message"
)

// Deps is what the routes need. Principal names the caller (and a display
// name for notices and audit), or fails with one of the Err* above.
type Deps struct {
	Store jam.Store
	CA    *sshca.CA // nil = feature off: every route 404s
	// Tunnels opens studio tunnels: Open for SSH, OpenPreview for the
	// preview proxy (capped below the studio's total, attach.TunnelBroker).
	Tunnels interface {
		Open(ctx context.Context, actorID string, port int32) (net.Conn, error)
		OpenPreview(ctx context.Context, actorID string, port int32) (net.Conn, error)
	}
	Notify    func(inst jam.Instance, body string)
	Now       func() time.Time
	Log       *slog.Logger
	Principal func(*http.Request) (collab.Principal, string, error)
	// Live is the open-SSH-connection registry; share both planes' one so an
	// unshare on either closes the user's connections. nil = a private one.
	Live *Live
	// Resume unpauses an idled studio (Supervisor.Resume) and Connected
	// reports whether a studio's attach stream is up. With both set, an
	// allowed cert or tunnel request on an idled studio resumes it and waits
	// up to WakeWait (DefaultWakeWait when <= 0) for it to reconnect; with
	// either nil it is refused 409 (idleMsg).
	Resume    func(ctx context.Context, actorID string) error
	Connected func(actorID string) bool
	WakeWait  time.Duration
	// Exposures lists a session's exposed previews (Supervisor.Exposures);
	// the preview proxy (Preview) serves only what it names.
	Exposures func(actorID string) []jam.Exposure
	// PreviewURL is the URL collaborators open for a session's named preview;
	// nil means previews are off (Preview 404s every host).
	PreviewURL func(actorID, name string) (string, error)
}

// withDefaults fills d's optional fields.
func withDefaults(d Deps) Deps {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if d.Notify == nil {
		d.Notify = func(jam.Instance, string) {}
	}
	if d.Live == nil {
		d.Live = NewLive()
	}
	if d.WakeWait <= 0 {
		d.WakeWait = DefaultWakeWait
	}
	return d
}

// Routes serves POST {prefix}/studios/{id}/ssh/cert, GET
// {prefix}/studios/{id}/ssh, and the sharing routes (share.go): POST
// {prefix}/studios/{id}/shares, DELETE {prefix}/studios/{id}/shares/{user}
// and GET {prefix}/studios/{id}/access. Mount it at {prefix}/studios/.
func Routes(prefix string, d Deps) http.Handler {
	if d.CA == nil {
		return http.NotFoundHandler()
	}
	h := &handler{d: withDefaults(d)}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+prefix+"/studios/{id}/ssh/cert", h.cert)
	mux.HandleFunc("GET "+prefix+"/studios/{id}/ssh", h.ssh)
	mux.HandleFunc("POST "+prefix+"/studios/{id}/shares", h.share)
	mux.HandleFunc("DELETE "+prefix+"/studios/{id}/shares/{user}", h.unshare)
	mux.HandleFunc("GET "+prefix+"/studios/{id}/access", h.access)
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

// resolve names the caller and finds the studio, writing the refusal and
// returning false when either fails.
func (h *handler) resolve(w http.ResponseWriter, r *http.Request) (collab.Principal, string, jam.Instance, bool) {
	p, name, err := h.d.Principal(r)
	switch {
	case err == nil:
	case errors.Is(err, ErrNoCredentials):
		http.Error(w, "sign in required", http.StatusUnauthorized)
		return p, "", jam.Instance{}, false
	case errors.Is(err, ErrInvalidToken):
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		http.Error(w, "invalid or expired token; sign in: at-jam login --participant", http.StatusUnauthorized)
		return p, "", jam.Instance{}, false
	case errors.Is(err, ErrUnbound):
		http.Error(w, ErrUnbound.Error(), http.StatusForbidden)
		return p, "", jam.Instance{}, false
	default:
		h.d.Log.Error("collab: resolving caller failed", "err", err.Error())
		http.Error(w, "could not resolve caller", http.StatusInternalServerError)
		return p, "", jam.Instance{}, false
	}
	inst, ok := h.d.Store.GetInstance(r.PathValue("id"))
	if !ok {
		http.Error(w, collabwire.NoSuchStudio, http.StatusNotFound)
		return p, "", jam.Instance{}, false
	}
	return p, name, inst, true
}

// authorize resolves the caller and the studio and checks right r on a live
// studio, writing the refusal and returning false when it fails.
func (h *handler) authorize(w http.ResponseWriter, r *http.Request, right collab.Right) (access, bool) {
	p, name, inst, ok := h.resolve(w, r)
	if !ok {
		return access{}, false
	}
	return h.authorizeInst(w, r, p, name, inst, right)
}

// authorizeInst checks that the named caller p may use right on inst, waking
// an idled studio for an allowed caller, and that the studio is live; it
// writes the refusal (403, or the wake's 409/503/504, or 409 not running) and
// returns false when it fails.
func (h *handler) authorizeInst(w http.ResponseWriter, r *http.Request, p collab.Principal, name string, inst jam.Instance, right collab.Right) (access, bool) {
	return h.authorizeInstWith(w, r, p, name, inst, right, http.Error)
}

// authorizeInstWith is authorizeInst answering its refusal (403) through
// refuse — the preview proxy's framed error page.
func (h *handler) authorizeInstWith(w http.ResponseWriter, r *http.Request, p collab.Principal, name string, inst jam.Instance, right collab.Right, refuse func(http.ResponseWriter, string, int)) (access, bool) {
	reason, ok := collab.Decide(h.d.Store, h.d.Now(), p, inst, right)
	if !ok {
		refuse(w, "you may not use "+string(right)+" on this studio", http.StatusForbidden)
		return access{}, false
	}
	// Only a live studio takes certs or tunnels: the supervisor closes a
	// studio's tunnels before pausing or tearing it down, and none may open
	// after. An idled one is woken for an allowed caller when it can be.
	if inst.Phase == jam.PhaseIdled {
		if inst, ok = h.wake(w, r, inst, p); !ok {
			return access{}, false
		}
	}
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

// wake resumes an idled studio and waits, up to WakeWait, for its attach
// stream to reconnect, returning the studio as it then is; it writes the
// refusal and returns false when there is no wake path or the studio does not
// come back. A failed Resume is fine when the studio is live anyway (wake-on
// or another request resumed it first).
func (h *handler) wake(w http.ResponseWriter, r *http.Request, inst jam.Instance, p collab.Principal) (jam.Instance, bool) {
	if h.d.Resume == nil || h.d.Connected == nil {
		http.Error(w, idleMsg, http.StatusConflict)
		return inst, false
	}
	who := access{p: p}.auditUser()
	// A dropped request does not cut a resume short.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), h.d.WakeWait)
	defer cancel()
	if err := h.d.Resume(ctx, inst.ActorID); err != nil {
		if cur, ok := h.d.Store.GetInstance(inst.ActorID); !ok || cur.Phase != jam.PhaseLive {
			h.d.Log.Warn("collab: resuming idled studio failed", "user", who, "session", inst.ActorID, "err", err.Error())
			http.Error(w, "could not resume the idle studio; try again", http.StatusServiceUnavailable)
			return inst, false
		}
	} else {
		h.d.Log.Info("collab: resumed idled studio", "user", who, "session", inst.ActorID)
	}
	t := time.NewTicker(50 * time.Millisecond)
	defer t.Stop()
	for !h.d.Connected(inst.ActorID) {
		select {
		case <-ctx.Done():
			http.Error(w, "studio resumed but did not reconnect in time; try again", http.StatusGatewayTimeout)
			return inst, false
		case <-t.C:
		}
	}
	cur, ok := h.d.Store.GetInstance(inst.ActorID)
	if !ok {
		http.Error(w, collabwire.NoSuchStudio, http.StatusNotFound)
		return inst, false
	}
	return cur, true
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
	h.d.Log.Info("ssh cert issued", "user", a.auditUser(), "name", a.name, "session", a.inst.ActorID, "right", "ssh", "reason", string(a.reason), "expires", exp.UTC().Format(time.RFC3339))
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
		case errors.Is(err, attach.ErrTunnelsClosed):
			code, msg = http.StatusConflict, err.Error()
		}
		h.d.Log.Warn("ssh tunnel refused", "user", a.auditUser(), "session", a.inst.ActorID, "right", "ssh", "port", SSHPort, "err", err.Error())
		http.Error(w, msg, code)
		return
	}
	conn, brw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		_ = studio.Close()
		http.Error(w, "connection cannot be upgraded", http.StatusInternalServerError)
		return
	}
	// The server's read/write timeouts would otherwise cut a long session.
	_ = conn.SetDeadline(time.Time{})
	_, _ = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: " + UpgradeProtocol + "\r\n\r\n")
	if err := brw.Flush(); err != nil {
		_ = conn.Close()
		_ = studio.Close()
		return
	}
	var once sync.Once
	closeBoth := func() { once.Do(func() { _ = conn.Close(); _ = studio.Close() }) }
	start := h.d.Now()
	remove := h.d.Live.add(&liveEntry{
		LiveConn: collabwire.LiveConn{Session: a.inst.ActorID, User: a.auditUser(), Name: a.name, Since: start},
		userID:   a.p.UserID, operator: a.p.Operator != "", right: collab.SSH, port: int(SSHPort), close: closeBoth,
	})
	defer remove()
	// An unshare removes the grant before it closes registered connections,
	// so re-checking once registered closes one that raced it.
	if _, ok := collab.Decide(h.d.Store, h.d.Now(), a.p, a.inst, collab.SSH); !ok {
		closeBoth()
		return
	}
	h.d.Notify(a.inst, a.name+" connected over SSH")
	h.d.Log.Info("ssh tunnel opened", "user", a.auditUser(), "name", a.name, "session", a.inst.ActorID, "right", "ssh", "reason", string(a.reason), "port", SSHPort)
	splice(brw.Reader, conn, studio, closeBoth)
	h.d.Log.Info("ssh tunnel closed", "user", a.auditUser(), "session", a.inst.ActorID, "right", "ssh", "port", SSHPort, "duration", h.d.Now().Sub(start).Round(time.Second).String())
}

// splice copies both ways until either side ends, then closes both (as
// closeBoth, which an unshare may also call). human's reads go through hr,
// which may hold bytes buffered past the request.
func splice(hr io.Reader, human, studio net.Conn, closeBoth func()) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(studio, hr); closeBoth() }()
	go func() { defer wg.Done(); _, _ = io.Copy(human, studio); closeBoth() }()
	wg.Wait()
}
