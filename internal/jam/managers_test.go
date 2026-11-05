package jam

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestValidManagerName(t *testing.T) {
	if err := ValidManagerName("mac-1"); err != nil {
		t.Fatalf("mac-1: %v", err)
	}
	if err := ValidManagerName("local"); err == nil || err.Error() != `manager name "local" is reserved` {
		t.Fatalf("local: %v", err)
	}
	if err := ValidManagerName("Bad"); err == nil || err.Error() != `invalid manager name "Bad"` {
		t.Fatalf("Bad: %v", err)
	}
}

type tagDraft struct{ tag string }

func (o *tagDraft) BuildKitDraft(_ context.Context, _ KitDefinition, tag string, _ io.Writer) (string, error) {
	o.tag = tag
	return "id", nil
}
func (o *tagDraft) HasKitDraftImage(string) (bool, error) { return true, nil }

func TestOneDraftManager(t *testing.T) {
	b := &tagDraft{}
	m := OneDraftManager("x", b)
	if got := m.DraftManagers(); !slices.Equal(got, []string{"x"}) {
		t.Fatalf("DraftManagers = %v", got)
	}
	if id, err := m.BuildKitDraftOn(context.Background(), "x", KitDefinition{}, "t", nil); err != nil || id != "id" || b.tag != "t" {
		t.Fatalf("build: %q %v %q", id, err, b.tag)
	}
	if ok, err := m.HasKitDraftImageOn("x", "id"); err != nil || !ok {
		t.Fatalf("has: %v %v", ok, err)
	}
	if _, err := m.BuildKitDraftOn(context.Background(), "y", KitDefinition{}, "t", nil); !errors.Is(err, ErrUnknownManager) {
		t.Fatalf("other: %v", err)
	}
	if _, err := m.HasKitDraftImageOn("y", "id"); !errors.Is(err, ErrUnknownManager) {
		t.Fatalf("other has: %v", err)
	}
	if err := m.DraftManagerErr("x"); err != nil {
		t.Fatalf("classify x: %v", err)
	}
	if err := m.DraftManagerErr("y"); !errors.Is(err, ErrUnknownManager) || !strings.Contains(err.Error(), "y") {
		t.Fatalf("classify other: %v", err)
	}
}

func TestManagerActivityProgress(t *testing.T) {
	for _, c := range []struct {
		a    ManagerActivity
		want string
	}{
		{ManagerActivity{}, ""},
		{ManagerActivity{Step: "assembling"}, "assembling"},
		{ManagerActivity{Step: "building", Detail: "step 7/12"}, "building step 7/12"},
		{ManagerActivity{Detail: "web@v2"}, "web@v2"},
	} {
		if got := c.a.Progress(); got != c.want {
			t.Errorf("%+v.Progress() = %q, want %q", c.a, got, c.want)
		}
	}
}

func TestFindKitActivity(t *testing.T) {
	web2 := KitRef{ID: "web", Version: 2, Digest: "d2"}
	web3 := KitRef{ID: "web", Version: 3, Digest: "d3"}
	prep := func(id string, kit KitRef) ManagerActivity {
		return ManagerActivity{ID: id, Kind: ActivityPrepareKit, Kit: kit}
	}
	ms := []ManagerStatus{
		{Name: "a", Activities: []ManagerActivity{prep("a1", web2), {ID: "a2", Kind: ActivityCleanup}}},
		{Name: "b", Activities: []ManagerActivity{prep("b1", web3), {ID: "b2", Kind: ActivityBuildKitDraft, Kit: KitRef{ID: "app"}}}},
	}
	for _, c := range []struct {
		name    string
		kit     KitRef
		manager string
		want    string // activity id, "" = none
	}{
		{"exact on the manager", web3, "b", "b1"},
		{"the manager builds another version", web2, "b", ""},
		{"only the named manager", web3, "a", ""},
		{"unknown manager", web2, "c", ""},
		{"no manager: any", web3, "", "b1"},
		{"no manager: first by order", web2, "", "a1"},
		{"another harness (digest)", KitRef{ID: "web", Version: 2, Digest: "other"}, "", ""},
		{"another version", KitRef{ID: "web", Version: 9, Digest: "d2"}, "", ""},
		{"a draft build is not a prepare", KitRef{ID: "app"}, "", ""},
		{"no kit", KitRef{}, "", ""},
	} {
		got, ok := FindKitActivity(ms, c.kit, c.manager)
		if (c.want == "") == ok || got.ID != c.want {
			t.Errorf("%s: got %q %v, want %q", c.name, got.ID, ok, c.want)
		}
	}
}

func TestManagerActivityAge(t *testing.T) {
	t0 := time.Unix(1000, 0)
	a := ManagerActivity{Started: t0}
	for d, want := range map[time.Duration]string{-time.Second: "0s", 1500 * time.Millisecond: "1s", 125 * time.Second: "2m", 61 * time.Minute: "1h1m"} {
		if got := a.Age(t0.Add(d)); got != want {
			t.Errorf("Age(+%s) = %q, want %q", d, got, want)
		}
	}
}
