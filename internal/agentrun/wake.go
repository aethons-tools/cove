package agentrun

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/aethons-tools/cove/internal/covemaster"
)

// wakeBox holds the reasons of Wakes not yet delivered to the agent. Wakes
// coalesce (one resume prompt answers all of them), so reasons are merged —
// never dropped — and drained by the delivery that answers them.
type wakeBox struct {
	mu      sync.Mutex
	pending []covemaster.WakeReason
	sig     chan struct{} // cap 1
}

func newWakeBox() *wakeBox { return &wakeBox{sig: make(chan struct{}, 1)} }

// post merges rs (dropping exact duplicates) and signals.
func (b *wakeBox) post(rs []covemaster.WakeReason) {
	b.mu.Lock()
	for _, r := range rs {
		if !slices.Contains(b.pending, r) {
			b.pending = append(b.pending, r)
		}
	}
	b.mu.Unlock()
	b.repost()
}

// repost signals without adding reasons (a coalesced wake handed to the
// post-exit wait).
func (b *wakeBox) repost() {
	select {
	case b.sig <- struct{}{}:
	default:
	}
}

func (b *wakeBox) signal() <-chan struct{} { return b.sig }

// take drains the pending reasons.
func (b *wakeBox) take() []covemaster.WakeReason {
	b.mu.Lock()
	defer b.mu.Unlock()
	rs := b.pending
	b.pending = nil
	return rs
}

// renderWake is the resume prompt for a delivery answering rs. base is the
// session kind's squawk prompt (resumeText): it is used when a squawk is among
// rs, or when rs is empty (a bare Wake from an older Jam). Every other reason
// gets a line of its own ahead of it; context-changed is skipped because the
// context notice is appended separately.
func renderWake(base string, rs []covemaster.WakeReason) string {
	var lines []string
	squawk := len(rs) == 0
	for _, r := range rs {
		switch r.Kind {
		case "squawk":
			squawk = true
		case "context-changed":
		case "alarm":
			l := fmt.Sprintf("Alarm %q fired: %s", r.Alarm, r.Note)
			if r.Detail != "" {
				l += "\nGate output:\n" + r.Detail
			}
			lines = append(lines, l)
		case "gate-failed":
			l := fmt.Sprintf("Alarm %q gate could not run: %s", r.Alarm, r.Detail)
			if r.Note != "" {
				l += "\nNote: " + r.Note
			}
			lines = append(lines, l)
		case "idle":
			lines = append(lines, "Idle timeout: no other wake arrived.")
		default:
			lines = append(lines, fmt.Sprintf("Woken (%s): %s", r.Kind, r.Detail))
		}
	}
	if squawk {
		lines = append(lines, base)
	} else {
		lines = append(lines, "Continue.")
	}
	return strings.Join(lines, "\n")
}
