package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/legacynames"
	"github.com/aethons-tools/cove/internal/secret"
)

// dockerRenamer is the studio-name migration's launcher surface over a
// docker host that may be unreachable (colima down after a reboot).
type dockerRenamer struct {
	down  bool
	calls int
}

var errDockerDown = errors.New("Cannot connect to the Docker daemon")

func (d *dockerRenamer) RetireLegacyContainers(context.Context, []string) ([]string, error) {
	d.calls++
	if d.down { // as the launcher reports docker's FindContainers failing
		return nil, fmt.Errorf("migration: list pre-rename containers: %w", errDockerDown)
	}
	return nil, nil
}
func (d *dockerRenamer) RetireLegacyStudio(context.Context, string) error { return nil }
func (d *dockerRenamer) MigrateStateVolumes(context.Context, []string) ([]string, error) {
	if d.down {
		return nil, errDockerDown
	}
	return nil, nil
}
func (d *dockerRenamer) StudioLocation(id string) string { return "jam-studio-" + id }

const wantDockerDownText = "jam rename: the studio migration must complete once before serve starts (docs/usage/jam/renamed-from-at.md): " +
	"migration: list pre-rename containers: Cannot connect to the Docker daemon"

// Before the migration has completed once, serve start needs docker and
// fails closed with the fixed text; nothing is recorded.
func TestStudioRenameMigrationFirstStartNeedsDocker(t *testing.T) {
	dir := t.TempDir()
	r := &dockerRenamer{down: true}
	err := migrateStudioNamesOnce(context.Background(), dir, "colima-jam", jam.NewMemStore(), r, nil)
	if err == nil || err.Error() != wantDockerDownText {
		t.Fatalf("err = %v, want %q", err, wantDockerDownText)
	}
	if _, err := os.Stat(filepath.Join(dir, renameMigratedFile)); !os.IsNotExist(err) {
		t.Fatalf("no completion may be recorded after a failed migration: %v", err)
	}
}

// After one fully successful migration, later starts skip the studio
// migration: they succeed with docker unreachable.
func TestStudioRenameMigrationLaterStartsSkipDocker(t *testing.T) {
	dir := t.TempDir()
	st := jam.NewMemStore()
	r := &dockerRenamer{}
	if err := migrateStudioNamesOnce(context.Background(), dir, "colima-jam", st, r, nil); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, renameMigratedFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("completion marker: %v %v", info, err)
	}
	r.down, r.calls = true, 0
	if err := migrateStudioNamesOnce(context.Background(), dir, "colima-jam", st, r, nil); err != nil {
		t.Fatalf("second start with docker down: %v", err)
	}
	if r.calls != 0 {
		t.Fatalf("the studio migration ran again (%d docker calls)", r.calls)
	}
	// Another docker context was never migrated: it runs (and fails closed).
	if err := migrateStudioNamesOnce(context.Background(), dir, "other", st, r, nil); err == nil || err.Error() != wantDockerDownText {
		t.Fatalf("other context: err = %v", err)
	}
}

// The state dir is created (0700) when the marker is first written.
func TestStudioRenameMigrationCreatesTheStateDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state", "jam")
	if err := migrateStudioNamesOnce(context.Background(), dir, "", jam.NewMemStore(), &dockerRenamer{}, nil); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("state dir: %v %v", info, err)
	}
}

// A credential command still calling a pre-rename binary is warned about at
// serve start: fixed text, credential names only (never argv or values).
func TestWarnPreRenameCredentialCommands(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	warnPreRenameCredentialCommands(map[string]secret.Spec{
		"gh":     {Name: "gh", Command: []string{"/usr/local/bin/" + legacynames.MintBinary, "github", "--secret-arg"}},
		"old":    {Name: "old", Command: []string{legacynames.JamBinary, "mint", "anthropic"}},
		"ok":     {Name: "ok", Command: []string{"jam", "mint", "github"}},
		"lit":    {Name: "lit", Value: legacynames.MintBinary, Literal: true},
		"op":     {Name: "op", Command: []string{"op", "read", legacynames.MintBinary}},
		"absent": {Name: "absent"},
	}, log)
	out := buf.String()
	if strings.Count(out, "level=WARN") != 1 ||
		!strings.Contains(out, `msg="jam rename: credential commands still call `+legacynames.MintBinary+" or "+legacynames.JamBinary+`, which the rename removed; use [\"jam\",\"mint\",...] with JAM_MINT_* env (docs/usage/jam/renamed-from-at.md)"`) ||
		!strings.Contains(out, "credentials=\"[gh old]\"") {
		t.Fatalf("log = %s", out)
	}
	if strings.Contains(out, "secret-arg") || strings.Contains(out, "github") {
		t.Fatalf("argv leaked: %s", out)
	}
	buf.Reset()
	warnPreRenameCredentialCommands(map[string]secret.Spec{"ok": {Name: "ok", Command: []string{"jam", "mint"}}}, log)
	if buf.Len() != 0 {
		t.Fatalf("no warning expected: %s", buf.String())
	}
}
