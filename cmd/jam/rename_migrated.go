package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/legacynames"
	"github.com/aethons-tools/cove/internal/secret"
)

// renameMigratedFile, in the state dir, records that the jam rename's studio
// migration (docs/usage/jam/renamed-from-at.md) completed once against a
// docker context (the file's content), so later serve starts skip it and do
// not need docker reachable.
const renameMigratedFile = "rename-migrated"

func renameMigratedContent(dockerContext string) string {
	return "docker-context=" + dockerContext + "\n"
}

// migrateStudioNamesOnce runs jam.MigrateStudioNames unless the state dir
// records it completed for dockerContext. Until it has completed, it fails
// closed (docker unreachable included); the record is written last, only
// after a fully successful run. A record that cannot be written is logged:
// the migration is idempotent, so the next start just runs it again.
func migrateStudioNamesOnce(ctx context.Context, stateDir, dockerContext string, st jam.Store, r jam.StudioRenamer, log *slog.Logger) error {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	path := filepath.Join(stateDir, renameMigratedFile)
	b, err := os.ReadFile(path)
	if err == nil && string(b) == renameMigratedContent(dockerContext) {
		return nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("jam rename: reading the studio migration record: %w", err)
	}
	if _, err := jam.MigrateStudioNames(ctx, st, r, log); err != nil {
		return fmt.Errorf("jam rename: the studio migration must complete once before serve starts (docs/usage/jam/renamed-from-at.md): %w", err)
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		log.Warn("jam rename: recording the studio migration failed; it runs again next start", "error", err.Error())
		return nil
	}
	if err := writeFileAtomic(path, []byte(renameMigratedContent(dockerContext))); err != nil {
		log.Warn("jam rename: recording the studio migration failed; it runs again next start", "error", err.Error())
	}
	return nil
}

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
