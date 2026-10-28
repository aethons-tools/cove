package adminui

import (
	"math"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/sessionevents"
)

// total_cost_usd is cumulative per claude process (episode = turn); usage is
// per result. Real numbers from one process with two results (0.1364 → 0.1453).
func TestTotalsCostIsLastPerEpisode(t *testing.T) {
	res := func(turn uint32, cost float64, in, out int64) sessionevents.Event {
		return sessionevents.Event{Kind: sessionevents.KindEvent, Turn: turn,
			Index: sessionevents.Index{Type: "result", CostUSD: cost, InputTokens: in, OutputTokens: out}}
	}
	var tot sessionevents.Totals
	tot.Add(sessionevents.Event{Kind: sessionevents.KindEvent, Turn: 1, Index: sessionevents.Index{Type: "assistant", ToolName: "Bash"}})
	tot.Add(res(1, 0.1364288, 10, 116))
	tot.Add(res(1, 0.145289, 20, 21))
	tot.Add(res(2, 0.05, 5, 7))
	if math.Abs(tot.CostUSD-0.195289) > 1e-9 {
		t.Fatalf("CostUSD = %v, want 0.195289 (last per episode, summed)", tot.CostUSD)
	}
	if tot.Turns != 3 || tot.Episodes != 2 {
		t.Fatalf("Turns=%d Episodes=%d, want 3 and 2", tot.Turns, tot.Episodes)
	}
	if tot.InputTokens != 35 || tot.OutputTokens != 144 || tot.ToolCalls != 1 {
		t.Fatalf("tokens %d/%d tools %d", tot.InputTokens, tot.OutputTokens, tot.ToolCalls)
	}
}
