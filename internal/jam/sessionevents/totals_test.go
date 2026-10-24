package sessionevents

import (
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
}
