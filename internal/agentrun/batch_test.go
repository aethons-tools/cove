package agentrun

import (
	"fmt"
	"strings"
	"testing"
)

func entry(id, body string) InboxEntry {
	return InboxEntry{ID: id, Author: "you", Body: body, At: "2026-10-28T00:00:00Z",
		Channel: InboxParty{ID: "chn_1", Kind: "session", Label: "me"}}
}

func TestRenderBatchHeaderAndClosing(t *testing.T) {
	e := entry("m1", "hello there")
	e.Parts = []InboxPart{{Type: "image/png", Ref: "blb_1", Name: "shot.png"}}
	text, upTo := renderBatch([]InboxEntry{e}, false)
	for _, want := range []string{
		"From you in me (session) at 2026-10-28T00:00:00Z — id m1\nhello there",
		"[attachment shot.png ref blb_1]",
		batchClosing,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("batch missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, batchMore) || upTo != "m1" {
		t.Fatalf("upTo = %q, text:\n%s", upTo, text)
	}
}

func TestRenderBatchMore(t *testing.T) {
	text, upTo := renderBatch([]InboxEntry{entry("m1", "a"), entry("m2", "b")}, true)
	if !strings.Contains(text, batchMore) || strings.Contains(text, batchClosing) || upTo != "m2" {
		t.Fatalf("upTo %q, text:\n%s", upTo, text)
	}
}

func TestRenderBatchByteCap(t *testing.T) {
	// A first entry alone over the cap is cut, and isn't committed.
	big := entry("m1", strings.Repeat("x", 40<<10))
	text, upTo := renderBatch([]InboxEntry{big, entry("m2", "after")}, false)
	if upTo != "" || !strings.Contains(text, "… (cut — `read` id m1 for the rest, then `commit` up to it once handled)") || strings.Contains(text, "id m2") {
		t.Fatalf("upTo %q, len %d", upTo, len(text))
	}
	if !strings.Contains(text, batchMore) {
		t.Fatal("an entry left out by the cap: want the more line")
	}
	if len(text) > maxBatchBytes+1024 {
		t.Fatalf("batch is %d bytes, cap %d", len(text), maxBatchBytes)
	}
	// Later entries that don't fit are left for the next wake.
	var es []InboxEntry
	for i := range 10 {
		es = append(es, entry(fmt.Sprintf("m%d", i), strings.Repeat("y", 5<<10)))
	}
	text, upTo = renderBatch(es, false)
	if upTo == "m9" || !strings.Contains(text, batchMore) || strings.Contains(text, "(cut") {
		t.Fatalf("upTo %q; whole entries only after the first", upTo)
	}
}
