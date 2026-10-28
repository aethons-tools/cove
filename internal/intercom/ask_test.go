package intercom

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
)

const (
	tAsker   ident.ID = "ses_01j9q3aaaaaaaaaaaaaaaaaaaa"
	tOther   ident.ID = "ses_01j9q3bbbbbbbbbbbbbbbbbbbb"
	tAlice   ident.ID = "usr_01j9q3cccccccccccccccccccc"
	tBob     ident.ID = "usr_01j9q3dddddddddddddddddddd"
	tChannel ident.ID = "chn_01j9q3eeeeeeeeeeeeeeeeeeee"
)

var yesNo = []AskOption{{ID: "yes", Label: "Yes"}, {ID: "no", Label: "No", Detail: "hold off"}}

func askSquawk(id string, from ident.ID, ev AskEvent) Squawk {
	return Squawk{ID: id, Channel: tChannel, From: from, Body: "x", At: time.Unix(100, 0), Seq: 7, Parts: []Part{AskPart(ev)}}
}

func openAsk(t *testing.T, of ...ident.ID) Ask {
	t.Helper()
	a, err := ApplyAsk(nil, askSquawk("a1", tAsker, AskEvent{Event: AskEventAsk, Question: "Merge PR #12?", Options: yesNo, Of: of}))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestApplyAskOpens(t *testing.T) {
	a := openAsk(t)
	if a.ID != "a1" || a.Asker != tAsker || a.Channel != tChannel || a.Version != 1 || a.State != AskOpen || a.LastSeq != 7 || len(a.Options) != 2 {
		t.Fatalf("ask = %+v", a)
	}
}

func TestApplyAskRejectsBadAsks(t *testing.T) {
	long := strings.Repeat("x", 2049)
	cases := map[string]AskEvent{
		"no question":    {Event: AskEventAsk, Options: yesNo},
		"long question":  {Event: AskEventAsk, Question: long, Options: yesNo},
		"one option":     {Event: AskEventAsk, Question: "q", Options: yesNo[:1]},
		"nine options":   {Event: AskEventAsk, Question: "q", Options: nine()},
		"duplicate id":   {Event: AskEventAsk, Question: "q", Options: []AskOption{{ID: "a", Label: "A"}, {ID: "a", Label: "B"}}},
		"other reserved": {Event: AskEventAsk, Question: "q", Options: []AskOption{{ID: "other", Label: "A"}, {ID: "b", Label: "B"}}},
		"bad id":         {Event: AskEventAsk, Question: "q", Options: []AskOption{{ID: "A B", Label: "A"}, {ID: "b", Label: "B"}}},
		"no label":       {Event: AskEventAsk, Question: "q", Options: []AskOption{{ID: "a"}, {ID: "b", Label: "B"}}},
		"long detail":    {Event: AskEventAsk, Question: "q", Options: []AskOption{{ID: "a", Label: "A", Detail: strings.Repeat("d", 1025)}, {ID: "b", Label: "B"}}},
	}
	for name, ev := range cases {
		if _, err := ApplyAsk(nil, askSquawk("a1", tAsker, ev)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
	if _, err := ApplyAsk(nil, askSquawk("a2", tAsker, AskEvent{Event: AskEventAnswer, AskID: "a1", Version: 1, Choice: "yes"})); !errors.Is(err, ErrAskNotFound) {
		t.Errorf("event on no ask: %v, want ErrAskNotFound", err)
	}
	a := openAsk(t)
	if _, err := ApplyAsk(&a, askSquawk("a2", tAsker, AskEvent{Event: AskEventAsk, Question: "q", Options: yesNo})); !errors.Is(err, ErrInvalid) {
		t.Errorf("ask over an existing ask: %v", err)
	}
}

func nine() []AskOption {
	var out []AskOption
	for _, id := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"} {
		out = append(out, AskOption{ID: id, Label: id})
	}
	return out
}

func TestApplyAskAmendAndWithdraw(t *testing.T) {
	a := openAsk(t)
	if _, err := ApplyAsk(&a, askSquawk("e1", tOther, AskEvent{Event: AskEventAmend, AskID: "a1", Question: "x"})); !errors.Is(err, ErrAskForbidden) {
		t.Fatalf("amend by another session: %v", err)
	}
	b, err := ApplyAsk(&a, askSquawk("e1", tAsker, AskEvent{Event: AskEventAmend, AskID: "a1", Question: "Merge PR #12 now?"}))
	if err != nil {
		t.Fatal(err)
	}
	if b.Version != 2 || b.Question != "Merge PR #12 now?" || len(b.Options) != 2 || len(b.History) != 1 || b.History[0].Question != "Merge PR #12?" || b.History[0].Version != 1 {
		t.Fatalf("amended = %+v", b)
	}
	if a.Version != 1 || len(a.History) != 0 {
		t.Fatalf("ApplyAsk mutated prev: %+v", a)
	}
	w, err := ApplyAsk(&b, askSquawk("e2", tAsker, AskEvent{Event: AskEventWithdraw, AskID: "a1", Reason: "found it"}))
	if err != nil || w.State != AskWithdrawn || w.Reason != "found it" {
		t.Fatalf("withdrawn = %+v, %v", w, err)
	}
	if _, err := ApplyAsk(&w, askSquawk("e3", tAlice, AskEvent{Event: AskEventAnswer, AskID: "a1", Version: 2, Choice: "yes"})); !errors.Is(err, ErrAskClosed) {
		t.Fatalf("answer after withdraw: %v", err)
	}
	if _, err := ApplyAsk(&w, askSquawk("e3", tAsker, AskEvent{Event: AskEventAmend, AskID: "a1", Question: "y"})); !errors.Is(err, ErrAskClosed) {
		t.Fatalf("amend after withdraw: %v", err)
	}
}

func TestApplyAskAnswer(t *testing.T) {
	a := openAsk(t, tAlice)
	cases := []struct {
		name string
		from ident.ID
		ev   AskEvent
		want error
	}{
		{"by a session", tOther, AskEvent{Event: AskEventAnswer, AskID: "a1", Version: 1, Choice: "yes"}, ErrAskForbidden},
		{"outside of", tBob, AskEvent{Event: AskEventAnswer, AskID: "a1", Version: 1, Choice: "yes"}, ErrAskForbidden},
		{"stale version", tAlice, AskEvent{Event: AskEventAnswer, AskID: "a1", Version: 0, Choice: "yes"}, ErrAskChanged},
		{"unknown choice", tAlice, AskEvent{Event: AskEventAnswer, AskID: "a1", Version: 1, Choice: "maybe"}, ErrInvalid},
		{"other without comment", tAlice, AskEvent{Event: AskEventAnswer, AskID: "a1", Version: 1, Choice: "other"}, ErrInvalid},
		{"long comment", tAlice, AskEvent{Event: AskEventAnswer, AskID: "a1", Version: 1, Choice: "yes", Comment: strings.Repeat("c", 4097)}, ErrInvalid},
	}
	for _, c := range cases {
		if _, err := ApplyAsk(&a, askSquawk("e1", c.from, c.ev)); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
	got, err := ApplyAsk(&a, askSquawk("e1", tAlice, AskEvent{Event: AskEventAnswer, AskID: "a1", Version: 1, Choice: "other", Comment: "after lunch"}))
	if err != nil {
		t.Fatal(err)
	}
	if got.State != AskAnswered || got.Answer == nil || got.Answer.By != tAlice || got.Answer.Choice != "other" || got.Answer.Comment != "after lunch" || got.Answer.Version != 1 || !got.Answer.At.Equal(time.Unix(100, 0)) {
		t.Fatalf("answered = %+v %+v", got, got.Answer)
	}
	if _, err := ApplyAsk(&got, askSquawk("e2", tAlice, AskEvent{Event: AskEventAnswer, AskID: "a1", Version: 1, Choice: "yes"})); !errors.Is(err, ErrAskClosed) {
		t.Fatalf("second answer: %v", err)
	}
	// A prev in another channel is no such ask there.
	elsewhere := askSquawk("e3", tAlice, AskEvent{Event: AskEventAnswer, AskID: "a1", Version: 1, Choice: "yes"})
	elsewhere.Channel = "chn_01j9q3ffffffffffffffffffff"
	if _, err := ApplyAsk(&a, elsewhere); !errors.Is(err, ErrAskNotFound) {
		t.Fatalf("answer from another channel: %v", err)
	}
}

func TestAskBody(t *testing.T) {
	a := openAsk(t)
	cases := map[string]struct {
		ev   AskEvent
		want []string
	}{
		"ask":      {AskEvent{Event: AskEventAsk, Question: a.Question, Options: yesNo}, []string{"asked", "Merge PR #12?", "Yes", "No"}},
		"amend":    {AskEvent{Event: AskEventAmend, AskID: "a1"}, []string{"edited", "Merge PR #12?"}},
		"withdraw": {AskEvent{Event: AskEventWithdraw, AskID: "a1", Reason: "found it"}, []string{"withdrew", "Merge PR #12?", "found it"}},
		"answer":   {AskEvent{Event: AskEventAnswer, AskID: "a1", Choice: "no", Comment: "wait"}, []string{"alice", "No", "wait"}},
		"other":    {AskEvent{Event: AskEventAnswer, AskID: "a1", Choice: "other", Comment: "later"}, []string{"alice", "other", "later"}},
	}
	for name, c := range cases {
		body := AskBody(c.ev, a, "alice")
		for _, w := range c.want {
			if !strings.Contains(body, w) {
				t.Errorf("%s: body %q missing %q", name, body, w)
			}
		}
	}
}

func TestPreparePartsRules(t *testing.T) {
	base := Squawk{Channel: tChannel, From: tAsker, Body: "x"}
	ok := base
	ok.Parts = []Part{AskPart(AskEvent{Event: AskEventAsk, Question: "q", Options: yesNo})}
	if _, err := Prepare(ok); err != nil {
		t.Fatalf("an ask part: %v", err)
	}
	if ev, found := AskOf(ok); !found || ev.Question != "q" {
		t.Fatalf("AskOf = %+v, %v", ev, found)
	}
	if _, found := AskOf(base); found {
		t.Fatal("AskOf on a plain squawk")
	}
	bad := map[string][]Part{
		"bad reference": {{Type: "image/png", Ref: "blb_x", Name: "a.png", Size: 3}},
		"unknown type":  {{Type: "application/x-thing", Data: []byte(`{}`)}},
		"two asks":      {ok.Parts[0], ok.Parts[0]},
		"undecodable":   {{Type: AskPartType, Data: []byte(`not json`)}},
	}
	for name, parts := range bad {
		m := base
		m.Parts = parts
		if _, err := Prepare(m); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
}

// A turn-end part marks an auto-posted turn end: no data, at most one.
func TestValidatePartsTurnEnd(t *testing.T) {
	te := Part{Type: TurnEndPartType}
	if err := validateParts([]Part{te}); err != nil {
		t.Fatalf("one turn-end part: %v", err)
	}
	if err := validateParts([]Part{te, te}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("two turn-end parts: %v, want ErrInvalid", err)
	}
	if err := validateParts([]Part{{Type: TurnEndPartType, Data: []byte(`{}`)}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a turn-end part with data: %v, want ErrInvalid", err)
	}
	if !IsTurnEnd(Squawk{Parts: []Part{te}}) || IsTurnEnd(Squawk{Body: "x"}) {
		t.Fatal("IsTurnEnd")
	}
}
