package agentrun

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aethons-tools/cove/internal/studioagent"
)

// Kept processes (keep_start) are started and owned by the Workload — not by
// the agent CLI, whose children die with each episode — so they outlive the
// agent's turns. Each runs `sh -c` in its own process group, its output in
// <dir>/<name>.log. One that exits on its own wakes the session; all of them
// stop with the session.
const (
	maxKept       = 8
	keepLogCap    = 1 << 20
	keepStopGrace = 10 * time.Second
	keepTailLines = 20
)

// defaultKeepDir is where kept processes' logs and registry live when Config
// leaves KeepDir empty: on the persisted /agent-data volume, so a restarted
// jam-studio-agent finds what its predecessor left running. A var so tests can
// point it away from the real /agent-data.
var defaultKeepDir = "/agent-data/keep"

var keepNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// errNoKept is a stop of a name that isn't kept.
var errNoKept = errors.New("no kept process by that name")

// KeptProcess is one kept process, as keep_list shows it.
type KeptProcess struct {
	Name    string    `json:"name"`
	Command string    `json:"command"`
	Workdir string    `json:"workdir,omitempty"`
	PID     int       `json:"pid"`
	Started time.Time `json:"started"`
	Log     string    `json:"log"`
}

type kept struct {
	KeptProcess
	stopping bool
	done     chan struct{}
}

type keeper struct {
	mu      sync.Mutex
	dir     string
	workdir string                                     // default working directory
	env     func() []string                            // the agent's env (nil or a nil result = inherit)
	onExit  func(name string, status int, tail string) // a process exited on its own
	log     *slog.Logger
	procs   map[string]*kept
}

func newKeeper(dir, workdir string, env func() []string, onExit func(string, int, string), log *slog.Logger) *keeper {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &keeper{dir: dir, workdir: workdir, env: env, onExit: onExit, log: log, procs: map[string]*kept{}}
}

// Start starts command as the kept process name.
func (k *keeper) Start(name, command, workdir string) (KeptProcess, error) {
	if !keepNameRE.MatchString(name) {
		return KeptProcess{}, fmt.Errorf("name must match %s", keepNameRE)
	}
	if strings.TrimSpace(command) == "" {
		return KeptProcess{}, errors.New("command is required")
	}
	if workdir == "" {
		workdir = k.workdir
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, ok := k.procs[name]; ok {
		return KeptProcess{}, fmt.Errorf("%q is already kept: keep_stop it first", name)
	}
	if len(k.procs) >= maxKept {
		return KeptProcess{}, fmt.Errorf("at most %d kept processes", maxKept)
	}
	if err := os.MkdirAll(k.dir, 0o700); err != nil {
		return KeptProcess{}, err
	}
	logPath := filepath.Join(k.dir, name+".log")
	out, err := newCappedLog(logPath, keepLogCap)
	if err != nil {
		return KeptProcess{}, err
	}
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = workdir
	if k.env != nil {
		cmd.Env = k.env()
	}
	cmd.Stdout, cmd.Stderr = out, out
	// A child still holding the output pipe (one it backgrounded) must not
	// keep Wait from reporting the exit.
	cmd.WaitDelay = 2 * time.Second
	keepGroup(cmd)
	if err := cmd.Start(); err != nil {
		out.Close()
		return KeptProcess{}, fmt.Errorf("start: %w", err)
	}
	p := &kept{KeptProcess: KeptProcess{Name: name, Command: command, Workdir: workdir, PID: cmd.Process.Pid, Started: time.Now().UTC(), Log: logPath},
		done: make(chan struct{})}
	k.procs[name] = p
	k.saveLocked()
	k.log.Info("agentrun: kept process started", "name", name, "pid", p.PID)
	go func() {
		err := cmd.Wait()
		out.Close()
		status := 0
		if ee := (*exec.ExitError)(nil); errors.As(err, &ee) {
			status = ee.ExitCode()
		}
		k.mu.Lock()
		stopped := p.stopping
		if k.procs[name] == p {
			delete(k.procs, name)
			k.saveLocked()
		}
		k.mu.Unlock()
		close(p.done) // after the registry no longer lists it
		k.log.Info("agentrun: kept process exited", "name", name, "status", status, "stopped", stopped)
		if !stopped && k.onExit != nil {
			k.onExit(name, status, tailLines(logPath, keepTailLines))
		}
	}()
	return p.KeptProcess, nil
}

// Stop stops the kept process name: SIGTERM to its group, SIGKILL after
// keepStopGrace. It returns once it has exited.
func (k *keeper) Stop(name string) error {
	k.mu.Lock()
	p, ok := k.procs[name]
	if ok {
		p.stopping = true
	}
	k.mu.Unlock()
	if !ok {
		return errNoKept
	}
	stopGroup(p.PID, p.done)
	return nil
}

func stopGroup(pgid int, done <-chan struct{}) {
	_ = signalGroup(pgid, true)
	select {
	case <-done:
	case <-time.After(keepStopGrace):
		_ = signalGroup(pgid, false)
		<-done
	}
}

// List returns the kept processes, by name.
func (k *keeper) List() []KeptProcess {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make([]KeptProcess, 0, len(k.procs))
	for _, p := range k.procs {
		out = append(out, p.KeptProcess)
	}
	slices.SortFunc(out, func(a, b KeptProcess) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// StopAll stops every kept process (the session is ending).
func (k *keeper) StopAll() {
	var wg sync.WaitGroup
	for _, p := range k.List() {
		wg.Add(1)
		go func() { defer wg.Done(); _ = k.Stop(p.Name) }()
	}
	wg.Wait()
}

// CleanupStale stops what a previous jam-studio-agent left kept (best
// effort, by process group) and returns their names; the registry is reset.
func (k *keeper) CleanupStale() []string {
	b, err := os.ReadFile(k.registry())
	if err != nil {
		return nil
	}
	var stale []KeptProcess
	if json.Unmarshal(b, &stale) != nil {
		return nil
	}
	var names []string
	for _, p := range stale {
		if p.PID > 0 {
			_ = signalGroup(p.PID, false)
		}
		names = append(names, p.Name)
	}
	k.mu.Lock()
	k.saveLocked()
	k.mu.Unlock()
	return names
}

func (k *keeper) registry() string { return filepath.Join(k.dir, "registry.json") }

// saveLocked persists the kept list. Caller holds mu.
func (k *keeper) saveLocked() {
	list := make([]KeptProcess, 0, len(k.procs))
	for _, p := range k.procs {
		list = append(list, p.KeptProcess)
	}
	b, _ := json.Marshal(list)
	err := os.MkdirAll(k.dir, 0o700)
	if err == nil {
		err = os.WriteFile(k.registry(), b, 0o600)
	}
	if err != nil {
		k.log.Warn("agentrun: kept-process registry not saved; a restart can't stop what is kept", "err", err.Error())
	}
}

// tailLines is the last n lines of the file at path ("" when unreadable).
func tailLines(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(b) > 64<<10 {
		b = b[len(b)-64<<10:]
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// cappedLog is an append-only log that rotates to <path>.1 past its cap.
type cappedLog struct {
	mu   sync.Mutex
	path string
	cap  int64
	f    *os.File
	size int64
}

func newCappedLog(path string, cap int64) (*cappedLog, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	var size int64
	if fi, err := f.Stat(); err == nil {
		size = fi.Size()
	}
	return &cappedLog{path: path, cap: cap, f: f, size: size}, nil
}

func (l *cappedLog) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.size+int64(len(b)) > l.cap && l.size > 0 {
		l.f.Close()
		_ = os.Rename(l.path, l.path+".1")
		f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return 0, err
		}
		l.f, l.size = f, 0
	}
	n, err := l.f.Write(b)
	l.size += int64(n)
	return n, err
}

func (l *cappedLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}

// keptLostNotice opens the first prompt after a restart that stopped kept
// processes; %s names them.
const keptLostNotice = "Kept processes stopped by a restart: %s — start them again with `keep_start` if they're still needed."

// agentEnv is the env kept processes run with: the agent's last spawn env
// (nil = inherit jam-studio-agent's).
func (w *Workload) agentEnv() []string {
	w.gateMu.Lock()
	defer w.gateMu.Unlock()
	return slices.Clone(w.gateEnv)
}

// keptExited wakes the session when a kept process exits on its own.
func (w *Workload) keptExited(name string, status int, tail string) {
	w.wake.post([]studioagent.WakeReason{{Kind: "process-exited", Note: name, Detail: fmt.Sprintf("status %d\n%s", status, tail)}})
}
