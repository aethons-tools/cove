package adminui_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
)

func suggestions(t *testing.T, h http.Handler, query string) []string {
	t.Helper()
	rec := get(t, h, "/ui/suggest?"+query)
	if rec.Code != http.StatusOK {
		t.Fatalf("suggest %s = %d", query, rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("suggest Content-Type = %q", ct)
	}
	var out []string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("suggest %s: %v", query, err)
	}
	return out
}

func suggestFixture(t *testing.T) http.Handler {
	t.Helper()
	store := seedProjects(t) // acme (dev, ops; alice; eng), beta, default (solo)
	if err := store.AddDestination(jam.Destination{Name: "git", Route: "/git/", Upstream: "https://github.com"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := jam.PushStudioKit(store, "web", "kind: studio\n"); err != nil {
		t.Fatal(err)
	}
	return adminui.Handler(store, testLogger(), nil, nil, anyCred, nil,
		adminui.WithCredentialNames("gh-pat", "anth-key"))
}

func TestSuggestKinds(t *testing.T) {
	h := suggestFixture(t)
	for query, want := range map[string][]string{
		"kind=projects":             {"acme", "beta", "default"},
		"kind=roles&project=acme":   {"dev", "ops"},
		"kind=roles&project=":       {"solo"}, // blank = default
		"kind=kits":                 {"web"},
		"kind=destinations":         {"git"},
		"kind=credentials":          {"anth-key", "gh-pat"},
		"kind=targets&project=acme": {"channel:*", "channel:eng", "human:*", "human:alice"},
		"kind=services":             {"discord"},
	} {
		if got := suggestions(t, h, query); !slices.Equal(got, want) {
			t.Errorf("%s = %v, want %v", query, got, want)
		}
	}
	// participants: every roster human/channel plus the studios' actors
	got := suggestions(t, h, "kind=participants")
	for _, want := range []string{"human:alice", "channel:eng", "actor:studio-acme", "actor:studio-solo"} {
		if !slices.Contains(got, want) {
			t.Errorf("participants missing %q: %v", want, got)
		}
	}
}

func TestSuggestUnknownKindAndEmpty(t *testing.T) {
	h := suggestFixture(t)
	if rec := get(t, h, "/ui/suggest?kind=bogus"); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown kind = %d, want 400", rec.Code)
	}
	if got := suggestions(t, h, "kind=roles&project=nope"); got == nil || len(got) != 0 {
		t.Errorf("unknown project roles = %v, want []", got)
	}
	// no credential names configured → an empty list, not an error
	plain := adminui.Handler(newStore(t), testLogger(), nil, nil, anyCred, nil)
	if got := suggestions(t, plain, "kind=credentials"); len(got) != 0 {
		t.Errorf("credentials without names = %v", got)
	}
}
