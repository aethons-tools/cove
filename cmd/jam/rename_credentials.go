package main

import (
	"log/slog"
	"path/filepath"
	"slices"

	"github.com/aethons-tools/cove/internal/legacynames"
	"github.com/aethons-tools/cove/internal/secret"
)

// warnPreRenameCredentialCommands warns, once and with credential names
// only, about supplied credential commands that still run the pre-rename
// CLI or minter binary (removed by the rename: the command would fail when
// the credential is first resolved).
func warnPreRenameCredentialCommands(specs map[string]secret.Spec, log *slog.Logger) {
	var names []string
	for name, s := range specs {
		if len(s.Command) == 0 {
			continue
		}
		if b := filepath.Base(s.Command[0]); b == legacynames.MintBinary || b == legacynames.JamBinary {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return
	}
	slices.Sort(names)
	log.Warn("jam rename: credential commands still call "+legacynames.MintBinary+" or "+legacynames.JamBinary+
		`, which the rename removed; use ["jam","mint",...] with JAM_MINT_* env (docs/usage/jam/renamed-from-at.md)`, "credentials", names)
}
