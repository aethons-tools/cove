package agentrun

import (
	"fmt"
	"strings"
)

// The inbox batch a wake delivers is capped at maxBatchEntries entries or
// maxBatchBytes of rendered text, whichever comes first; the rest wait for
// read (or the next wake).
const (
	maxBatchEntries = 20
	maxBatchBytes   = 32 << 10
)

// The batch's closing lines: nothing beyond it, or more waiting.
const (
	batchClosing = "Reply by ending your turn: your final message is posted to your channel."
	batchMore    = "More messages are waiting — use `read` to page them; only the ones above count as handled when you end your turn."
)

// renderBatch renders inbox entries (oldest first) for a wake prompt and
// returns the id of the last one shown in full, the commit point once the
// turn ends. Whole entries are shown until the byte cap; a first entry alone
// over it is cut (and not committed). more is whether entries wait beyond es.
func renderBatch(es []InboxEntry, more bool) (text, upTo string) {
	var b strings.Builder
	b.WriteString("New messages, oldest first:\n")
	for i, e := range es {
		if i == maxBatchEntries {
			more = true
			break
		}
		r := renderEntry(e)
		if b.Len()+len(r) > maxBatchBytes {
			if i > 0 {
				more = true
				break
			}
			// Cut, so not shown in full: it isn't committed for the agent, and
			// stays first in line until it reads and commits it itself.
			r = strings.ToValidUTF8(r[:max(0, maxBatchBytes-b.Len())], "") +
				fmt.Sprintf("… (cut — `read` id %s for the rest, then `commit` up to it once handled)\n", e.ID)
			b.WriteString("\n")
			b.WriteString(r)
			more = true
			break
		}
		b.WriteString("\n")
		b.WriteString(r)
		upTo = e.ID
	}
	b.WriteString("\n")
	if more {
		b.WriteString(batchMore)
	} else {
		b.WriteString(batchClosing)
	}
	return b.String(), upTo
}

// renderEntry is one entry: a header line, its body, and a line per part.
func renderEntry(e InboxEntry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "From %s in %s (%s) at %s — id %s\n", e.Author, e.Channel.Label, e.Channel.Kind, e.At, e.ID)
	b.WriteString(e.Body)
	b.WriteString("\n")
	for _, p := range e.Parts {
		if p.Ref != "" {
			fmt.Fprintf(&b, "[attachment %s ref %s]\n", p.Name, p.Ref)
		} else {
			fmt.Fprintf(&b, "[part %s]\n", p.Type)
		}
	}
	return b.String()
}
