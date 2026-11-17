package main

import (
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/sessionevents"
	"github.com/aethons-tools/cove/internal/wakeon"
)

// Jam compacts a session that ignored compact-due as the session view's
// Compact does: resuming a paused studio first.
func TestKeepWarmCompactorUsesSessionCompact(t *testing.T) {
	cmd, res := &fakeCommander{}, &stubResumer{}
	c := keepWarmCompactor{ops: sessionOps{res: res, cmd: cmd, connectWait: time.Second, connectPoll: time.Millisecond}}
	if err := c.Compact(t.Context(), jam.Instance{ActorID: "w1", Phase: jam.PhaseIdled}); err != nil {
		t.Fatal(err)
	}
	if len(res.resumed) != 1 || len(cmd.sent) != 1 || cmd.sent[0] != "compact w1 "+compactDueBy+" "+jam.CompactInstructions {
		t.Fatalf("resumed %v, sent %v", res.resumed, cmd.sent)
	}
}

func TestContextSizesFromEvents(t *testing.T) {
	s := sessionevents.NewMemStore()
	for i, raw := range []string{
		`{"type":"assistant","parent_tool_use_id":null,"message":{"model":"m","usage":{"input_tokens":5,"cache_creation_input_tokens":1000,"cache_read_input_tokens":40000,"output_tokens":7}}}`,
		`{"type":"result","usage":{"input_tokens":99999}}`,
	} {
		ev := sessionevents.Event{ActorID: "w1", StreamID: "s", Seq: uint64(i + 1), Kind: sessionevents.KindEvent, Turn: 1,
			ObservedAt: time.Unix(int64(1000+i), 0), Raw: []byte(raw), Index: sessionevents.DeriveIndex([]byte(raw))}
		if err := s.Append(ev); err != nil {
			t.Fatal(err)
		}
	}
	got, err := contextSizes{totals: sessionevents.NewTotalsCache(s)}.ContextSize("w1")
	if err != nil || got != (wakeon.ContextSize{Tokens: 41005}) {
		t.Fatalf("ContextSize = %+v, %v; want the last call's input", got, err)
	}
}
