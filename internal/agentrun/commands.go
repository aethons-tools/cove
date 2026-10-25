package agentrun

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/aethons-tools/cove/internal/covemaster"
)

// interruptAck is the acknowledge turn's text after an Interrupt; %s is who
// pressed it.
const interruptAck = "%s pressed **Interrupt** in the session header, which stopped your turn. Don't resume what you were doing. " +
	"In your channel, acknowledge the interrupt with `send`, ask what they want instead, then end your turn."

// interruptPending is appended to the acknowledge prompt when it also answers
// wakes coalesced into the interrupted turn.
const interruptPending = "New messages may be waiting. Read them, but don't act on any request until the person who interrupted you answers."

// cmdBox holds the session-header commands (Interrupt, Compact, Clear) not yet
// taken by Run. Posting never blocks (Control runs on the Attach receive loop):
// it records the command and signals. A command posted twice before it is
// taken collapses into one; the last post wins.
type cmdBox struct {
	mu sync.Mutex

	interrupt   bool
	interruptBy string

	compact      bool
	compactBy    string
	instructions string

	clear   bool
	clearBy string

	sig chan struct{} // cap 1
}

func newCmdBox() *cmdBox { return &cmdBox{sig: make(chan struct{}, 1)} }

func (b *cmdBox) postInterrupt(by string) {
	b.mu.Lock()
	b.interrupt, b.interruptBy = true, by
	b.mu.Unlock()
	b.repost()
}

func (b *cmdBox) takeInterrupt() (by string, ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	by, ok = b.interruptBy, b.interrupt
	b.interrupt, b.interruptBy = false, ""
	return by, ok
}

func (b *cmdBox) postCompact(by, instructions string) {
	b.mu.Lock()
	b.compact, b.compactBy, b.instructions = true, by, instructions
	b.mu.Unlock()
	b.repost()
}

func (b *cmdBox) takeCompact() (by, instructions string, ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	by, instructions, ok = b.compactBy, b.instructions, b.compact
	b.compact, b.compactBy, b.instructions = false, "", ""
	return by, instructions, ok
}

func (b *cmdBox) postClear(by string) {
	b.mu.Lock()
	b.clear, b.clearBy = true, by
	b.mu.Unlock()
	b.repost()
}

func (b *cmdBox) takeClear() (by string, ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	by, ok = b.clearBy, b.clear
	b.clear, b.clearBy = false, ""
	return by, ok
}

func (b *cmdBox) signal() <-chan struct{} { return b.sig }

// repost signals without posting (a command handed on to a later consumer).
func (b *cmdBox) repost() {
	select {
	case b.sig <- struct{}{}:
	default:
	}
}

// interruptPrompt is the acknowledge turn written after an interrupted turn
// ends. rs are the wakes coalesced into that turn: the prompt flags them as
// waiting and names each non-squawk reason as renderWake does, but never asks
// the agent to continue.
func interruptPrompt(by string, rs []covemaster.WakeReason) string {
	lines := []string{fmt.Sprintf(interruptAck, by)}
	if len(rs) > 0 {
		lines = append(lines, interruptPending)
		for _, r := range rs {
			if l, ok := wakeLine(r); ok {
				lines = append(lines, l)
			}
		}
	}
	return strings.Join(lines, "\n")
}

// emitCommand records a session-header command in the session event stream as
// a synthetic stdout-style line, so the Thinking tab and the admin timeline
// show who pressed it.
func (w *Workload) emitCommand(h covemaster.Handle, turn uint32, command, by string) {
	line, _ := json.Marshal(struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
		Command string `json:"command"`
		By      string `json:"by"`
	}{"cove", "command", command, by})
	h.Event(turn, line, 0)
}
