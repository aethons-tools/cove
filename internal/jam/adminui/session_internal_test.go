package adminui

import (
	"math"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/sessionevents"
)

func TestSSEWriteFraming(t *testing.T) {
	cases := []struct{ name, event, id, data, want string }{
		{"plain", "ev", "a:1", "x", "id: a:1\nevent: ev\ndata: x\n\n"},
		{"no id", "ev", "", "x", "event: ev\ndata: x\n\n"},
		{"CR in data", "ev", "", "a\rb", "event: ev\ndata: a\ndata: b\n\n"},
		{"CRLF in data", "ev", "", "a\r\nb", "event: ev\ndata: a\ndata: b\n\n"},
		{"LF CR mix", "ev", "", "a\n\rb", "event: ev\ndata: a\ndata: \ndata: b\n\n"},
		{"CR in id", "ev", "a\rretry: 1", "x", "id: a retry: 1\nevent: ev\ndata: x\n\n"},
		{"CRLF in id", "ev", "a\r\nb", "x", "id: a b\nevent: ev\ndata: x\n\n"},
		{"CR in event", "e\rid: z", "", "x", "event: e id: z\ndata: x\n\n"},
		{"CRLF in event", "e\r\nf", "", "x", "event: e f\ndata: x\n\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			sseWrite(rec, c.event, c.id, c.data)
			if got := rec.Body.String(); got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
			if strings.Contains(strings.ReplaceAll(rec.Body.String(), "\r\n", ""), "\r") {
				t.Fatal("raw CR in output")
			}
		})
	}
}

// total_cost_usd is cumulative per claude process (episode = turn); usage is
// per result. Real numbers from one process with two results (0.1364 → 0.1453).
func TestTotalsCostIsLastPerEpisode(t *testing.T) {
	res := func(turn uint32, cost float64, in, out int64) sessionevents.Event {
		return sessionevents.Event{Kind: sessionevents.KindEvent, Turn: turn,
			Index: sessionevents.Index{Type: "result", CostUSD: cost, InputTokens: in, OutputTokens: out}}
	}
	var tot totals
	tot.add(sessionevents.Event{Kind: sessionevents.KindEvent, Turn: 1, Index: sessionevents.Index{Type: "assistant", ToolName: "Bash"}})
	tot.add(res(1, 0.1364288, 10, 116))
	tot.add(res(1, 0.145289, 20, 21))
	tot.add(res(2, 0.05, 5, 7))
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
