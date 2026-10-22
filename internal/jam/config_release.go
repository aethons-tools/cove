package jam

import (
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/aethons-tools/cove/internal/jam/condition"
)

// ReleaseConfigMark clears key's managed mark: the object stays as it is,
// unmanaged — the next plan lists it as unmanaged, or adopts or refuses it
// if the repo still holds it. An unknown kind is 400, an object with no mark
// 404. It holds configMu (no apply interleaves), then re-checks config.drift
// (t nil: none).
func ReleaseConfigMark(store Store, key ConfigKey, t *condition.Tracker, log *slog.Logger, actor string) error {
	if !slices.Contains(configKindOrder, key.Kind) {
		return writeErr(http.StatusBadRequest, "config release: unknown kind (want one of %s)", strings.Join(configKindOrder, ", "))
	}
	configMu.Lock()
	defer configMu.Unlock()
	if !slices.ContainsFunc(store.ListConfigMarks(), func(m ConfigMark) bool { return m.Key() == key }) {
		return writeErr(http.StatusNotFound, "config release: %s %s is not managed", key.Kind, clipName(key.Name))
	}
	if err := store.ApplyConfig(ConfigWrite{RemoveMarks: []ConfigKey{key}}); err != nil {
		return fmt.Errorf("config release: %w", err)
	}
	if log != nil {
		log.Info("config released", "operator", actor, "kind", key.Kind, "name", key.Name)
	}
	checkConfigDriftLocked(store, t, log)
	return nil
}
