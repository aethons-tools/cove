package agentrun

import (
	"testing"

	"github.com/aethons-tools/cove/internal/covemaster"
)

func TestRenderWakeNoReasonsIsLegacyPrompt(t *testing.T) {
	if got := renderWake(standingResumePrompt, nil); got != standingResumePrompt {
		t.Fatalf("got %q", got)
	}
	if got := renderWake(residentResumePrompt, []covemaster.WakeReason{{Kind: "squawk"}}); got != residentResumePrompt {
		t.Fatalf("squawk-only got %q", got)
	}
}

func TestRenderWakeAlarmAndSquawk(t *testing.T) {
	got := renderWake(standingResumePrompt, []covemaster.WakeReason{
		{Kind: "alarm", Alarm: "pr-watch", Note: "check the PR", Detail: "CI red"},
		{Kind: "squawk"},
	})
	want := "Alarm \"pr-watch\" fired: check the PR\nGate output:\nCI red\n" + standingResumePrompt
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestRenderWakeWithoutSquawk(t *testing.T) {
	got := renderWake(standingResumePrompt, []covemaster.WakeReason{{Kind: "idle"}, {Kind: "mystery", Detail: "x"}})
	want := "Idle timeout: no other wake arrived.\nWoken (mystery): x\nContinue."
	if got != want {
		t.Fatalf("got %q", got)
	}
}

func TestWakeBoxMergesAndDedups(t *testing.T) {
	b := newWakeBox()
	b.post([]covemaster.WakeReason{{Kind: "squawk"}})
	b.post([]covemaster.WakeReason{{Kind: "squawk"}, {Kind: "alarm", Alarm: "a"}})
	b.post(nil) // a bare Wake from an older Jam: the legacy marker
	select {
	case <-b.signal():
	default:
		t.Fatal("no signal")
	}
	got := b.take()
	if len(got) != 3 || got[0].Kind != "squawk" || got[1].Alarm != "a" || got[2] != legacyWake {
		t.Fatalf("take = %+v", got)
	}
	if len(b.take()) != 0 {
		t.Fatal("take must drain")
	}
}

// A bare Wake (older Jam) is recorded as a legacy marker, so an empty take()
// always means "already answered", never "a bare wake".
func TestWakeBoxBareWakeIsLegacyMarker(t *testing.T) {
	b := newWakeBox()
	b.post(nil)
	rs := b.take()
	if len(rs) != 1 {
		t.Fatalf("bare wake take = %+v, want one legacy marker", rs)
	}
	if got := renderWake(residentResumePrompt, rs); got != residentResumePrompt {
		t.Fatalf("legacy marker renders %q", got)
	}
}

// restore puts the reasons of a delivered-but-unanswered prompt back.
func TestWakeBoxRestore(t *testing.T) {
	b := newWakeBox()
	b.post([]covemaster.WakeReason{{Kind: "alarm", Alarm: "nightly"}})
	b.take()
	b.restore()
	if rs := b.take(); len(rs) != 1 || rs[0].Alarm != "nightly" {
		t.Fatalf("after restore take = %+v", rs)
	}
	b.take() // empty take clears what is in flight
	b.restore()
	if rs := b.take(); len(rs) != 0 {
		t.Fatalf("restore after an empty take = %+v, want none", rs)
	}
}
