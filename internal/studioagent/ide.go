package studioagent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/aethons-tools/cove/internal/jam/preview"
)

// IDEConfig tunes the built-in IDE; zero values mean the spec's defaults.
type IDEConfig struct {
	Bin       string        // "code-server" (looked up on PATH)
	Workspace string        // "/home/agent/workspace"
	Addr      string        // "127.0.0.1:13337"
	StartWait time.Duration // 15s
	Idle      time.Duration // 30m
	Grace     time.Duration // 10s (SIGTERM → SIGKILL)
}

func (c IDEConfig) withDefaults() IDEConfig {
	if c.Bin == "" {
		c.Bin = "code-server"
	}
	if c.Workspace == "" {
		c.Workspace = "/home/agent/workspace"
	}
	if c.Addr == "" {
		c.Addr = net.JoinHostPort("127.0.0.1", strconv.Itoa(preview.IDEPort))
	}
	if c.StartWait <= 0 {
		c.StartWait = 15 * time.Second
	}
	if c.Idle <= 0 {
		c.Idle = 30 * time.Minute
	}
	if c.Grace <= 0 {
		c.Grace = 10 * time.Second
	}
	return c
}

// ErrIDEUnavailable is Acquire's error when the code-server binary is missing.
var ErrIDEUnavailable = errors.New("ide unavailable: code-server not installed")

// ideRunner is what dialBack needs from the IDE (a test seam); *IDE is the
// real one.
type ideRunner interface {
	Acquire(ctx context.Context) error
	Release()
	Close()
}

// errIDEClosed is Acquire's error once Close has run.
var errIDEClosed = errors.New("ide: closed")

// IDE lazily runs the studio's built-in code-server: the first tunnel to its
// port starts it, and it stops once no tunnel has been open for Idle.
//
// State, all under mu: at most one of proc (running and accepting), start (a
// start in flight) and stopping (an idle stop in flight) is set. Callers that
// find a start or stop in flight wait for it outside the lock and look again,
// so concurrent Acquires share one process and a new process never races the
// old one for the port.
type IDE struct {
	cfg IDEConfig
	log *slog.Logger

	mu       sync.Mutex
	proc     *ideProc
	start    *ideStart
	stopping chan struct{} // the stopping process's done
	open     int
	closed   bool          // Close ran: nothing starts again
	closing  chan struct{} // closed by Close; aborts a start in flight
	idle     *time.Timer
	idleGen  uint64 // invalidates a timer that fired after it was stopped
}

// ideProc is one code-server process; done closes once it is reaped.
type ideProc struct {
	cmd    *exec.Cmd
	out    *logLines
	cancel context.CancelFunc // SIGTERM to the group, SIGKILL after Grace
	done   chan struct{}
	exited bool // under IDE.mu: reaped; never publish it as running
}

// ideStart is a start in flight; err is set before done closes.
type ideStart struct {
	done chan struct{}
	err  error
}

// NewIDE returns a stopped IDE; nothing runs until the first Acquire.
func NewIDE(cfg IDEConfig, log *slog.Logger) *IDE {
	return &IDE{cfg: cfg.withDefaults(), log: newLogger(log), closing: make(chan struct{})}
}

// Acquire ensures code-server is running and accepting on Addr (starting it
// if needed, waiting up to StartWait) and counts one open tunnel; the caller
// must Release when the tunnel ends. ErrIDEUnavailable when Bin is missing.
func (i *IDE) Acquire(ctx context.Context) error {
	for {
		i.mu.Lock()
		if i.closed {
			i.mu.Unlock()
			return errIDEClosed
		}
		if i.proc != nil {
			i.open++
			i.stopIdleLocked()
			i.mu.Unlock()
			return nil
		}
		var wait <-chan struct{}
		st := i.start
		switch {
		case st != nil:
			wait = st.done
		case i.stopping != nil:
			wait = i.stopping
		default:
			st = &ideStart{done: make(chan struct{})}
			i.start = st
			wait = st.done
			// The start must not die with this caller's ctx: others may be
			// waiting on it, and the process outlives every tunnel.
			go i.run(st)
		}
		i.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return ctx.Err()
		}
		if st != nil && st.err != nil {
			return st.err
		}
		// Started (or the old process is gone): look again.
	}
}

// Release ends one Acquire. When none remain, the idle clock starts.
func (i *IDE) Release() {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.open == 0 {
		return // unbalanced; never go negative
	}
	i.open--
	if i.open == 0 && i.proc != nil {
		i.armIdleLocked()
	}
}

func (i *IDE) armIdleLocked() {
	i.stopIdleLocked()
	gen := i.idleGen
	i.idle = time.AfterFunc(i.cfg.Idle, func() { i.onIdle(gen) })
}

func (i *IDE) stopIdleLocked() {
	if i.idle != nil {
		i.idle.Stop()
		i.idle = nil
	}
	i.idleGen++
}

func (i *IDE) onIdle(gen uint64) {
	i.mu.Lock()
	if gen != i.idleGen || i.open != 0 || i.proc == nil {
		i.mu.Unlock()
		return
	}
	i.idle = nil
	p := i.stopLocked()
	i.mu.Unlock()
	i.log.Info("ide stopped", "reason", "idle", "pid", p.cmd.Process.Pid)
}

// stopLocked moves the running process to stopping and signals it.
func (i *IDE) stopLocked() *ideProc {
	p := i.proc
	i.proc = nil
	i.stopping = p.done
	p.cancel()
	return p
}

// run starts code-server and waits for it to accept, then publishes the
// result to st's waiters.
func (i *IDE) run(st *ideStart) {
	p, err := i.launch()
	i.mu.Lock()
	if err == nil && p.exited { // died between accepting and now
		err = fmt.Errorf("ide: code-server exited right after starting")
	}
	if err == nil && i.closed { // Close is waiting on this start: stop it
		i.stopping = p.done
		p.cancel()
		err = errIDEClosed
	}
	st.err = err
	i.start = nil
	if err == nil {
		i.proc = p
		if i.open == 0 {
			// Arm the idle clock; waiters that still want it cancel it on
			// their next pass.
			i.armIdleLocked()
		}
	}
	i.mu.Unlock()
	close(st.done)
}

func (i *IDE) launch() (*ideProc, error) {
	bin, err := exec.LookPath(i.cfg.Bin)
	if err != nil {
		return nil, fmt.Errorf("%w (%v)", ErrIDEUnavailable, err)
	}
	pctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(pctx, bin, "--bind-addr", i.cfg.Addr, "--auth", "none",
		"--disable-telemetry", "--disable-update-check", i.cfg.Workspace)
	cmd.Env = childEnv(os.Environ())
	inOwnProcessGroup(cmd)
	cmd.WaitDelay = i.cfg.Grace
	out := &logLines{log: i.log}
	cmd.Stdout, cmd.Stderr = out, out // one writer: one pipe, one copier
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("ide: start %s: %w", bin, err)
	}
	p := &ideProc{cmd: cmd, out: out, cancel: cancel, done: make(chan struct{})}
	go i.reap(p)
	i.log.Info("ide started", "pid", cmd.Process.Pid, "addr", i.cfg.Addr)

	deadline := time.NewTimer(i.cfg.StartWait)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if c, err := net.DialTimeout("tcp", i.cfg.Addr, 200*time.Millisecond); err == nil {
			_ = c.Close()
			return p, nil
		}
		select {
		case <-p.done:
			return nil, fmt.Errorf("ide: code-server exited before accepting on %s", i.cfg.Addr)
		case <-deadline.C:
			p.cancel()
			<-p.done
			return nil, fmt.Errorf("ide: code-server not accepting on %s after %s", i.cfg.Addr, i.cfg.StartWait)
		case <-i.closing:
			p.cancel()
			<-p.done
			return nil, errIDEClosed
		case <-tick.C:
		}
	}
}

// reap waits for the process, kills any of its group left behind, and clears
// whatever state still points at it.
func (i *IDE) reap(p *ideProc) {
	err := p.cmd.Wait() // returns after the output copier has drained
	p.out.Close()
	_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL) // stragglers; ESRCH when none
	p.cancel()
	i.mu.Lock()
	p.exited = true
	if i.proc == p { // exited on its own
		i.proc = nil
		i.stopIdleLocked()
		i.log.Warn("ide exited", "pid", p.cmd.Process.Pid, "err", fmt.Sprint(err))
	}
	if i.stopping == p.done {
		i.stopping = nil
	}
	i.mu.Unlock()
	close(p.done)
}

// secretEnvPrefixes name cove-master's own credentials (AT_JAM_IDENTITY_TOKEN,
// AT_JAM_LAUNCH_SECRET, AT_HARBOR_* and the connector settings); an IDE
// terminal must not inherit them.
var secretEnvPrefixes = []string{"AT_JAM_", "AT_HARBOR_"}

// childEnv is env minus every variable named with a secretEnvPrefixes prefix.
func childEnv(env []string) []string {
	out := make([]string, 0, len(env))
next:
	for _, kv := range env {
		for _, p := range secretEnvPrefixes {
			if strings.HasPrefix(kv, p) {
				continue next
			}
		}
		out = append(out, kv)
	}
	return out
}

// inOwnProcessGroup puts code-server in its own process group and makes
// cancellation SIGTERM the whole group (cmd.WaitDelay then escalates).
func inOwnProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
}

// logLines writes code-server's output to the log at debug, one record per
// line. Only exec's single copier goroutine calls Write.
type logLines struct {
	log *slog.Logger
	buf []byte
}

func (w *logLines) Write(b []byte) (int, error) {
	w.buf = append(w.buf, b...)
	for {
		n := bytes.IndexByte(w.buf, '\n')
		if n < 0 {
			break
		}
		w.log.Debug("ide output", "line", string(w.buf[:n]))
		w.buf = w.buf[n+1:]
	}
	if len(w.buf) > 4096 { // an unterminated flood: flush rather than grow
		w.log.Debug("ide output", "line", string(w.buf))
		w.buf = nil
	}
	return len(b), nil
}

// Close logs any buffered unterminated tail. Call it once the copier is done
// (after cmd.Wait); safe to call twice.
func (w *logLines) Close() {
	if len(w.buf) > 0 {
		w.log.Debug("ide output", "line", string(w.buf))
		w.buf = nil
	}
}

// Close stops code-server (SIGTERM to its group, SIGKILL after Grace),
// whatever is open, and returns once it has been reaped — including one a
// start in flight produces. Nothing starts afterwards. Safe to call twice.
func (i *IDE) Close() {
	i.mu.Lock()
	if !i.closed {
		i.closed = true
		close(i.closing)
	}
	for {
		if i.proc != nil {
			i.stopIdleLocked()
			p := i.stopLocked()
			i.log.Info("ide stopped", "reason", "shutdown", "pid", p.cmd.Process.Pid)
		}
		var wait <-chan struct{}
		switch {
		case i.start != nil:
			wait = i.start.done
		case i.stopping != nil:
			wait = i.stopping
		default:
			i.mu.Unlock()
			return
		}
		i.mu.Unlock()
		<-wait
		i.mu.Lock()
	}
}

// pid is the running process's pid, or 0 (tests).
func (i *IDE) pid() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.proc == nil {
		return 0
	}
	return i.proc.cmd.Process.Pid
}

// busy reports a start or stop in flight (tests).
func (i *IDE) busy() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.start != nil || i.stopping != nil
}

// stopNow stops a running process at once, whatever is open (tests).
func (i *IDE) stopNow() {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.proc != nil {
		i.stopIdleLocked()
		i.stopLocked()
	}
}
