package adminui

import (
	"math"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/sessionevents"
	"github.com/aethons-tools/cove/internal/jam/sessionview"
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

// Golden: the admin timeline fragment for each event family. Pins the output
// across the move of viewOf into sessionview.
func TestSessionEventFragmentGolden(t *testing.T) {
	mk := func(raw string, idx sessionevents.Index) sessionevents.Event {
		return sessionevents.Event{Kind: sessionevents.KindEvent, Seq: 4, Turn: 1, Raw: []byte(raw), Index: idx}
	}
	cases := []struct {
		name string
		ev   sessionevents.Event
		want string
	}{
		{"thinking", mk(`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"Plan:\nread it"}]}}`, sessionevents.Index{}), "<div class=\"ev\">\n  <span class=\"turn mono\">t1</span>\n  <div class=\"head\"><span class=\"label mono\">thinking</span>\n  <span class=\"summary\">Plan: read it</span>\n  </div>\n  <details><summary>details</summary><pre>Plan:\nread it</pre></details>\n  <details><summary>raw JSON</summary><pre>{\n  &#34;type&#34;: &#34;assistant&#34;,\n  &#34;message&#34;: {\n    &#34;content&#34;: [\n      {\n        &#34;type&#34;: &#34;thinking&#34;,\n        &#34;thinking&#34;: &#34;Plan:\\nread it&#34;\n      }\n    ]\n  }\n}</pre></details>\n</div>"},
		{"tool_use", mk(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu1","name":"Bash","input":{"command":"ls"}}]}}`, sessionevents.Index{}), "<div class=\"ev\">\n  <span class=\"turn mono\">t1</span>\n  <div class=\"head\"><span class=\"label mono\">tool_use Bash</span>\n  <span class=\"summary\">{&#34;command&#34;:&#34;ls&#34;}</span>\n  </div>\n  <details><summary>details</summary><pre>{\n  &#34;command&#34;: &#34;ls&#34;\n}</pre></details>\n  <details><summary>raw JSON</summary><pre>{\n  &#34;type&#34;: &#34;assistant&#34;,\n  &#34;message&#34;: {\n    &#34;content&#34;: [\n      {\n        &#34;type&#34;: &#34;tool_use&#34;,\n        &#34;id&#34;: &#34;tu1&#34;,\n        &#34;name&#34;: &#34;Bash&#34;,\n        &#34;input&#34;: {\n          &#34;command&#34;: &#34;ls&#34;\n        }\n      }\n    ]\n  }\n}</pre></details>\n</div>"},
		{"tool_result_error", mk(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu1","is_error":true,"content":"boom"}]}}`, sessionevents.Index{}), "<div class=\"ev error\">\n  <span class=\"turn mono\">t1</span>\n  <div class=\"head\"><span class=\"label mono\">tool_result</span><span class=\"pill phase-lost\">error</span>\n  <span class=\"summary\">4 bytes</span>\n  </div>\n  <details><summary>details</summary><pre>boom</pre></details>\n  <details><summary>raw JSON</summary><pre>{\n  &#34;type&#34;: &#34;user&#34;,\n  &#34;message&#34;: {\n    &#34;content&#34;: [\n      {\n        &#34;type&#34;: &#34;tool_result&#34;,\n        &#34;tool_use_id&#34;: &#34;tu1&#34;,\n        &#34;is_error&#34;: true,\n        &#34;content&#34;: &#34;boom&#34;\n      }\n    ]\n  }\n}</pre></details>\n</div>"},
		{"result", mk(`{"type":"result","subtype":"success","result":"ok"}`, sessionevents.Index{CostUSD: 0.5, InputTokens: 3, OutputTokens: 4, DurationMS: 1500}), "<div class=\"ev\">\n  <span class=\"turn mono\">t1</span>\n  <div class=\"head\"><span class=\"label mono\">result</span>\n  <span class=\"summary\">$0.5000 episode total · in 3 / out 4 tokens · 1.5s</span>\n  </div>\n  <details><summary>details</summary><pre>ok</pre></details>\n  <details><summary>raw JSON</summary><pre>{\n  &#34;type&#34;: &#34;result&#34;,\n  &#34;subtype&#34;: &#34;success&#34;,\n  &#34;result&#34;: &#34;ok&#34;\n}</pre></details>\n</div>"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := fragment("session-event", sessionview.View(c.ev)); got != c.want {
				t.Fatalf("fragment:\n got %q\nwant %q", got, c.want)
			}
		})
	}
}
