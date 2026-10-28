package agentrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// AgentSocketPath is where jam-studio-agent serves its agent socket: the local
// channel from its MCP server (a child of the agent CLI, which dies with each
// episode) to the long-lived Workload, for what only the Workload can do —
// the turn end's directives and kept processes. tmpfs, like the MCP config.
const AgentSocketPath = "/dev/shm/jam-agent.sock"

// maxEndReason bounds turn_end's end reason (as Jam's /end does).
const maxEndReason = 2000

// Directives are an agent's turn_end call: how the current turn ends, beyond
// the defaults (post the final text to the home channel, commit what was
// shown, wait, wake on a message). The last call in a turn wins; they apply
// at that turn's end only.
type Directives struct {
	// To is where the final text is posted (send's address grammar); "" =
	// the home channel.
	To string `json:"to,omitempty"`
	// Post false posts nothing this turn.
	Post *bool `json:"post,omitempty"`
	// Commit false leaves the messages shown this turn pending (deny): they
	// come back with the next wake.
	Commit *bool `json:"commit,omitempty"`
	// Next is "" or "wait" (the default), "compact" (then wait) or "end".
	Next   string `json:"next,omitempty"`
	Reason string `json:"reason,omitempty"` // Next "end": why
	// WakeOnMessage false: a new message doesn't wake the session until it
	// next runs (alarms, the idle timeout and kept processes still do).
	WakeOnMessage *bool   `json:"wake_on_message,omitempty"`
	WakeAt        *WakeAt `json:"wake_at,omitempty"`
	// IdleTimeout overrides the idle timeout for this turn end ("off" or a
	// duration).
	IdleTimeout string `json:"idle_timeout,omitempty"`
}

// WakeAt is a one-shot alarm set at the turn end.
type WakeAt struct {
	Name string `json:"name,omitempty"` // "" = "turn-end"
	At   string `json:"at"`             // RFC 3339
	Note string `json:"note,omitempty"`
}

// Validate checks what the Workload can check before the turn ends; Jam
// validates the rest (the address, the alarm, the duration) when it is applied.
func (d Directives) Validate() error {
	switch d.Next {
	case "", "wait", "compact":
	case "end":
		if r := strings.TrimSpace(d.Reason); r == "" || len(r) > maxEndReason {
			return fmt.Errorf("next end needs a reason of at most %d bytes", maxEndReason)
		}
	default:
		return fmt.Errorf("next must be wait, compact or end")
	}
	if d.WakeAt != nil {
		if _, err := time.Parse(time.RFC3339, d.WakeAt.At); err != nil {
			return fmt.Errorf("wake_at.at must be an RFC 3339 time")
		}
	}
	return nil
}

// agentSocketHandler is the socket's HTTP surface over w.
func (w *Workload) agentSocketHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /turn-end", func(rw http.ResponseWriter, r *http.Request) {
		var d Directives
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&d); err != nil {
			http.Error(rw, "invalid request", http.StatusBadRequest)
			return
		}
		if !w.structuredOn.Load() {
			http.Error(rw, "this session's Jam doesn't support turn_end yet: end your turn as usual and send your reply with send", http.StatusConflict)
			return
		}
		if err := d.Validate(); err != nil {
			http.Error(rw, err.Error(), http.StatusBadRequest)
			return
		}
		w.dirMu.Lock()
		w.directives = &d
		w.dirMu.Unlock()
		rw.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /keeps", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Name, Command, Workdir string }
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil {
			http.Error(rw, "invalid request", http.StatusBadRequest)
			return
		}
		p, err := w.keep.Start(in.Name, in.Command, in.Workdir)
		if err != nil {
			http.Error(rw, err.Error(), http.StatusBadRequest)
			return
		}
		writeSockJSON(rw, http.StatusCreated, p)
	})
	mux.HandleFunc("DELETE /keeps/{name}", func(rw http.ResponseWriter, r *http.Request) {
		if err := w.keep.Stop(r.PathValue("name")); err != nil {
			http.Error(rw, err.Error(), http.StatusNotFound)
			return
		}
		rw.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /keeps", func(rw http.ResponseWriter, _ *http.Request) {
		writeSockJSON(rw, http.StatusOK, w.keep.List())
	})
	return mux
}

// takeDirectives returns the turn's directives (zero = the defaults) and
// clears them.
func (w *Workload) takeDirectives() Directives {
	w.dirMu.Lock()
	defer w.dirMu.Unlock()
	d := w.directives
	w.directives = nil
	if d == nil {
		return Directives{}
	}
	return *d
}

// serveAgentSocket listens on path (a stale socket file is replaced), mode
// 0600, and serves h until stop. An error leaves the session without
// turn_end and keep (logged by the caller); the turn end still works.
func serveAgentSocket(path string, h http.Handler, log *slog.Logger) (stop func(), err error) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Warn("agentrun: agent socket stopped", "err", err.Error())
		}
	}()
	return func() {
		_ = srv.Close()
		_ = os.Remove(path)
	}, nil
}

// AgentClient is the MCP server's side of the agent socket.
type AgentClient struct{ hc *http.Client }

// ErrNoAgentSocket is a call on a studio whose agent serves no socket (an
// older jam-studio-agent, or the socket failed to start).
var ErrNoAgentSocket = errors.New("this studio's agent doesn't support it (upgrade the studio)")

// DialAgentSocket returns a client for the agent socket at path.
func DialAgentSocket(path string) *AgentClient {
	return &AgentClient{hc: &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{
		DisableKeepAlives: true, // each call dials: a restarted agent is found at once
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		},
	}}}
}

// do sends one request; a 4xx surfaces the agent's reason.
func (c *AgentClient) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://agent"+path, body)
	if err != nil {
		return err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		var op *net.OpError
		if errors.As(err, &op) {
			return ErrNoAgentSocket
		}
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		if msg := strings.TrimSpace(string(b)); msg != "" && resp.StatusCode/100 == 4 {
			return errors.New(msg)
		}
		return fmt.Errorf("agent socket: %s", resp.Status)
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

func writeSockJSON(rw http.ResponseWriter, status int, v any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	_ = json.NewEncoder(rw).Encode(v)
}

// KeepStart starts command as the kept process name (workdir "" = the
// agent's working directory).
func (c *AgentClient) KeepStart(ctx context.Context, name, command, workdir string) (KeptProcess, error) {
	var p KeptProcess
	err := c.do(ctx, http.MethodPost, "/keeps", struct{ Name, Command, Workdir string }{name, command, workdir}, &p)
	return p, err
}

// KeepStop stops the kept process name.
func (c *AgentClient) KeepStop(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, "/keeps/"+url.PathEscape(name), nil, nil)
}

// KeepList lists the kept processes.
func (c *AgentClient) KeepList(ctx context.Context) ([]KeptProcess, error) {
	var out []KeptProcess
	err := c.do(ctx, http.MethodGet, "/keeps", nil, &out)
	return out, err
}

// SetTurnEnd records the current turn's directives.
func (c *AgentClient) SetTurnEnd(ctx context.Context, d Directives) error {
	return c.do(ctx, http.MethodPut, "/turn-end", d, nil)
}
