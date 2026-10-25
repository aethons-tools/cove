package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/kittree/kittreetest"
)

func TestWriteConfigTreeMirrorsKitDirs(t *testing.T) {
	dir := t.TempDir()
	for p, body := range map[string]string{"kits/app/stale.txt": "old", "kits/app/debug.log": "ignored", "kits/gone/kit.yml": "kind: studio\n", "kits/gone/x": "x"} {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(dir, "kits/app/leak")); err != nil {
		t.Fatal(err)
	}
	exp := jam.ConfigTreeExport{Files: map[string]string{}, Kits: map[string][]jam.ConfigKitFile{
		"kits/app":  {{Path: "kit.yml", Data: []byte("kind: studio\n"), Mode: 0o644}, {Path: "run.sh", Data: []byte("echo"), Mode: 0o755}, {Path: ".dockerignore", Data: []byte("*.log\n"), Mode: 0o644}},
		"kits/gone": {},
	}}
	if err := writeConfigTree(dir, exp); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]bool{"kits/app/kit.yml": true, "kits/app/run.sh": true, "kits/app/debug.log": true, "kits/app/stale.txt": false, "kits/app/leak": false, "kits/gone": false, "kits": true} {
		if _, err := os.Lstat(filepath.Join(dir, p)); (err == nil) != want {
			t.Fatalf("%s present=%v, want %v", p, err == nil, want)
		}
	}
	if fi, _ := os.Stat(filepath.Join(dir, "kits/app/run.sh")); fi.Mode().Perm() != 0o755 {
		t.Fatalf("run.sh mode %v", fi.Mode())
	}
	if b, err := os.ReadFile(victim); err != nil || string(b) != "keep me" {
		t.Fatalf("the symlink target must never be touched: %q %v", b, err)
	}
	for _, bad := range []jam.ConfigTreeExport{
		{Kits: map[string][]jam.ConfigKitFile{"kits/../x": {}}},
		{Kits: map[string][]jam.ConfigKitFile{"kits/a": {{Path: "../x", Mode: 0o644}}}},
		{Kits: map[string][]jam.ConfigKitFile{"kits/a": {{Path: "x", Mode: 0o777}}}},
	} {
		if err := writeConfigTree(dir, bad); err == nil {
			t.Fatalf("writeConfigTree(%+v) must refuse", bad.Kits)
		}
	}
}

// writeConfigTree writes kit files byte for byte at their modes, past the
// umask and over an existing file's mode.
func TestWriteConfigTreeKitModes(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "kits/k/run.sh"), "old", 0o600)
	blob := []byte{0xff, 0x00, 0xfe, 0x80}
	exp := jam.ConfigTreeExport{Kits: map[string][]jam.ConfigKitFile{"kits/k": {
		{Path: "kit.yml", Data: []byte("kind: studio\n"), Mode: 0o644},
		{Path: "run.sh", Data: []byte("#!/bin/sh\n"), Mode: 0o755},
		{Path: "lib/blob.bin", Data: blob, Mode: 0o644},
	}}}
	if err := writeConfigTree(dir, exp); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]os.FileMode{"kits/k/run.sh": 0o755, "kits/k/lib/blob.bin": 0o644, "kits/k/kit.yml": 0o644} {
		fi, err := os.Stat(filepath.Join(dir, p))
		if err != nil || fi.Mode().Perm() != want {
			t.Errorf("%s: %v %v, want %o", p, fi, err, want)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "kits/k/lib/blob.bin")); !bytes.Equal(b, blob) {
		t.Fatalf("blob %x", b)
	}
}

// A mirror removes only files under the kit dirs the export names: other
// kits, files beside kits/ and empty subdirs it leaves are handled right.
func TestWriteConfigTreeMirrorTouchesOnlyItsDirs(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"kits/k/stale.txt", "kits/k/sub/old.bin", "kits/k/keep", "kits/other/kit.yml", "kits/other.source.yml", "README.md"} {
		mustWrite(t, filepath.Join(dir, p), "x", 0o644)
	}
	exp := jam.ConfigTreeExport{Kits: map[string][]jam.ConfigKitFile{
		"kits/k":    {{Path: "kit.yml", Data: []byte("kind: studio\n"), Mode: 0o644}, {Path: "keep", Data: []byte("new"), Mode: 0o644}},
		"kits/none": {},
	}}
	if err := writeConfigTree(dir, exp); err != nil {
		t.Fatal(err)
	}
	got := readTree(t, dir)
	want := map[string]string{"kits/k/kit.yml": "kind: studio\n", "kits/k/keep": "new", "kits/other/kit.yml": "x", "kits/other.source.yml": "x", "README.md": "x"}
	if len(got) != len(want) {
		t.Fatalf("tree %v", got)
	}
	for p, body := range want {
		if got[p] != body {
			t.Errorf("%s = %q, want %q", p, got[p], body)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "kits/k/sub")); !os.IsNotExist(err) {
		t.Errorf("empty dir left: %v", err)
	}
}

// A symlinked kits/ or kits/<n> is refused before anything is written, so
// neither a write nor a mirror follows it out of the tree.
func TestWriteConfigTreeRefusesSymlinkedKitDirs(t *testing.T) {
	for _, link := range []string{"kits", "kits/k"} {
		dir, outside := t.TempDir(), t.TempDir()
		mustWrite(t, filepath.Join(outside, "victim"), "x", 0o644)
		mustMkdir(t, filepath.Join(dir, "kits"))
		if link == "kits" {
			cfgMust(t, os.Remove(filepath.Join(dir, "kits")))
		}
		if err := os.Symlink(outside, filepath.Join(dir, link)); err != nil {
			t.Fatal(err)
		}
		exp := jam.ConfigTreeExport{Files: map[string]string{"marker": "m"}, Kits: map[string][]jam.ConfigKitFile{"kits/k": {{Path: "new", Data: []byte("x"), Mode: 0o644}}}}
		err := writeConfigTree(dir, exp)
		if err == nil || !strings.HasPrefix(err.Error(), "refusing to") {
			t.Fatalf("%s: err %v", link, err)
		}
		if _, serr := os.Stat(filepath.Join(outside, "victim")); serr != nil {
			t.Fatalf("%s: mirrored outside the tree", link)
		}
		for _, p := range []string{filepath.Join(outside, "new"), filepath.Join(outside, "k/new"), filepath.Join(dir, "marker")} {
			if _, serr := os.Stat(p); !os.IsNotExist(serr) {
				t.Fatalf("%s: wrote %s", link, p)
			}
		}
	}
}

// A mirror keeps what the exported .dockerignore excludes: Jam never stored
// those files, so it says nothing about them.
func TestWriteConfigTreeMirrorKeepsDockerignored(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"kits/k/build.log", "kits/k/node_modules/x/y.js", "kits/k/stale.txt", "kits/k/keep.log"} {
		mustWrite(t, filepath.Join(dir, p), "x", 0o644)
	}
	exp := jam.ConfigTreeExport{Kits: map[string][]jam.ConfigKitFile{"kits/k": {
		{Path: ".dockerignore", Data: []byte("*.log\nnode_modules\n!keep.log\n"), Mode: 0o644},
		{Path: "kit.yml", Data: []byte("kind: studio\n"), Mode: 0o644},
	}}}
	if err := writeConfigTree(dir, exp); err != nil {
		t.Fatal(err)
	}
	got := readTree(t, dir)
	for _, p := range []string{"kits/k/build.log", "kits/k/node_modules/x/y.js"} {
		if _, ok := got[p]; !ok {
			t.Errorf("%s (ignored) was removed", p)
		}
	}
	for _, p := range []string{"kits/k/stale.txt", "kits/k/keep.log"} {
		if _, ok := got[p]; ok {
			t.Errorf("%s (not ignored, not exported) was kept", p)
		}
	}
}

// Every path the export names is checked before anything is written.
func TestWriteConfigTreeRefusesUnsafePaths(t *testing.T) {
	kit := func(p string, mode uint32) map[string][]jam.ConfigKitFile {
		return map[string][]jam.ConfigKitFile{"kits/k": {{Path: p, Mode: mode}}}
	}
	for name, exp := range map[string]jam.ConfigTreeExport{
		"kit-abs":      {Kits: kit("/etc/x", 0o644)},
		"kit-dotdot":   {Kits: kit("../x", 0o644)},
		"kit-escape":   {Kits: kit("a\x1b[2Jb", 0o644)},
		"kit-mode":     {Kits: kit("x", 0o4755)},
		"kit-mode-600": {Kits: kit("x", 0o600)},
		"dir-dotdot":   {Kits: map[string][]jam.ConfigKitFile{"../kits/k": {}}},
		"dir-other":    {Kits: map[string][]jam.ConfigKitFile{"users": {}}},
		"dir-deep":     {Kits: map[string][]jam.ConfigKitFile{"kits/k/sub": {}}},
		"dir-name":     {Kits: map[string][]jam.ConfigKitFile{"kits/My Kit": {}}},
		"both":         {Kits: kit("kit.yml", 0o644), Files: map[string]string{"kits/k/kit.yml": "x"}},
		"remove-out":   {Remove: []string{"../x"}},
	} {
		dir := t.TempDir()
		mustWrite(t, filepath.Join(dir, "users/bob.yml"), "x", 0o644)
		if exp.Files == nil {
			exp.Files = map[string]string{}
		}
		exp.Files["marker"] = "m"
		err := writeConfigTree(dir, exp)
		if err == nil || !strings.HasPrefix(err.Error(), "refusing to") {
			t.Errorf("%s: %v", name, err)
		}
		if err != nil && strings.Contains(err.Error(), "\x1b") {
			t.Errorf("%s: echoes an unsafe name: %q", name, err)
		}
		if _, serr := os.Stat(filepath.Join(dir, "marker")); !os.IsNotExist(serr) {
			t.Errorf("%s: wrote before refusing", name)
		}
	}
}

// config write writes a kit as its directory (modes kept); apply --dir
// replicates it with the same tree and nothing left to apply; a file the
// kit drops is removed by the next write.
func TestConfigWriteKitDirRoundTrip(t *testing.T) {
	srcURL, src := configServer(t)
	if _, _, err := jam.PushKitFiles(src, "builder", kittreetest.Files("kit.yml", "kind: studio\n", "Dockerfile", "ARG COVE_BASE_IMAGE\nFROM ${COVE_BASE_IMAGE}\n",
		"run.sh*", "#!/bin/sh\n", "lib/deep/blob.bin", "\xff\xfe\x00\x80")); err != nil {
		t.Fatal(err)
	}
	repo := gitDir(t)
	if code, out, errs := runJam("config", "write", repo, "--all", "--admin-url", srcURL); code != 0 {
		t.Fatalf("write: %d %s %s", code, out, errs)
	}
	tree := readTree(t, repo)
	if tree["kits/builder/kit.yml"] != "kind: studio\n" || tree["kits/builder/lib/deep/blob.bin"] != "\xff\xfe\x00\x80" {
		t.Fatalf("tree %v", tree)
	}
	if fi, err := os.Stat(filepath.Join(repo, "kits/builder/run.sh")); err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("run.sh %v %v", fi, err)
	}
	dstURL, dst := configServer(t)
	if code, out, errs := runJam("config", "apply", "--dir", repo, "--yes", "--admin-url", dstURL); code != 0 {
		t.Fatalf("apply: %d\n%s\n%s", code, out, errs)
	}
	s, _ := src.GetKit("builder")
	d, _ := dst.GetKit("builder")
	if d.Trees[d.Current] != s.Trees[s.Current] {
		t.Fatal("the replica's stored tree differs")
	}
	if code, out, _ := runJam("config", "apply", "--dir", repo, "--admin-url", dstURL); code != 0 || !strings.Contains(out, "nothing to apply") {
		t.Fatalf("second apply: %d\n%s", code, out)
	}
	if _, _, err := jam.PushKitFiles(src, "builder", kittreetest.Files("kit.yml", "kind: studio\nprompt: plain now\n")); err != nil {
		t.Fatal(err)
	}
	if code, out, errs := runJam("config", "write", repo, "--all", "--admin-url", srcURL); code != 0 {
		t.Fatalf("rewrite: %d %s %s", code, out, errs)
	}
	if tree := readTree(t, repo); len(tree) != 1 || tree["kits/builder/kit.yml"] != "kind: studio\nprompt: plain now\n" {
		t.Fatalf("after the kit dropped its files: %v", tree)
	}
}

// A kit now written by reference loses its kits/<n>/ except what the dir's
// own .dockerignore excludes (likely gitignored and unrecoverable), which
// survives with the .dockerignore; the loader names the left-over dir.
func TestConfigWriteSwitchToSourceKeepsIgnoredFiles(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "kits/ic/kit.yml"), "kind: studio\n", 0o644)
	mustWrite(t, filepath.Join(dir, "kits/ic/.dockerignore"), "*.log\nsecrets/\n", 0o644)
	mustWrite(t, filepath.Join(dir, "kits/ic/Dockerfile"), "FROM x\n", 0o644)
	mustWrite(t, filepath.Join(dir, "kits/ic/run.sh"), "echo\n", 0o755)
	mustWrite(t, filepath.Join(dir, "kits/ic/build.log"), "local only", 0o644)
	mustWrite(t, filepath.Join(dir, "kits/ic/secrets/key"), "k", 0o644)
	exp := jam.ConfigTreeExport{
		Files: map[string]string{"kits/ic.source.yml": "kind: kit-source\nversion: 1\ngit: https://github.com/acme/kits//ic@main\n"},
		Kits:  map[string][]jam.ConfigKitFile{"kits/ic": {}},
	}
	for range 2 { // a second write keeps them too
		if err := writeConfigTree(dir, exp); err != nil {
			t.Fatal(err)
		}
	}
	got := readTree(t, dir)
	for _, p := range []string{"kits/ic/build.log", "kits/ic/secrets/key", "kits/ic/.dockerignore"} {
		if _, ok := got[p]; !ok {
			t.Errorf("%s was removed", p)
		}
	}
	for _, p := range []string{"kits/ic/kit.yml", "kits/ic/Dockerfile", "kits/ic/run.sh"} {
		if _, ok := got[p]; ok {
			t.Errorf("%s was kept", p)
		}
	}
	tr, err := jam.LoadConfigTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := "kits/ic/ is left over beside kits/ic.source.yml (only files its .dockerignore excludes); remove it"
	if len(tr.Refusals) != 1 || tr.Refusals[0] != want {
		t.Fatalf("refusals %q", tr.Refusals)
	}
}

// An on-disk .dockerignore that is not a regular file (a symlink, say) or
// is oversized is not read: that kit dir is left as it is.
func TestWriteConfigTreeMirrorSkipsOddDockerignore(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	mustWrite(t, filepath.Join(outside, "ignore"), "*\n", 0o644)
	mustWrite(t, filepath.Join(dir, "kits/a/stale"), "x", 0o644)
	if err := os.Symlink(filepath.Join(outside, "ignore"), filepath.Join(dir, "kits/a/.dockerignore")); err != nil {
		t.Skip(err)
	}
	mustWrite(t, filepath.Join(dir, "kits/b/stale"), "x", 0o644)
	mustWrite(t, filepath.Join(dir, "kits/b/.dockerignore"), strings.Repeat("#\n", maxDockerignoreBytes), 0o644)
	if err := writeConfigTree(dir, jam.ConfigTreeExport{Kits: map[string][]jam.ConfigKitFile{"kits/a": {}, "kits/b": {}}}); err != nil {
		t.Fatal(err)
	}
	got := readTree(t, dir)
	for _, p := range []string{"kits/a/stale", "kits/b/stale"} {
		if _, ok := got[p]; !ok {
			t.Errorf("%s was removed", p)
		}
	}
}

// An in-tree symlink at an exported file's path, or at a directory on its
// way, is never written through: the file lands as a regular file at its
// mode, or the write is refused before anything is written.
func TestWriteConfigTreeDoesNotFollowInTreeSymlinks(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "users/bob.yml"), "bob\n", 0o644)
	mustMkdir(t, filepath.Join(dir, "kits/app"))
	cfgMust(t, os.Symlink("../../users/bob.yml", filepath.Join(dir, "kits/app/run.sh")))
	exp := jam.ConfigTreeExport{Kits: map[string][]jam.ConfigKitFile{"kits/app": {
		{Path: "kit.yml", Data: []byte("kind: studio\n"), Mode: 0o644}, {Path: "run.sh", Data: []byte("echo\n"), Mode: 0o755}}}}
	if err := writeConfigTree(dir, exp); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "users/bob.yml")); string(b) != "bob\n" {
		t.Fatalf("users/bob.yml overwritten: %q", b)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "users/bob.yml")); fi.Mode().Perm() != 0o644 {
		t.Fatalf("users/bob.yml mode %v", fi.Mode())
	}
	fi, err := os.Lstat(filepath.Join(dir, "kits/app/run.sh"))
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o755 {
		t.Fatalf("run.sh %v %v", fi, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "kits/app/run.sh")); string(b) != "echo\n" {
		t.Fatalf("run.sh %q", b)
	}

	// A symlinked subdir: refused before anything is written.
	dir = t.TempDir()
	mustWrite(t, filepath.Join(dir, "kits/other/kit.yml"), "kind: studio\n# other\n", 0o644)
	mustMkdir(t, filepath.Join(dir, "kits/app"))
	cfgMust(t, os.Symlink("../other", filepath.Join(dir, "kits/app/lib")))
	exp = jam.ConfigTreeExport{Files: map[string]string{"marker": "m"}, Kits: map[string][]jam.ConfigKitFile{"kits/app": {
		{Path: "kit.yml", Data: []byte("kind: studio\n"), Mode: 0o644}, {Path: "lib/kit.yml", Data: []byte("evil\n"), Mode: 0o644}}}}
	err = writeConfigTree(dir, exp)
	if err == nil || err.Error() != `refusing to write "kits/app/lib/kit.yml": "kits/app/lib" is not a directory` {
		t.Fatalf("err %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "kits/other/kit.yml")); string(b) != "kind: studio\n# other\n" {
		t.Fatalf("kits/other/kit.yml overwritten: %q", b)
	}
	for _, p := range []string{"marker", "kits/app/kit.yml"} {
		if _, err := os.Lstat(filepath.Join(dir, p)); !os.IsNotExist(err) {
			t.Fatalf("%s written before refusing", p)
		}
	}
}

// Conflicting paths are refused before anything is written.
func TestWriteConfigTreeRefusesConflicts(t *testing.T) {
	f := func(p string) jam.ConfigKitFile { return jam.ConfigKitFile{Path: p, Mode: 0o644} }
	for name, exp := range map[string]jam.ConfigTreeExport{
		"duplicate":        {Kits: map[string][]jam.ConfigKitFile{"kits/k": {f("a"), f("a")}}},
		"file-vs-dir-dot":  {Kits: map[string][]jam.ConfigKitFile{"kits/k": {f("a"), f("a.b"), f("a/b")}}},
		"file-vs-dir-dash": {Kits: map[string][]jam.ConfigKitFile{"kits/k": {f("a/b/c"), f("a-x"), f("a/b")}}},
		"file-vs-dir":      {Kits: map[string][]jam.ConfigKitFile{"kits/k": {f("a/b"), f("a")}}},
		"files-is-kit-dir": {Kits: map[string][]jam.ConfigKitFile{"kits/k": {f("kit.yml")}}, Files: map[string]string{"kits/k": "x"}},
		"remove-in-kit":    {Kits: map[string][]jam.ConfigKitFile{"kits/k": {f("kit.yml")}}, Remove: []string{"kits/k/old"}},
		"remove-kit-dir":   {Kits: map[string][]jam.ConfigKitFile{"kits/k": {}}, Remove: []string{"kits/k"}},
	} {
		dir := t.TempDir()
		if exp.Files == nil {
			exp.Files = map[string]string{}
		}
		exp.Files["marker"] = "m"
		if err := writeConfigTree(dir, exp); err == nil || !strings.HasPrefix(err.Error(), "refusing to") {
			t.Errorf("%s: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "marker")); !os.IsNotExist(err) {
			t.Errorf("%s: wrote before refusing", name)
		}
	}
}

// A symlinked or non-directory parent of a Files path (users/, a project's
// roles/) is refused before anything is written.
func TestWriteConfigTreeRefusesSymlinkedFilesParents(t *testing.T) {
	for _, tc := range []struct{ link, file, want string }{
		{"users", "users/bob.yml", `refusing to write "users/bob.yml": "users" is not a directory`},
		{"projects/x/roles", "projects/x/roles/ic.yml", `refusing to write "projects/x/roles/ic.yml": "projects/x/roles" is not a directory`},
	} {
		dir := t.TempDir()
		mustWrite(t, filepath.Join(dir, "elsewhere/bob.yml"), "keep\n", 0o644)
		mustWrite(t, filepath.Join(dir, "elsewhere/ic.yml"), "keep\n", 0o644)
		mustMkdir(t, filepath.Dir(filepath.Join(dir, tc.link)))
		target, _ := filepath.Rel(filepath.Dir(filepath.Join(dir, tc.link)), filepath.Join(dir, "elsewhere"))
		cfgMust(t, os.Symlink(target, filepath.Join(dir, tc.link)))
		exp := jam.ConfigTreeExport{Files: map[string]string{"marker": "m", tc.file: "evil\n"}}
		if err := writeConfigTree(dir, exp); err == nil || err.Error() != tc.want {
			t.Fatalf("%s: err %v", tc.link, err)
		}
		for _, v := range []string{"elsewhere/bob.yml", "elsewhere/ic.yml"} {
			if b, _ := os.ReadFile(filepath.Join(dir, v)); string(b) != "keep\n" {
				t.Fatalf("%s: %s overwritten: %q", tc.link, v, b)
			}
		}
		if _, err := os.Stat(filepath.Join(dir, "marker")); !os.IsNotExist(err) {
			t.Fatalf("%s: wrote before refusing", tc.link)
		}
	}
}

// The upgrade recovery: a config repo written by an older Jam (kit.yml with
// base.context-dir, the context under kits/<n>/context/) is refused by apply
// with the hint; config write then mirrors the migrated kit over it — the
// kit.yml rewritten, context/ gone — and apply has nothing left to do.
func TestConfigWriteRewritesOldContextDirRepo(t *testing.T) {
	url, st := configServer(t)
	if _, _, err := jam.PushKitFiles(st, "app", kittreetest.Files("kit.yml", "kind: studio\n", "Dockerfile", "ARG COVE_BASE_IMAGE\nFROM ${COVE_BASE_IMAGE}\n", "run.sh*", "echo\n")); err != nil {
		t.Fatal(err)
	}
	repo := gitDir(t)
	mustWrite(t, filepath.Join(repo, "kits/app/kit.yml"), "kind: studio\nbase:\n  context-dir: context\n", 0o644)
	mustWrite(t, filepath.Join(repo, "kits/app/context/Dockerfile"), "ARG COVE_BASE_IMAGE\nFROM ${COVE_BASE_IMAGE}\n", 0o644)
	mustWrite(t, filepath.Join(repo, "kits/app/context/run.sh"), "echo\n", 0o755)
	code, out, errs := runJam("config", "apply", "--dir", repo, "--admin-url", url)
	if code == 0 || !strings.Contains(out+errs, "base.context-dir is no longer supported; put a Dockerfile next to kit.yml; a config repo written by an older Jam: run at-jam config write <repo> and commit") {
		t.Fatalf("apply on the old repo: %d %s %s", code, out, errs)
	}
	if code, out, errs := runJam("config", "write", repo, "--all", "--admin-url", url); code != 0 {
		t.Fatalf("write: %d %s %s", code, out, errs)
	}
	tree := readTree(t, repo)
	if tree["kits/app/kit.yml"] != "kind: studio\n" || tree["kits/app/Dockerfile"] == "" || tree["kits/app/run.sh"] != "echo\n" {
		t.Fatalf("tree %v", tree)
	}
	for p := range tree {
		if strings.HasPrefix(p, "kits/app/context/") {
			t.Fatalf("%s survived the write: %v", p, tree)
		}
	}
	before, _ := st.GetKit("app")
	if code, out, errs := runJam("config", "apply", "--dir", repo, "--yes", "--admin-url", url); code != 0 || !strings.Contains(out, "0 created, 0 updated") {
		t.Fatalf("apply after the write (adopts, no new version): %d\n%s\n%s", code, out, errs)
	}
	if after, _ := st.GetKit("app"); after.Current != before.Current {
		t.Fatalf("apply made a version: v%d → v%d", before.Current, after.Current)
	}
	if code, out, errs := runJam("config", "apply", "--dir", repo, "--admin-url", url); code != 0 || !strings.Contains(out, "nothing to apply") {
		t.Fatalf("second apply: %d\n%s\n%s", code, out, errs)
	}
}
