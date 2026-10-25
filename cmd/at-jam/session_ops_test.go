package main

import (
	"context"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

// fakeCommander connects after connectAfter Connected polls and records the
// commands it is sent.
type fakeCommander struct {
	connectAfter, polls int
	sent                []string
}

func (f *fakeCommander) live() bool { return f.polls >= f.connectAfter }

func (f *fakeCommander) Interrupt(id, by string) bool {
	f.sent = append(f.sent, "interrupt "+id+" "+by)
	return f.live()
}

func (f *fakeCommander) Compact(id, by, instructions string) bool {
	f.sent = append(f.sent, "compact "+id+" "+by+" "+instructions)
	return f.live()
}

func (f *fakeCommander) Clear(id, by string) bool {
	f.sent = append(f.sent, "clear "+id+" "+by)
	return f.live()
}

func (f *fakeCommander) Connected(string) bool {
	f.polls++
	return f.live()
}

type stubResumer struct{ resumed []string }

func (s *stubResumer) Resume(_ context.Context, id string) error {
	s.resumed = append(s.resumed, id)
	return nil
}

func TestSessionOpsCommandResumesIdled(t *testing.T) {
	cmd, res := &fakeCommander{connectAfter: 2}, &stubResumer{}
	o := sessionOps{res: res, cmd: cmd, connectWait: time.Second, connectPoll: time.Millisecond}
	if err := o.Command(t.Context(), jam.Instance{ActorID: "w1", Phase: jam.PhaseIdled}, jam.CmdClear, "Bob"); err != nil {
		t.Fatalf("Command: %v", err)
	}
	if len(res.resumed) != 1 || res.resumed[0] != "w1" {
		t.Errorf("resumed %v, want [w1]", res.resumed)
	}
	if len(cmd.sent) != 1 || cmd.sent[0] != "clear w1 Bob" {
		t.Errorf("sent %v, want [clear w1 Bob]", cmd.sent)
	}
}

func TestSessionOpsCommandNeverConnects(t *testing.T) {
	cmd, res := &fakeCommander{connectAfter: 1 << 30}, &stubResumer{}
	o := sessionOps{res: res, cmd: cmd, connectWait: 50 * time.Millisecond, connectPoll: time.Millisecond}
	if err := o.Command(t.Context(), jam.Instance{ActorID: "w1", Phase: jam.PhaseIdled}, jam.CmdClear, "Bob"); err == nil {
		t.Fatal("Command succeeded with no stream")
	}
	if len(cmd.sent) != 0 {
		t.Errorf("sent %v to a session that never connected", cmd.sent)
	}
}

func TestSessionOpsCommandLive(t *testing.T) {
	tests := []struct {
		name string
		cmd  jam.SessionCommand
		want string
	}{
		{"interrupt", jam.CmdInterrupt, "interrupt w1 Bob"},
		{"compact", jam.CmdCompact, "compact w1 Bob " + jam.CompactInstructions},
		{"clear", jam.CmdClear, "clear w1 Bob"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, res := &fakeCommander{}, &stubResumer{}
			o := sessionOps{res: res, cmd: cmd}
			if err := o.Command(t.Context(), jam.Instance{ActorID: "w1", Phase: jam.PhaseLive}, tt.cmd, "Bob"); err != nil {
				t.Fatalf("Command: %v", err)
			}
			if len(res.resumed) != 0 {
				t.Errorf("resumed a live session: %v", res.resumed)
			}
			if len(cmd.sent) != 1 || cmd.sent[0] != tt.want {
				t.Errorf("sent %v, want [%s]", cmd.sent, tt.want)
			}
		})
	}
	// No live stream: the sender's false is an error.
	o := sessionOps{res: &stubResumer{}, cmd: &fakeCommander{connectAfter: 1 << 30}}
	if err := o.Command(t.Context(), jam.Instance{ActorID: "w1", Phase: jam.PhaseLive}, jam.CmdInterrupt, "Bob"); err == nil {
		t.Error("Command succeeded with no stream")
	}
}

var _ sessionCommander = (*fakeCommander)(nil)
