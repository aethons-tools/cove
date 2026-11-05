package colima

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/runner"
)

func argv(c runner.Call) string { return strings.Join(append([]string{c.Name}, c.Args...), " ") }

// The prune steps are context-pinned docker calls; volumes are pruned only
// with the anonymous label, so no daemon version ever removes a named volume.
func TestPrunes(t *testing.T) {
	for name, tc := range map[string]struct {
		call func(*Colima) error
		want string
	}{
		"dangling":    {(*Colima).pruneDangling, "docker --context colima-p image prune -f"},
		"volumes":     {(*Colima).pruneVolumes, "docker --context colima-p volume prune -f --filter label=com.docker.volume.anonymous"},
		"build cache": {(*Colima).pruneBuildCache, "docker --context colima-p builder prune -f"},
		"remove":      {func(c *Colima) error { return c.RemoveImage(context.Background(), "sha256:abc") }, "docker --context colima-p image rm sha256:abc"},
	} {
		f := &runner.Fake{}
		if err := tc.call(NewForProfile(f, "p")); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(f.Calls) != 1 || argv(f.Calls[0]) != tc.want {
			t.Errorf("%s: calls = %+v, want %q", name, f.Calls, tc.want)
		}
		if strings.Contains(argv(f.Calls[0]), "--all") || strings.Contains(argv(f.Calls[0]), " -a") {
			t.Errorf("%s: prunes all: %q", name, argv(f.Calls[0]))
		}
	}
}

func (c *Colima) pruneDangling() error   { return c.PruneDanglingImages(context.Background()) }
func (c *Colima) pruneVolumes() error    { return c.PruneVolumes(context.Background()) }
func (c *Colima) pruneBuildCache() error { return c.PruneBuildCache(context.Background()) }

func TestPruneErrorCarriesStderr(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stderr: "Error: daemon busy\n", Err: errors.New("exit 1")}}}
	err := NewForProfile(f, "p").PruneBuildCache(context.Background())
	if err == nil || !strings.Contains(err.Error(), "daemon busy") {
		t.Fatalf("err = %v", err)
	}
}

const imagesOut = `{"ID":"sha256:aaa","Repository":"jam-kit","Tag":"d1-a1","CreatedAt":"2026-11-01 10:00:00 +0000 UTC","Size":"1.5GB"}
{"ID":"sha256:aaa","Repository":"jam-kit","Tag":"d1-a2","CreatedAt":"2026-11-01 10:00:00 +0000 UTC","Size":"1.5GB"}
{"ID":"sha256:bbb","Repository":"jam-kit-base","Tag":"b1","CreatedAt":"2026-10-01 09:30:00 +0200 CEST","Size":"900MB"}
{"ID":"sha256:ccc","Repository":"jam-kit-draft","Tag":"t1","CreatedAt":"2026-11-04 10:00:00 +0000 UTC","Size":"1GB"}
{"ID":"sha256:ddd","Repository":"jam-kit","Tag":"d2-a1","CreatedAt":"2026-11-02 10:00:00 +0000 UTC","Size":"1GB"}
{"ID":"sha256:ddd","Repository":"myorg/keep","Tag":"latest","CreatedAt":"2026-11-02 10:00:00 +0000 UTC","Size":"1GB"}
{"ID":"sha256:eee","Repository":"ubuntu","Tag":"24.04","CreatedAt":"2026-09-02 10:00:00 +0000 UTC","Size":"80MB"}
{"ID":"sha256:fff","Repository":"<none>","Tag":"<none>","CreatedAt":"2026-09-02 10:00:00 +0000 UTC","Size":"80MB"}
{"ID":"sha256:ggg","Repository":"jam-kitchen","Tag":"x","CreatedAt":"2026-09-02 10:00:00 +0000 UTC","Size":"80MB"}
`

// KitImages lists the images only Jam kit tags name (one row per tag in
// docker's output), marking those a container uses.
func TestKitImages(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{
		{Stdout: imagesOut},
		{Stdout: "c1\nc2\n"},
		{Stdout: "sha256:ccc\nsha256:eee\n"},
	}}
	got, err := NewForProfile(f, "p").KitImages(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cest := time.FixedZone("CEST", 2*3600)
	want := []jam.KitImage{
		{ID: "sha256:aaa", Tags: []string{"jam-kit:d1-a1", "jam-kit:d1-a2"}, Created: time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC), Size: 1_500_000_000},
		{ID: "sha256:bbb", Tags: []string{"jam-kit-base:b1"}, Created: time.Date(2026, 10, 1, 9, 30, 0, 0, cest), Size: 900_000_000},
		{ID: "sha256:ccc", Tags: []string{"jam-kit-draft:t1"}, Created: time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC), Size: 1_000_000_000, InUse: true},
	}
	if len(got) != len(want) {
		t.Fatalf("KitImages = %+v", got)
	}
	for i := range want {
		if got[i].ID != want[i].ID || !reflect.DeepEqual(got[i].Tags, want[i].Tags) || !got[i].Created.Equal(want[i].Created) || got[i].Size != want[i].Size || got[i].InUse != want[i].InUse {
			t.Errorf("[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	calls := []string{
		"docker --context colima-p image ls --no-trunc --format {{json .}}",
		"docker --context colima-p ps -a -q --no-trunc",
		"docker --context colima-p container inspect --format {{.Image}} c1 c2",
	}
	if len(f.Calls) != len(calls) {
		t.Fatalf("calls = %+v", f.Calls)
	}
	for i, w := range calls {
		if a := argv(f.Calls[i]); a != w {
			t.Errorf("call %d = %q, want %q", i, a, w)
		}
	}
}

// With no containers, nothing is in use and nothing is inspected.
func TestKitImagesNoContainers(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: imagesOut}, {Stdout: "\n"}}}
	got, err := NewForProfile(f, "p").KitImages(context.Background())
	if err != nil || len(got) != 3 || got[2].InUse {
		t.Fatalf("KitImages = %+v, %v", got, err)
	}
	if len(f.Calls) != 2 {
		t.Fatalf("calls = %+v", f.Calls)
	}
}

// A container removed between the listing and the inspect (a --rm studio
// exiting) is skipped; the rest still count.
func TestKitImagesContainerVanishes(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{
		{Stdout: imagesOut},
		{Stdout: "c1\nc2\n"},
		{Stdout: "sha256:ccc\n", Stderr: "Error: No such container: c2\n", Err: errors.New("exit 1")},
	}}
	got, err := NewForProfile(f, "p").KitImages(context.Background())
	if err != nil || len(got) != 3 || !got[2].InUse {
		t.Fatalf("KitImages = %+v, %v", got, err)
	}
}

func TestKitImagesErrors(t *testing.T) {
	for name, outs := range map[string][]runner.FakeResult{
		"ls fails":      {{Err: errors.New("exit 1")}},
		"bad json":      {{Stdout: "nope\n"}},
		"bad created":   {{Stdout: `{"ID":"x","Repository":"jam-kit","Tag":"t","CreatedAt":"yesterday","Size":"1GB"}` + "\n"}},
		"bad size":      {{Stdout: `{"ID":"x","Repository":"jam-kit","Tag":"t","CreatedAt":"2026-11-01 10:00:00 +0000 UTC","Size":"big"}` + "\n"}},
		"ps fails":      {{Stdout: imagesOut}, {Err: errors.New("exit 1")}},
		"inspect fails": {{Stdout: imagesOut}, {Stdout: "c1\n"}, {Err: errors.New("exit 1")}},
		"inspect other": {{Stdout: imagesOut}, {Stdout: "c1\nc2\n"}, {Stdout: "sha256:ccc\n", Stderr: "Error: No such container: c2\nError: daemon busy\n", Err: errors.New("exit 1")}},
	} {
		f := &runner.Fake{Outputs: outs}
		if _, err := NewForProfile(f, "p").KitImages(context.Background()); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
