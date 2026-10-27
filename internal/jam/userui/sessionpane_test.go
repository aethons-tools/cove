package userui

import (
	"strings"
	"testing"
)

func TestLinksIn(t *testing.T) {
	got := linksIn("see https://a.test/x. and (https://b.test/y) or https://en.wikipedia.org/wiki/Go_(game), `https://c.test`")
	want := []string{"https://a.test/x", "https://b.test/y", "https://en.wikipedia.org/wiki/Go_(game)", "https://c.test"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("linksIn = %q, want %q", got, want)
	}
}
