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
