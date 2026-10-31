package configrepo

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/filemode"

	"github.com/aethons-tools/cove/internal/jam/gitsource/gittest"
)

func put(p, data string) Changes {
	return Changes{Put: map[string]File{p: {Data: []byte(data), Mode: filemode.Regular}}}
}

// branched is a repo with a.yml and b.yml on main and a branch jam/alice at main.
func branched(t *testing.T) (*Repo, string, func(map[string]string) string, string) {
	t.Helper()
	r, url, update := served(t, map[string]string{"a.yml": "a\n", "b.yml": "b\n"})
	h, err := r.CreateBranch(ctx, "jam/alice")
	if err != nil {
		t.Fatal(err)
	}
	return r, url, update, h
}

func noCheck(map[string]File) error { return nil }

func TestMergeFastForward(t *testing.T) {
	r, url, _, h0 := branched(t)
	h1, _ := r.Commit(ctx, "jam/alice", h0, put("a.yml", "A\n"), alice, "a")
	var checked map[string]File
	res, err := r.Merge(ctx, "jam/alice", h1, alice, "merge", func(f map[string]File) error { checked = f; return nil })
	if err != nil || !res.FastForward || res.Head != h1 || gittest.Head(t, url, "main") != h1 {
		t.Fatalf("ff = %+v %v", res, err)
	}
	if string(checked["a.yml"].Data) != "A\n" {
		t.Fatalf("checked %v", checked)
	}
}

func TestMergeCommit(t *testing.T) {
	r, url, update, h0 := branched(t)
	h1, _ := r.Commit(ctx, "jam/alice", h0, put("a.yml", "A\n"), alice, "a")
	update(map[string]string{"b.yml": "B\n"}) // main moves on, another file
	res, err := r.Merge(ctx, "jam/alice", h1, alice, "merge jam/alice", noCheck)
	if err != nil || res.FastForward || res.NoOp || gittest.Head(t, url, "main") != res.Head || gittest.Head(t, url, "jam/alice") != res.Head {
		t.Fatalf("merge = %+v %v", res, err)
	}
	if parents := strings.Fields(gittest.Git(t, url, "log", "-1", "--format=%P", "main")); len(parents) != 2 || parents[1] != h1 {
		t.Fatalf("parents %v", parents)
	}
	for p, want := range map[string]string{"a.yml": "A\n", "b.yml": "B\n"} {
		if got, _ := gittest.Show(t, url, "main", p); got != want {
			t.Errorf("%s = %q", p, got)
		}
	}
}

func TestMergeConflicts(t *testing.T) {
	for name, tc := range map[string]struct {
		ours   Changes
		theirs map[string]string
		gitRm  bool
		want   []string
	}{
		"same file":      {ours: put("a.yml", "ours\n"), theirs: map[string]string{"a.yml": "theirs\n"}, want: []string{"a.yml"}},
		"delete vs edit": {ours: Changes{Delete: []string{"a.yml"}}, theirs: map[string]string{"a.yml": "theirs\n"}, want: []string{"a.yml"}},
		"same change":    {ours: put("a.yml", "same\n"), theirs: map[string]string{"a.yml": "same\n"}},
	} {
		t.Run(name, func(t *testing.T) {
			r, url, update, h0 := branched(t)
			h1, err := r.Commit(ctx, "jam/alice", h0, tc.ours, alice, "ours")
			if err != nil {
				t.Fatal(err)
			}
			update(tc.theirs)
			before := gittest.Head(t, url, "main")
			_, err = r.Merge(ctx, "jam/alice", h1, alice, "merge", noCheck)
			var ce *ConflictError
			if tc.want == nil {
				if err != nil {
					t.Fatalf("merge = %v", err)
				}
				return
			}
			if !errors.As(err, &ce) || !reflect.DeepEqual(ce.Paths, tc.want) || err.Error() != "merge conflict in: a.yml" {
				t.Fatalf("err = %v", err)
			}
			if gittest.Head(t, url, "main") != before {
				t.Fatal("a conflicting merge pushed")
			}
		})
	}
}

func TestMergeAlreadyMergedIsNoOp(t *testing.T) {
	r, url, update, h0 := branched(t)
	update(map[string]string{"b.yml": "B\n"})
	res, err := r.Merge(ctx, "jam/alice", h0, alice, "merge", noCheck)
	if err != nil || !res.NoOp || res.Head != gittest.Head(t, url, "main") {
		t.Fatalf("no-op = %+v %v", res, err)
	}
}

func TestMergeCheckErrorPushesNothing(t *testing.T) {
	r, url, _, h0 := branched(t)
	h1, _ := r.Commit(ctx, "jam/alice", h0, put("a.yml", "A\n"), alice, "a")
	boom := errors.New("plan refused")
	if _, err := r.Merge(ctx, "jam/alice", h1, alice, "merge", func(map[string]File) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if gittest.Head(t, url, "main") != h0 {
		t.Fatal("main moved")
	}
}

func TestMergeStale(t *testing.T) {
	r, _, _, h0 := branched(t)
	if _, err := r.Commit(ctx, "jam/alice", h0, put("a.yml", "A\n"), alice, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Merge(ctx, "jam/alice", h0, alice, "merge", noCheck); !errors.Is(err, ErrStale) {
		t.Fatalf("err = %v", err)
	}
}
