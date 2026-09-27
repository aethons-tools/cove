package logging

import (
	"io"
	"log/slog"
	"os"
	"sync"
)

// RenameDoc is the doc every Harbor → Jam deprecation warning points at: it
// lists each old name, its new name, and whether the old one is still accepted.
const RenameDoc = "docs/usage/jam/renamed-from-harbor.md"

var deprecations sync.Map // old name → struct{}; warned already this process

// Deprecated logs a WARN diagnostic to stderr that the name old is deprecated
// in favour of new, pointing at RenameDoc. It fires at most once per old name
// per process, so an alias hit in a loop (or by several commands) warns once.
// The mode follows AT_LOG_MODE (auto-detect by default), like the binaries'
// other diagnostics: human text on a TTY, a JSON record otherwise. It never
// logs values — only the two names.
func Deprecated(stderr io.Writer, old, new string) {
	if _, warned := deprecations.LoadOrStore(old, struct{}{}); warned {
		return
	}
	lg, err := New(Options{Mode: ModeFrom(os.Getenv("AT_LOG_MODE")), Stderr: stderr, Level: slog.LevelWarn})
	if err != nil { // unreachable without a FilePath; never block on a warning
		return
	}
	lg.Warn("deprecated name: "+old+" is now "+new+" (the old name still works for now)",
		slog.String("old", old), slog.String("new", new), slog.String("see", RenameDoc))
}

// ResetDeprecations forgets which deprecations have warned, so a test can
// assert on a warning another test in the same process already triggered.
func ResetDeprecations() {
	deprecations.Range(func(k, _ any) bool { deprecations.Delete(k); return true })
}
