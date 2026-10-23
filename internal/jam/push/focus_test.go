package push

import (
	"testing"
	"time"
)

func TestFocus(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	f := NewFocus(func() time.Time { return now })
	if f.Focused(u1, chat) {
		t.Fatal("focused before any Set")
	}
	f.Set(u1, chat)
	if !f.Focused(u1, chat) {
		t.Fatal("not focused after Set")
	}
	if f.Focused(u1, sess) || f.Focused(u2, chat) {
		t.Fatal("focus leaked to another channel or user")
	}
	now = now.Add(59 * time.Second)
	if !f.Focused(u1, chat) {
		t.Fatal("focus expired before 60 s")
	}
	f.Set(u1, chat) // the page re-posts
	now = now.Add(59 * time.Second)
	if !f.Focused(u1, chat) {
		t.Fatal("a re-post did not extend focus")
	}
	now = now.Add(2 * time.Second) // 61 s after the last Set: a crashed tab
	if f.Focused(u1, chat) {
		t.Fatal("still focused 61 s after the last Set")
	}
	f.Set(u1, chat)
	f.Set(u1, sess) // moving to another channel replaces it
	if f.Focused(u1, chat) || !f.Focused(u1, sess) {
		t.Fatal("Set did not move focus")
	}
	f.Set(u1, "")
	if f.Focused(u1, sess) {
		t.Fatal(`Set "" did not clear focus`)
	}
}
