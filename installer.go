// Package cove is the module root. It exists only to embed repo-root assets
// that must ship *inside* the built binaries.
//
// Currently that is install.sh — the one-command installer. `at-cove update`
// (COV-128) re-runs it to fetch → verify → replace the latest release, so the
// script has to travel inside the binary: a user who installed via
// `curl | bash` has no repo checkout to read it from, and re-fetching the
// script over the network at update time would defeat the point of shipping a
// self-contained updater. Embedding it here (rather than copying it under a
// package dir) keeps a single source of truth — the exact install.sh advertised
// for `curl | bash` is the one baked into at-cove.
package cove

import _ "embed"

// InstallScript is the embedded install.sh. internal/update materializes it to a
// temp file and drives it via the Runner; see cmd/at-cove doUpdate.
//
//go:embed install.sh
var InstallScript []byte
