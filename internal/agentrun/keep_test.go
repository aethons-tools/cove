//go:build unix

package agentrun

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/aethons-tools/cove/internal/studioagent"
)

type exitRec struct {
	mu    sync.Mutex
	exits []string
}

func (r *exitRec) onExit(name string, status int, tail string) {
	r.mu.Lock()
	r.exits = append(r.exits, fmt.Sprintf("%s %d %s", name, status, tail))
	r.mu.Unlock()
}

func (r *exitRec) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.exits...)
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func TestKeeperStartStopList(t *testing.T) {
	rec := &exitRec{}
	k := newKeeper(t.TempDir(), t.TempDir(), nil, rec.onExit, nil)
	defer k.StopAll()
	p, err := k.Start("preview", "sleep 30", "")
	if err != nil || p.PID == 0 {
		t.Fatalf("Start = %+v, %v", p, err)
	}
	if _, err := k.Start("preview", "sleep 30", ""); err == nil {
		t.Fatal("duplicate name: want an error")
	}
	for _, bad := range []string{"", "Preview", "-x", "a b", strings.Repeat("a", 33)} {
		if _, err := k.Start(bad, "true", ""); err == nil {
			t.Errorf("name %q: want an error", bad)
		}
	}
	for i := 1; i < maxKept; i++ {
		if _, err := k.Start(fmt.Sprintf("p%d", i), "sleep 30", ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := k.Start("onetoomany", "sleep 30", ""); err == nil {
		t.Fatalf("more than %d kept: want an error", maxKept)
	}
	if l := k.List(); len(l) != maxKept || l[0].Name != "p1" {
		t.Fatalf("List = %+v (want sorted by name)", l)
	}
	if err := k.Stop("preview"); err != nil {
		t.Fatal(err)
	}
	if err := k.Stop("preview"); err == nil {
		t.Fatal("stopping a stopped process: want an error")
	}
	waitFor(t, func() bool { return !alive(p.PID) })
	if len(rec.list()) != 0 {
		t.Fatalf("a stop is not an exit: %v", rec.list())
	}
}

func TestKeeperExitReportsTail(t *testing.T) {
	rec := &exitRec{}
	dir := t.TempDir()
	k := newKeeper(dir, t.TempDir(), nil, rec.onExit, nil)
	defer k.StopAll()
	if _, err := k.Start("job", "echo starting; echo bye; exit 3", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(rec.list()) == 1 })
	if got := rec.list()[0]; got != "job 3 starting\nbye" {
		t.Fatalf("exit = %q", got)
	}
	if len(k.List()) != 0 {
		t.Fatal("an exited process stays listed")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "job.log")); !strings.Contains(string(b), "bye") {
		t.Fatalf("log = %q", b)
	}
}

func TestKeeperRestartCleanup(t *testing.T) {
	dir := t.TempDir()
	k := newKeeper(dir, t.TempDir(), nil, nil, nil)
	p, err := k.Start("preview", "sleep 30", "")
	if err != nil {
		t.Fatal(err)
	}
	// A jam-studio-agent restart: the old keeper is gone without stopping it.
	k2 := newKeeper(dir, t.TempDir(), nil, nil, nil)
	if names := k2.CleanupStale(); len(names) != 1 || names[0] != "preview" {
		t.Fatalf("CleanupStale = %v", names)
	}
	waitFor(t, func() bool { return !alive(p.PID) })
	k.mu.Lock()
	old := k.procs["preview"]
	k.mu.Unlock()
	if old != nil {
		<-old.done // the test's stand-in for the dead predecessor finishes its bookkeeping
	}
	var reg []KeptProcess
	b, _ := os.ReadFile(filepath.Join(dir, "registry.json"))
	if json.Unmarshal(b, &reg); len(reg) != 0 {
		t.Fatalf("registry after cleanup = %s", b)
	}
}

func TestKeptProcessOutlivesEpisodeAndWakes(t *testing.T) {
	s := newStreamSpawner()
	io := &fakeTurnIO{}
	sock := shortSock(t)
	keepDir := t.TempDir()
	w := streamWL(t, t.TempDir(), s, func(c *Config) {
		c.TurnIO, c.AgentSocket, c.KeepDir = io, sock, keepDir
		structuredContext(t, c)
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := runAsync(ctx, w, &recordHandle{})
	p := s.next(t)
	p.in.next(t)
	p.emit(lnInit)
	c := DialAgentSocket(sock)
	kp, err := c.KeepStart(context.Background(), "preview", "sleep 0.3; echo served; exit 0", "")
	if err != nil {
		t.Fatal(err)
	}
	p.emit(resultLine(""))
	p.in.waitClosed(t)
	p.exit <- nil // the episode ends; the kept process doesn't
	if !alive(kp.PID) {
		t.Fatal("kept process died with the episode")
	}
	// Its exit wakes the session locally: a new episode with the reason.
	p = s.next(t)
	got := p.in.next(t)
	if !strings.Contains(got, "Kept process `preview` exited (status 0); last lines:\nserved") {
		t.Fatalf("wake prompt = %q", got)
	}
	// Teardown stops whatever is still kept.
	kp2, err := c.KeepStart(context.Background(), "other", "sleep 30", "")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	<-done
	waitFor(t, func() bool { return !alive(kp2.PID) })
}

func TestWakeLineProcessExited(t *testing.T) {
	l, ok := wakeLine(studioagent.WakeReason{Kind: "process-exited", Note: "x", Detail: "status 3\nlast\nlines"})
	if !ok || l != "Kept process `x` exited (status 3); last lines:\nlast\nlines" {
		t.Fatalf("line = %q", l)
	}
}
