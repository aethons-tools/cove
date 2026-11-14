package jam

import (
	"errors"
	"slices"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
)

// mentionsFixture is the icFixture's room "eng" with alice, bob, and two
// sessions labelled editor and checker as members.
func mentionsFixture(t *testing.T) (*icFixture, Instance, Instance) {
	t.Helper()
	f := newICFixture(t, "channel:*")
	editor := Instance{ActorID: string(ident.New(ident.Session)), Name: "editor", Project: "acme", Role: "impl", Phase: PhaseLive}
	checker := Instance{ActorID: string(ident.New(ident.Session)), Name: "checker", Project: "acme", Role: "impl", Phase: PhaseLive}
	for _, inst := range []Instance{editor, checker} {
		if err := f.store.PutInstance(inst); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range ids(f.alice, f.bob, editor, checker) {
		if err := f.store.JoinChannel(f.room.ID, p, 0); err != nil {
			t.Fatal(err)
		}
	}
	return f, editor, checker
}

// A post records whom it mentions: the members its body's @<label>s name —
// sessions by label or id, people by name — not its author, and no one
// outside the conversation.
func TestIntercomPostRecordsBodyMentions(t *testing.T) {
	f, editor, checker := mentionsFixture(t)
	pl := Planned{Channel: f.room, Audience: f.ic.audience(f.room, f.alice.ID)}
	for name, tc := range map[string]struct {
		body string
		want []ident.ID
	}{
		"a session by label":          {"@editor can you look?", ids(editor)},
		"a session by id":             {"cc @" + checker.ActorID, ids(checker)},
		"a person by name":            {"thoughts, @bob?", ids(f.bob)},
		"several, end of a sentence":  {"@editor and @checker, see @bob.", ids(editor, checker, f.bob)},
		"the author is not mentioned": {"@alice here", nil},
		"a non-member is not":         {"@carol", nil},
		"an email address is not":     {"mail editor@example.com", nil},
		"no mention":                  {"hello", nil},
	} {
		m, err := f.ic.Post(pl, intercom.Squawk{From: f.alice.ID, Body: tc.body})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := intercom.Mentions(m); !slices.Equal(got, tc.want) {
			t.Errorf("%s: mentions = %v, want %v", name, got, tc.want)
		}
	}
}

// send's mentions are session: targets naming session members of the
// conversation, by label or id; they add to the body's.
func TestIntercomResolveMentions(t *testing.T) {
	f, editor, checker := mentionsFixture(t)
	got, err := f.ic.ResolveMentions(f.room, []string{"session:editor", "session:" + checker.ActorID})
	if err != nil || !slices.Equal(got, ids(editor, checker)) {
		t.Fatalf("ResolveMentions = %v, %v", got, err)
	}
	for _, bad := range []string{"user:bob", "editor", "session:nobody", "session:" + f.ticket.ActorID} {
		if _, err := f.ic.ResolveMentions(f.room, []string{bad}); !errors.Is(err, ErrMentionUnresolved) {
			t.Errorf("ResolveMentions(%q) = %v, want ErrMentionUnresolved", bad, err)
		}
	}
	pl := Planned{Channel: f.room, Audience: f.ic.audience(f.room, f.alice.ID)}
	m, err := f.ic.Post(pl, intercom.Squawk{From: f.alice.ID, Body: "and @bob", Parts: []intercom.Part{intercom.MentionsPart(ids(editor))}})
	if err != nil {
		t.Fatal(err)
	}
	if got := intercom.Mentions(m); !slices.Equal(got, ids(editor, f.bob)) {
		t.Fatalf("mentions = %v, want editor and bob", got)
	}
}
