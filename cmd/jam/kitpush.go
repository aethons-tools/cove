package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/studio"
)

var errKitPushUsage = errors.New("expected <dir> or --config <file> (- for stdin)")

// kitPushFiles reads what `kit push` sends and the name it goes under: the
// files of <dir> (its .dockerignore applied, symlinks refused) named --name,
// else kit.yml's legacy name:, else the dir's base name; or, with --config,
// the one file kit.yml, named --name or its legacy name:.
func kitPushFiles(pos []string, config, name string) ([]kittree.File, string, error) {
	switch {
	case len(pos) == 1 && config == "":
		files, err := readKitDir(pos[0])
		if err != nil {
			return nil, "", err
		}
		if name == "" {
			abs, _ := filepath.Abs(pos[0])
			name = filepath.Base(abs)
		}
		return files, name, nil
	case len(pos) == 0 && config != "":
		data, err := readConfig(config)
		if err != nil {
			return nil, "", err
		}
		return []kittree.File{{Path: kittree.KitFile, Mode: 0o644, Data: data}}, name, nil
	}
	return nil, "", errKitPushUsage
}

// readKitDir reads a kit directory as `kit push` and `kit draft sync` send it
// (.dockerignore applied, symlinks and irregular files refused); errors are
// prefixed with dir.
func readKitDir(dir string) ([]kittree.File, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("%s: %s", dir, openDirError(dir, err))
	}
	defer root.Close()
	files, err := kittree.ReadDir(root, ".")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", dir, err)
	}
	return files, nil
}

// openDirError says, in fixed text, why <dir> could not be opened; the OS
// error itself is not shown.
func openDirError(dir string, err error) string {
	if errors.Is(err, fs.ErrNotExist) {
		return "no such directory"
	}
	if fi, serr := os.Stat(dir); serr == nil && !fi.IsDir() {
		return "not a directory"
	}
	return "cannot open the directory"
}

// checkKitPush validates files client-side as push would: .dockerignore, then
// the one ingest pipeline (studio.ParseFiles: the tree, no packed context,
// kit.yml parsed with its note files), the name and the authoring budgets.
// The legacy name: wins over a defaulted dir name.
func checkKitPush(files []kittree.File, name string, nameGiven bool) (string, error) {
	files, err := kittree.Ignore(files)
	if err != nil {
		return "", err
	}
	_, _, sk, err := studio.ParseFiles(files)
	if err != nil {
		if _, isTree := errors.AsType[*studio.TreeError](err); isTree {
			return "", err
		}
		return "", fmt.Errorf("invalid studio kit config: %w", err)
	}
	if !nameGiven && sk.LegacyName != "" {
		name = sk.LegacyName
	}
	if name == "" {
		return "", errors.New("--name is required with --config")
	}
	for _, check := range []func() error{func() error { return sk.CheckName(name) }, sk.CheckPrompt, sk.CheckNotes} {
		if err := check(); err != nil {
			return "", err
		}
	}
	return name, nil
}
