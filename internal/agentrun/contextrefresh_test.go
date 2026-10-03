package agentrun

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

type seqContext struct {
	bundles []sessionctx.Bundle
	errs    []error
	i       int
}

func (s *seqContext) Fetch(context.Context) (sessionctx.Bundle, error) {
	i := min(s.i, len(s.bundles)-1)
	s.i++
	if i < len(s.errs) && s.errs[i] != nil {
		return sessionctx.Bundle{}, s.errs[i]
	}
	return s.bundles[i], nil
}

func compileRole(core string) sessionctx.Bundle {
	return sessionctx.Compile(sessionctx.Inputs{Session: sessionctx.SessionFacts{Kind: "standing", Name: "n", Project: "p", Role: "r"}, Role: sessionctx.Layer{Core: core}})
}

func TestContextRefresher(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "context")
	first := compileRole("ONE")
	if err := writeContext(dir, first); err != nil {
		t.Fatal(err)
	}
	src := &seqContext{bundles: []sessionctx.Bundle{first, compileRole("TWO"), compileRole("TWO"), compileRole("THREE")}, errs: []error{nil, nil, nil, errors.New("jam down")}}
	r := newContextRefresher(src, first, dir, nil)
	if got := r.refresh(context.Background()); got != nil {
		t.Fatalf("unchanged: %v", got)
	}
	if got := r.refresh(context.Background()); strings.Join(got, ",") != "role" {
		t.Fatalf("changed: %v", got)
	}
	if core, _ := os.ReadFile(filepath.Join(dir, "CORE.md")); !strings.Contains(string(core), "TWO") {
		t.Fatalf("files not rewritten: %s", core)
	}
	if got := r.refresh(context.Background()); got != nil {
		t.Fatalf("same again: %v", got)
	}
	if got := r.refresh(context.Background()); got != nil {
		t.Fatalf("fetch error must keep the last bundle silently: %v", got)
	}
	if core, _ := os.ReadFile(filepath.Join(dir, "CORE.md")); !strings.Contains(string(core), "TWO") {
		t.Fatal("a failed fetch must not touch the files")
	}
}

// A notice written into a live episode is repeated at the next episode, in
// case that turn never ran (write failed / the process exited first).
func TestContextRefresherCarriesLiveNoticeToNextEpisode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "context")
	one := compileRole("ONE")
	if err := writeContext(dir, one); err != nil {
		t.Fatal(err)
	}
	r := newContextRefresher(&seqContext{bundles: []sessionctx.Bundle{compileRole("TWO")}}, one, dir, nil)
	if got := r.live(context.Background()); strings.Join(got, ",") != "role" {
		t.Fatalf("live: %v", got)
	}
	if got := r.episode(context.Background()); strings.Join(got, ",") != "role" {
		t.Fatalf("next episode must repeat the live change: %v", got)
	}
	if got := r.episode(context.Background()); got != nil {
		t.Fatalf("then nothing: %v", got)
	}
}

type countingContext struct {
	calls int
	err   error
	block bool
}

func (c *countingContext) Fetch(ctx context.Context) (sessionctx.Bundle, error) {
	c.calls++
	if c.block {
		<-ctx.Done()
		return sessionctx.Bundle{}, ctx.Err()
	}
	return sessionctx.Bundle{}, c.err
}

// An older Jam has no /context: stop asking after the first 404.
func TestContextRefresherStopsOnNoEndpoint(t *testing.T) {
	src := &countingContext{err: ErrNoContextEndpoint}
	r := newContextRefresher(src, compileRole("ONE"), filepath.Join(t.TempDir(), "context"), nil)
	for range 3 {
		if got := r.episode(context.Background()); got != nil {
			t.Fatalf("no endpoint: %v", got)
		}
	}
	if src.calls != 1 {
		t.Fatalf("fetched %d times; want 1", src.calls)
	}
}

// The wake path must not stall the episode loop on a slow Jam.
func TestContextRefresherLiveTimeout(t *testing.T) {
	r := newContextRefresher(&countingContext{block: true}, compileRole("ONE"), filepath.Join(t.TempDir(), "context"), nil)
	r.liveTimeout = 30 * time.Millisecond
	start := time.Now()
	if got := r.live(context.Background()); got != nil {
		t.Fatalf("timed out fetch: %v", got)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("live refresh took %v", d)
	}
}
