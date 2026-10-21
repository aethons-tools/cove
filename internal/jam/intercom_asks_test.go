package jam

import (
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
)

var askYesNo = []intercom.AskOption{{ID: "yes", Label: "Yes"}, {ID: "no", Label: "No"}}

// askFixture: the standing session asks in its own channel, which alice and
// bob have joined; the hook records each open-asks count.
func askFixture(t *testing.T) (*icFixture, *[]int) {
	t.Helper()
	f := newICFixture(t)
	home := f.mustPlan(f.standing, "").Channel
	for _, u := range []ident.ID{f.alice.ID, f.bob.ID} {
		if err := f.ic.JoinChannel(u, home.ID); err != nil {
			t.Fatal(err)
		}
	}
	var counts []int
	f.ic.SetOnAsksChanged(func(s ident.ID, n int) {
		if s == ident.ID(f.standing.ActorID) {
			counts = append(counts, n)
		}
	})
	return f, &counts
}

func TestIntercomAskAndAnswer(t *testing.T) {
	f, counts := askFixture(t)
	a, ch, err := f.ic.Ask(f.poster(f.standing), "", AskInput{Question: "Merge PR #12?", Options: askYesNo}, f.now)
	if err != nil {
		t.Fatal(err)
	}
	if a.State != intercom.AskOpen || a.Version != 1 || ch.Kind != SourceSession {
		t.Fatalf("ask = %+v in %+v", a, ch)
	}
	if got := f.ic.OpenAsksIn(ch.ID); len(got) != 1 || !f.ic.MayAnswer(f.alice.ID, got[0]) || f.ic.MayAnswer(f.carol.ID, got[0]) {
		t.Fatalf("open asks %+v", got)
	}
	if _, err := f.ic.AnswerAsk(f.carol.ID, a.ID, 1, "yes", ""); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("a non-member answers: %v", err)
	}
	got, err := f.ic.AnswerAsk(f.alice.ID, a.ID, 1, "no", "wait for CI")
	if err != nil || got.State != intercom.AskAnswered {
		t.Fatalf("answer = %+v, %v", got, err)
	}
	inbox := f.log.InboxSince(ident.ID(f.standing.ActorID), 0, 0)
	ev, ok := intercom.AskOf(inbox[len(inbox)-1])
	if !ok || ev.Event != intercom.AskEventAnswer || ev.Choice != "no" || ev.Comment != "wait for CI" || inbox[len(inbox)-1].From != f.alice.ID {
		t.Fatalf("the session's inbox ends %+v (%+v)", inbox[len(inbox)-1], ev)
	}
	if _, err := f.ic.AnswerAsk(f.bob.ID, a.ID, 1, "yes", ""); !errors.Is(err, intercom.ErrAskClosed) {
		t.Fatalf("second answer: %v", err)
	}
	if want := []int{1, 0}; len(*counts) != 2 || (*counts)[0] != want[0] || (*counts)[1] != want[1] {
		t.Fatalf("hook counts %v, want %v", *counts, want)
	}
	if l := f.ic.AsksOf(ident.ID(f.standing.ActorID), intercom.AskAnswered); len(l) != 1 {
		t.Fatalf("AsksOf answered = %+v", l)
	}
}

func TestIntercomAskRules(t *testing.T) {
	f, _ := askFixture(t)
	if _, _, err := f.ic.Ask(f.poster(f.standing), "user:bob", AskInput{Question: "q", Options: askYesNo}, f.now); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("ask beyond addressing: %v", err)
	}
	if _, _, err := f.ic.Ask(f.poster(f.standing), "", AskInput{Question: "q", Options: askYesNo, Of: []string{"user:nobody"}}, f.now); !errors.Is(err, ErrSendUnresolved) {
		t.Fatalf("of an unknown person: %v", err)
	}
	a, _, err := f.ic.Ask(f.poster(f.standing), "", AskInput{Question: "q", Options: askYesNo, Of: []string{"user:alice"}}, f.now)
	if err != nil {
		t.Fatal(err)
	}
	if f.ic.MayAnswer(f.bob.ID, a) {
		t.Fatal("bob may answer an ask for alice")
	}
	if _, err := f.ic.AnswerAsk(f.bob.ID, a.ID, 1, "yes", ""); !errors.Is(err, intercom.ErrAskForbidden) {
		t.Fatalf("bob answers an ask for alice: %v", err)
	}
	if _, err := f.ic.AmendAsk(f.poster(f.ticket), a.ID, "x", nil); !errors.Is(err, intercom.ErrAskForbidden) {
		t.Fatalf("another session amends: %v", err)
	}
	b, err := f.ic.AmendAsk(f.poster(f.standing), a.ID, "q2", nil)
	if err != nil || b.Version != 2 {
		t.Fatalf("amend = %+v, %v", b, err)
	}
	if _, err := f.ic.AnswerAsk(f.alice.ID, a.ID, 1, "yes", ""); !errors.Is(err, intercom.ErrAskChanged) {
		t.Fatalf("stale answer: %v", err)
	}
	if _, err := f.ic.AnswerAsk(f.alice.ID, "nope", 1, "yes", ""); !errors.Is(err, intercom.ErrAskNotFound) {
		t.Fatalf("no such ask: %v", err)
	}
	if w, err := f.ic.WithdrawAsk(f.poster(f.standing), a.ID, "found it"); err != nil || w.State != intercom.AskWithdrawn {
		t.Fatalf("withdraw = %+v, %v", w, err)
	}
}

// When the asking session ends its open asks close as withdrawn, and it no
// longer counts as asking.
func TestIntercomEndedWithdrawsAsks(t *testing.T) {
	f, counts := askFixture(t)
	a, _, err := f.ic.Ask(f.poster(f.standing), "", AskInput{Question: "q", Options: askYesNo}, f.now)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.ic.Ended(f.standing); err != nil {
		t.Fatal(err)
	}
	got, _ := f.log.GetAsk(a.ID)
	if got.State != intercom.AskWithdrawn || got.Reason != "session ended" {
		t.Fatalf("after Ended: %+v", got)
	}
	if last := (*counts)[len(*counts)-1]; last != 0 {
		t.Fatalf("hook counts %v, want ending at 0", *counts)
	}
}

func TestSupervisorSetOpenAsks(t *testing.T) {
	store := NewMemStore()
	if err := store.PutInstance(Instance{ActorID: "s1", Phase: PhaseLive}); err != nil {
		t.Fatal(err)
	}
	sup := NewSupervisor(store, &fakeLauncher{}, "holder-A", time.Minute, time.Minute, time.Now, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := sup.SetOpenAsks("s1", 2); err != nil {
		t.Fatal(err)
	}
	if inst, _ := store.GetInstance("s1"); inst.OpenAsks != 2 || !AskedForPerson(inst) {
		t.Fatalf("instance = %+v", inst)
	}
	if err := sup.SetOpenAsks("gone", 1); err != nil {
		t.Fatalf("a gone actor is no error: %v", err)
	}
}
