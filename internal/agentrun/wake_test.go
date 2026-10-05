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
	b.post(nil) // a bare Wake from an older Jam still signals
	select {
	case <-b.signal():
	default:
		t.Fatal("no signal")
	}
	got := b.take()
	if len(got) != 2 || got[0].Kind != "squawk" || got[1].Alarm != "a" {
		t.Fatalf("take = %+v", got)
	}
	if len(b.take()) != 0 {
		t.Fatal("take must drain")
	}
}
