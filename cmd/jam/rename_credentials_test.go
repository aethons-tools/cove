package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/legacynames"
	"github.com/aethons-tools/cove/internal/secret"
)

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
