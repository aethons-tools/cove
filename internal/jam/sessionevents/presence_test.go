package sessionevents

import (
	"testing"
	"time"
)

func ev(actor, raw string) Event {
	return Event{ActorID: actor, Kind: KindEvent, Raw: []byte(raw), Index: DeriveIndex([]byte(raw))}
}

func TestDeriveStatus(t *testing.T) {
	for _, c := range []struct {
		raw         string
		state, tool string
		ok          bool
	}{
		{`{"type":"system","subtype":"init"}`, StatusThinking, "", true},
		{`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"hm"}]}}`, StatusThinking, "", true},
		{`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"secret"}}]}}`, StatusRunning, "Bash", true},
		{`{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}`, StatusWriting, "", true},
		{`{"type":"user","message":{"content":[{"type":"tool_result","content":"x"}]}}`, StatusThinking, "", true},
		{`{"type":"result","subtype":"success"}`, StatusIdle, "", true},
		// Progress and hook events don't change what the session is doing.
		{`{"type":"system","subtype":"hook_response"}`, "", "", false},
		{`not json`, "", "", false},
	} {
		got, ok := DeriveStatus(ev("a", c.raw))
		if ok != c.ok || got.State != c.state || got.Tool != c.tool {
			t.Errorf("%s: got %+v ok=%v, want %s/%s ok=%v", c.raw, got, ok, c.state, c.tool, c.ok)
		}
	}
	if _, ok := DeriveStatus(Event{ActorID: "a", Kind: KindGap}); ok {
		t.Error("a gap row carries no status")
	}
}

func TestPresenceTracksAndSignalsOnlyOnChange(t *testing.T) {
	now := time.Unix(100, 0)
	p := NewPresence(func() time.Time { return now })
	ch, cancel := p.Subscribe()
	defer cancel()

	if _, ok := p.Status("a"); ok {
		t.Fatal("unknown actor should have no status")
	}
	p.Observe(ev("a", `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash"}]}}`))
	if s, ok := p.Status("a"); !ok || s.State != StatusRunning || s.Tool != "Bash" || !s.At.Equal(now) {
		t.Fatalf("status = %+v ok=%v", s, ok)
	}
	select {
	case <-ch:
	default:
		t.Fatal("a status change should signal")
	}
	// Same status again, and a no-status event: no signal.
	p.Observe(ev("a", `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash"}]}}`))
	p.Observe(ev("a", `{"type":"system","subtype":"hook_response"}`))
	select {
	case <-ch:
		t.Fatal("an unchanged status should not signal")
	default:
	}
	p.Observe(ev("a", `{"type":"result"}`))
	if s, _ := p.Status("a"); s.State != StatusIdle {
		t.Fatalf("after result: %+v", s)
	}
	select {
	case <-ch:
	default:
		t.Fatal("idle after running should signal")
	}
}

func TestHubObserversSeeEveryPublish(t *testing.T) {
	h := NewHub()
	var got []uint64
	h.Observe(func(e Event) { got = append(got, e.Seq) })
	h.Publish(Event{ActorID: "a", Seq: 1})
	h.Publish(Event{ActorID: "b", Seq: 2}) // no subscribers for b: observers still see it
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("observed %v", got)
	}
}

func TestPresenceCountsInits(t *testing.T) {
	p := NewPresence(nil)
	ch, cancel := p.Subscribe()
	defer cancel()
	if n := p.Inits("a"); n != 0 {
		t.Fatalf("Inits before any = %d", n)
	}
	p.Observe(ev("a", `{"type":"system","subtype":"init"}`))
	<-ch
	// A second init while already thinking changes no status, but is a new
	// mark on the sparkline: it still counts and signals.
	p.Observe(ev("a", `{"type":"system","subtype":"init"}`))
	select {
	case <-ch:
	default:
		t.Fatal("an init should signal even when the status is unchanged")
	}
	p.Observe(ev("a", `{"type":"system","subtype":"hook_response"}`))
	if n := p.Inits("a"); n != 2 {
		t.Fatalf("Inits = %d, want 2", n)
	}
	if n := p.Inits("b"); n != 0 {
		t.Fatalf("Inits(b) = %d", n)
	}
}

// Stream-json lines a subagent's lifecycle produces (Claude Code 2.1.x),
// trimmed to the fields Presence reads plus a little content it must ignore.
const (
	lnAgentCall  = `{"type":"assistant","parent_tool_use_id":null,"message":{"content":[{"type":"tool_use","id":"tu_a","name":"Agent","input":{"prompt":"secret"}}]}}`
	lnAgentBg    = `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu_g","name":"Agent","input":{"prompt":"secret","run_in_background":true}}]}}`
	lnAgentBgOK  = `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu_g","content":"Async agent launched"}]}}`
	lnAgentDone  = `{"type":"user","parent_tool_use_id":null,"message":{"content":[{"type":"tool_result","tool_use_id":"tu_a","content":"secret"}]}}`
	lnTaskCall   = `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu_t","name":"Task","input":{}}]}}`
	lnTaskDone   = `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu_t","content":"x"}]}}`
	lnBashCall   = `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu_b","name":"Bash","input":{}}]}}`
	lnBashDone   = `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu_b","content":"x"}]}}`
	lnBgAgents2  = `{"type":"system","subtype":"background_tasks_changed","tasks":[{"task_id":"a1","task_type":"local_agent","description":"d"},{"task_id":"a2","task_type":"local_agent","description":"d"},{"task_id":"b1","task_type":"local_bash","description":"d"}]}`
	lnBgAgents1  = `{"type":"system","subtype":"background_tasks_changed","tasks":[{"task_id":"a2","task_type":"local_agent","description":"d"}]}`
	lnBgNone     = `{"type":"system","subtype":"background_tasks_changed","tasks":[]}`
	lnTurnEnd    = `{"type":"result","subtype":"success"}`
	lnTurnInit   = `{"type":"system","subtype":"init","session_id":"s"}`
	lnUserPrompt = `{"type":"user","message":{"role":"user","content":"hello"}}`
)

func observeAll(p *Presence, actor string, lines ...string) {
	for _, l := range lines {
		p.Observe(ev(actor, l))
	}
}

func wantSubagents(t *testing.T, p *Presence, actor string, want int) {
	t.Helper()
	if n := p.Subagents(actor); n != want {
		t.Fatalf("Subagents(%s) = %d, want %d", actor, n, want)
	}
}

func TestPresenceCountsForegroundSubagents(t *testing.T) {
	p := NewPresence(nil)
	wantSubagents(t, p, "a", 0)
	observeAll(p, "a", lnTurnInit, lnAgentCall)
	wantSubagents(t, p, "a", 1)
	observeAll(p, "a", lnTaskCall) // a second, concurrent one (the legacy Task name)
	wantSubagents(t, p, "a", 2)
	observeAll(p, "a", lnBashCall, lnBashDone) // other tools don't count
	wantSubagents(t, p, "a", 2)
	observeAll(p, "a", lnAgentDone)
	wantSubagents(t, p, "a", 1)
	observeAll(p, "a", lnTaskDone)
	wantSubagents(t, p, "a", 0)
	observeAll(p, "a", lnUserPrompt, lnAgentDone) // a string prompt, a repeated result: harmless
	wantSubagents(t, p, "a", 0)
	wantSubagents(t, p, "b", 0)
}

func TestPresenceCountsSubagentsInOneMessage(t *testing.T) {
	p := NewPresence(nil)
	observeAll(p, "a", `{"type":"assistant","message":{"content":[{"type":"text","text":"x"},{"type":"tool_use","id":"1","name":"Agent"},{"type":"tool_use","id":"2","name":"Agent"}]}}`)
	wantSubagents(t, p, "a", 2)
	observeAll(p, "a", `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"1"},{"type":"tool_result","tool_use_id":"2"}]}}`)
	wantSubagents(t, p, "a", 0)
}

func TestPresenceCountsBackgroundSubagents(t *testing.T) {
	p := NewPresence(nil)
	// A background Agent call answers at once; the task list carries it on,
	// so the call itself never counts (nor counts twice before its answer).
	observeAll(p, "a", lnTurnInit, lnAgentBg)
	wantSubagents(t, p, "a", 0)
	observeAll(p, "a", lnBgAgents2)
	wantSubagents(t, p, "a", 2) // two agents; the background Bash isn't one
	observeAll(p, "a", lnAgentBgOK)
	wantSubagents(t, p, "a", 2)
	// claude starts every turn with an init, so neither a turn ending nor the
	// next one starting means the background agents stopped.
	observeAll(p, "a", lnTurnEnd, lnTurnInit)
	wantSubagents(t, p, "a", 2)
	observeAll(p, "a", lnBgAgents1)
	wantSubagents(t, p, "a", 1)
	observeAll(p, "a", lnTaskCall)
	wantSubagents(t, p, "a", 2) // foreground and background add up
	observeAll(p, "a", lnBgNone)
	wantSubagents(t, p, "a", 1)
}

func TestPresenceSubagentsTurnBoundaries(t *testing.T) {
	p := NewPresence(nil)
	observeAll(p, "a", lnAgentCall, lnTaskCall, lnTurnEnd) // results never came: the turn is over
	wantSubagents(t, p, "a", 0)
	observeAll(p, "a", lnAgentCall, lnTurnInit) // a new turn (or process) starts with none
	wantSubagents(t, p, "a", 0)
}

func TestPresenceSignalsOnSubagentChange(t *testing.T) {
	p := NewPresence(nil)
	ch, cancel := p.Subscribe()
	defer cancel()
	signalled := func() bool {
		select {
		case <-ch:
			return true
		default:
			return false
		}
	}
	observeAll(p, "a", lnAgentCall)
	signalled() // drain: the status changed too
	// The same running-Agent status: only the count changes.
	observeAll(p, "a", `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu_2","name":"Agent"}]}}`)
	if !signalled() {
		t.Fatal("a second subagent should signal")
	}
	// A task list with no agents changes no count (and system events carry no status).
	observeAll(p, "a", `{"type":"system","subtype":"background_tasks_changed","tasks":[{"task_id":"b","task_type":"local_bash"}]}`)
	if signalled() {
		t.Fatal("an unchanged count should not signal")
	}
	observeAll(p, "a", lnBgAgents1)
	if !signalled() || p.Subagents("a") != 3 {
		t.Fatalf("a background agent should signal (count %d)", p.Subagents("a"))
	}
}
