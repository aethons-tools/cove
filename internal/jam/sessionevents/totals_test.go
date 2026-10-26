package sessionevents

import (
	"fmt"
	"testing"
	"time"
)

func res(actor, stream string, seq uint64, turn uint32, cost float64, in, out int64, isErr bool, at time.Time) Event {
	return Event{ActorID: actor, StreamID: stream, Seq: seq, Kind: KindEvent, Turn: turn, ObservedAt: at,
		Index: Index{Type: "result", CostUSD: cost, InputTokens: in, OutputTokens: out, IsError: isErr}}
}

func tool(actor, stream string, seq uint64, turn uint32, at time.Time) Event {
	return Event{ActorID: actor, StreamID: stream, Seq: seq, Kind: KindEvent, Turn: turn, ObservedAt: at, Index: Index{Type: "assistant", ToolName: "Bash"}}
}

func TestTotalsFold(t *testing.T) {
	t0 := time.Unix(1000, 0)
	var tot Totals
	for _, ev := range []Event{
		tool("a", "s", 1, 1, t0),
		res("a", "s", 2, 1, 0.10, 100, 10, false, t0), // episode 1: cumulative 0.10
		res("a", "s", 3, 1, 0.25, 50, 5, false, t0),   // episode 1: now 0.25 (+0.15)
		tool("a", "s", 4, 2, t0),
		res("a", "s", 5, 2, 0.05, 10, 1, true, t0.Add(time.Minute)), // episode 2
	} {
		tot.Add(ev)
	}
	if tot.Turns != 3 || tot.Episodes != 2 || tot.ToolCalls != 2 || tot.Errors != 1 || tot.InputTokens != 160 || tot.OutputTokens != 16 {
		t.Fatalf("totals = %+v", tot)
	}
	if d := tot.CostUSD - 0.30; d > 1e-9 || d < -1e-9 {
		t.Fatalf("cost = %v, want 0.30", tot.CostUSD)
	}
	if d := tot.TurnCostUSD - 0.05; d > 1e-9 || d < -1e-9 {
		t.Fatalf("this turn's cost = %v, want 0.05 (the latest episode)", tot.TurnCostUSD)
	}
	if !tot.LastAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("last = %v", tot.LastAt)
	}
}

func TestTotalsOfAcrossStreamsCached(t *testing.T) {
	s := NewMemStore()
	t0 := time.Unix(1000, 0)
	for _, ev := range []Event{res("a", "s1", 1, 1, 0.1, 1, 1, false, t0), res("a", "s2", 1, 1, 0.2, 1, 1, false, t0), res("b", "s1", 1, 1, 9, 1, 1, false, t0)} {
		if err := s.Append(ev); err != nil {
			t.Fatal(err)
		}
	}
	c := NewTotalsCache(s)
	tot, err := c.Of("a")
	if err != nil || tot.Turns != 2 {
		t.Fatalf("Of = %+v, %v", tot, err)
	}
	if d := tot.CostUSD - 0.3; d > 1e-9 || d < -1e-9 {
		t.Fatalf("cost across streams = %v", tot.CostUSD)
	}
	if err := s.Append(res("a", "s2", 2, 1, 0.5, 1, 1, false, t0)); err != nil {
		t.Fatal(err)
	}
	tot, _ = c.Of("a")
	if tot.Turns != 3 {
		t.Fatalf("incremental refresh missed an event: %+v", tot)
	}
	if d := tot.CostUSD - 0.6; d > 1e-9 || d < -1e-9 {
		t.Fatalf("cost after the new result = %v, want 0.6", tot.CostUSD)
	}
	// Each stream's turn 1 is its own episode; the latest turn cost only its delta.
	if tot.Episodes != 2 {
		t.Fatalf("episodes across streams = %d, want 2", tot.Episodes)
	}
	if d := tot.TurnCostUSD - 0.3; d > 1e-9 || d < -1e-9 {
		t.Fatalf("this turn = %v, want 0.3 (0.5 cumulative - 0.2)", tot.TurnCostUSD)
	}
}

func rawEv(seq uint64, at time.Time, raw string) Event {
	return Event{ActorID: "a", StreamID: "s", Seq: seq, Kind: KindEvent, Turn: 1, ObservedAt: at, Raw: []byte(raw), Index: DeriveIndex([]byte(raw))}
}

func TestTotalsContext(t *testing.T) {
	t0 := time.Unix(1000, 0)
	main := func(seq uint64, in, cc, cr, out int64) Event {
		return rawEv(seq, t0.Add(time.Duration(seq)*time.Second), fmt.Sprintf(
			`{"type":"assistant","parent_tool_use_id":null,"message":{"model":"claude-x","usage":{"input_tokens":%d,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d,"output_tokens":%d}}}`,
			in, cc, cr, out))
	}
	var tot Totals
	steps := []struct {
		ev          Event
		ctx, window int64
	}{
		{main(1, 10, 34000, 0, 4), 34014, 0},
		// a subagent's message is not the main thread's context
		{rawEv(2, t0.Add(2*time.Second), `{"type":"assistant","parent_tool_use_id":"toolu_1","message":{"model":"claude-y","usage":{"input_tokens":5,"cache_read_input_tokens":90000}}}`), 34014, 0},
		{rawEv(3, t0.Add(3*time.Second), `{"type":"result","total_cost_usd":0.1,"usage":{"input_tokens":1},"modelUsage":{"claude-x[1m]":{"contextWindow":1000000},"claude-y":{"contextWindow":200000}}}`), 34014, 1000000},
		{main(4, 2, 100, 34014, 50), 34166, 1000000},
		{rawEv(5, t0.Add(5*time.Second), `{"type":"cove","subtype":"command","command":"clear","by":"Bob","ignored":"live episode"}`), 34166, 1000000},
		{rawEv(6, t0.Add(6*time.Second), `{"type":"cove","subtype":"command","command":"clear","by":"Bob"}`), 0, 1000000},
		{main(7, 3, 20000, 0, 1), 20004, 1000000},
		{rawEv(8, t0.Add(8*time.Second), `{"type":"system","subtype":"compact_boundary","compact_metadata":{"pre_tokens":20004}}`), 0, 1000000},
	}
	for i, s := range steps {
		tot.Add(s.ev)
		if tot.Context != s.ctx || tot.ContextWindow != s.window {
			t.Fatalf("step %d: context = %d / %d, want %d / %d", i, tot.Context, tot.ContextWindow, s.ctx, s.window)
		}
	}
	// an older stream folded later doesn't override the latest context
	tot.Add(Event{ActorID: "a", StreamID: "old", Seq: 1, Kind: KindEvent, ObservedAt: t0, Raw: main(1, 1, 1, 1, 1).Raw, Index: Index{Type: "assistant"}})
	if tot.Context != 0 {
		t.Fatalf("an older stream's message overrode the reset: %d", tot.Context)
	}
}

// claude carries a session's total_cost_usd across --continue episodes, so a
// continued episode's results count only their growth.
func TestTotalsCostAcrossContinue(t *testing.T) {
	t0 := time.Unix(1000, 0)
	r := func(seq uint64, turn uint32, sess string, cost float64) Event {
		ev := res("a", "s", seq, turn, cost, 0, 0, false, t0.Add(time.Duration(seq)*time.Second))
		ev.Index.ClaudeSessionID = sess
		return ev
	}
	var tot Totals
	for _, ev := range []Event{
		r(1, 1, "c1", 1.00),
		r(2, 2, "c1", 1.10), // --continue: 1.00 carried over
		r(3, 2, "c1", 1.30),
		r(4, 3, "c2", 0.05), // after a Clear: a new claude session
		r(5, 4, "c2", 0.07),
	} {
		tot.Add(ev)
	}
	if d := tot.CostUSD - 1.37; d > 1e-9 || d < -1e-9 {
		t.Fatalf("cost = %v, want 1.37", tot.CostUSD)
	}
	if d := tot.TurnCostUSD - 0.02; d > 1e-9 || d < -1e-9 {
		t.Fatalf("this turn = %v, want 0.02", tot.TurnCostUSD)
	}
}
