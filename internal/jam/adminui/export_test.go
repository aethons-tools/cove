package adminui

import (
	"log/slog"
	"net/http"

	"github.com/aethons-tools/cove/internal/jam"
)

// RegisterAppForTest is Handler's mux without the config-write routing: a
// config write is served on store itself. Only for tests of a write
// handler's own validation; every other test writes on a branch.
func RegisterAppForTest(store jam.Store, log *slog.Logger, sup *jam.Supervisor, alloc jam.SessionAllocator, credExists func(string) bool, msgs SquawkReader, opts ...Option) http.Handler {
	mux, _ := newMux(store, log, sup, alloc, credExists, msgs, opts)
	return mux
}
