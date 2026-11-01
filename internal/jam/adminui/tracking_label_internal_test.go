package adminui

import (
	"log/slog"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/gitsource"
)

// A git config repo (ssh or a local path) does not re-parse as a kit
// source: the page shows it by the serve's label, not "invalid".
func TestBuildConfigPageFollowedLabel(t *testing.T) {
	env := jam.ConfigEnv{Followed: gitsource.Source{Repo: "git@h:x.git", Ref: "main"}, FollowedLabel: "git@h:x.git@main"}
	d := buildConfigPage(jam.NewMemStore(), env, true, slog.New(slog.DiscardHandler))
	if d.Source == nil || d.Source.Label != env.FollowedLabel || d.Source.Repo != "git@h:x.git" || d.Source.RepoLink != "" {
		t.Fatalf("source = %+v", d.Source)
	}
	env.FollowedLabel = ""
	if d := buildConfigPage(jam.NewMemStore(), env, true, slog.New(slog.DiscardHandler)); d.Source.Label != "invalid" {
		t.Fatalf("unlabelled source = %+v", d.Source)
	}
}
