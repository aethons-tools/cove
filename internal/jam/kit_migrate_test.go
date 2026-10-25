package jam

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/harnessinstall"
	"github.com/aethons-tools/cove/internal/jam/condition"
	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/studio"
)

var updateKitMigrateGolden = flag.Bool("update-kit-migrate", false, "rewrite internal/jam/testdata/kitmigrate/*.golden")

// tgz packs name, mode, body triples as a pre-tree context: a tar.gz with
// "./" names and directory entries, as `tar czf - .` makes, in base64.
func tgz(t *testing.T, ents ...string) string {
	t.Helper()
	return tgzHeaders(t, func(tw *tar.Writer) {
		for i := 0; i+2 < len(ents); i += 3 {
			var mode int64
			fmt.Sscanf(ents[i+1], "%o", &mode)
			if err := tw.WriteHeader(&tar.Header{Name: "./" + ents[i], Typeflag: tar.TypeReg, Mode: mode, Size: int64(len(ents[i+2]))}); err != nil {
				t.Fatal(err)
			}
			_, _ = tw.Write([]byte(ents[i+2]))
		}
	})
}

// tgzHeaders is tgz with the entries after "./" written by add.
func tgzHeaders(t *testing.T, add func(tw *tar.Writer)) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "./", Typeflag: tar.TypeDir, Mode: 0o755})
	add(tw)
	_ = tw.Close()
	_ = gz.Close()
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// listing renders a version's files for a golden: each path and mode, then its content.
func listing(t *testing.T, st Store, name string) string {
	t.Helper()
	v, ok := LoadKitVersion(st, name, 0)
	if !ok || v.TreeHash == "" {
		t.Fatalf("%s is not a tree: %+v", name, v)
	}
	var b strings.Builder
	for _, e := range v.Tree.Entries {
		data, _ := st.KitBlob(e.SHA256)
		fmt.Fprintf(&b, "== %s %04o\n%s\n", e.Path, e.Mode, data)
	}
	return b.String()
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "kitmigrate", name+".golden")
	if *updateKitMigrateGolden {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil || string(want) != got {
		t.Fatalf("%s (rerun with -update-kit-migrate to accept):\n%s", name, got)
	}
}

func legacyDoc(name string, texts ...string) Kit {
	k := Kit{Name: name, Current: len(texts), Versions: map[int]string{}}
	for i, s := range texts {
		k.Versions[i+1] = s
	}
	return k
}

// exportStable is ExportConfig without its timestamp, for comparing two.
func exportStable(st Store) ConfigSnapshot {
	s := st.ExportConfig()
	s.ExportedAt = time.Time{}
	return s
}

func TestMigrateKitTreesGoldens(t *testing.T) {
	st := NewMemStore()
	ctx := tgz(t, "Dockerfile", "644", "ARG COVE_BASE_IMAGE\nFROM ${COVE_BASE_IMAGE}\nCOPY run.sh /usr/local/bin/\n", "run.sh", "755", "echo hi\n")
	docs := []Kit{
		legacyDoc("context", `{"kind":"studio","base":{"context":"`+ctx+`"},"egress":["github.com"],"prompt":"p"}`),
		legacyDoc("context-files", `{"kind":"studio","base":{"contextFiles":{"dockerfile":"FROM x\n","sub":{"a.txt":"hi\n"}}}}`),
		legacyDoc("plain", `{"kind":"studio","notes":[{"name":"r.md","read-when":"releasing","body":"# R\n"}]}`),
	}
	if err := st.PutKitDocs(docs, KitObjects{}); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	rep, err := MigrateKitTrees(st, nil, log)
	if err != nil || len(rep.Migrated) != 3 || len(rep.Broken) != 0 {
		t.Fatalf("report %+v %v", rep, err)
	}
	if !strings.Contains(logs.String(), "kits migrated to trees") {
		t.Fatalf("log = %s", logs.String())
	}
	for _, d := range docs {
		golden(t, d.Name, listing(t, st, d.Name))
		k, _ := st.GetKit(d.Name)
		if len(k.Versions) != 0 || k.Legacy[1] != d.Versions[1] {
			t.Fatalf("%s: legacy text not kept: %+v", d.Name, k)
		}
	}
	v, _ := LoadKitVersion(st, "context", 0)
	if e, _ := v.Tree.Lookup("run.sh"); e.Mode != 0o755 || v.Kit.BuildContext == "" {
		t.Fatalf("context kit: %+v", v.Tree)
	}
	before := exportStable(st)
	logs.Reset()
	if rep, err := MigrateKitTrees(st, nil, log); err != nil || len(rep.Migrated) != 0 {
		t.Fatalf("second run: %+v %v", rep, err)
	}
	if !reflect.DeepEqual(exportStable(st), before) {
		t.Fatal("a second run changed the store")
	}
	if logs.Len() != 0 {
		t.Fatalf("a second run logged: %s", logs.String())
	}
}

func TestMigrateKitTreesKeepsABrokenVersion(t *testing.T) {
	st := NewMemStore()
	if err := st.PutKitDocs([]Kit{legacyDoc("app", `{"kind":"studio"}`, `{"kind":"studio","base":{"context":"!!not base64"}}`)}, KitObjects{}); err != nil {
		t.Fatal(err)
	}
	tr := condition.New(condition.Options{})
	rep, err := MigrateKitTrees(st, tr, nil)
	if err != nil || len(rep.Broken) != 1 || rep.Broken[0] != "app@v2" {
		t.Fatalf("report %+v %v", rep, err)
	}
	if _, err := StudioKitDefinition(st, "app", harnessinstall.Default()); err == nil || !strings.Contains(err.Error(), "could not be migrated to a tree: its base.context is not valid base64") {
		t.Fatalf("raise must fail closed: %v", err)
	}
	c, ok := tr.Get(condition.Key(KitMigrateKind, "app"))
	if !ok || c.Severity != condition.Warning {
		t.Fatalf("condition = %+v %v", c, ok)
	}
	if strings.Contains(c.Summary+c.Detail+c.Fix, "!!not") {
		t.Fatalf("the condition echoes stored content: %+v", c)
	}
	if v, ok := LoadKitVersion(st, "app", 1); !ok || v.Err != nil {
		t.Fatalf("v1 must have migrated: %+v", v)
	}
	if err := PinKitChecked(st, "app", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateKitTrees(st, tr, nil); err != nil {
		t.Fatal(err)
	}
	if tr.IsOpen(condition.Key(KitMigrateKind, "app")) {
		t.Fatal("the condition must clear once current is not broken")
	}
}

// The ruling from Task 9: the default kit's stored JSON text migrates to the
// very tree EnsureDefaultStudioKit renders, so the first start after the
// upgrade adds no default-kit version.
func TestMigrateKitTreesDefaultKitNotReversioned(t *testing.T) {
	st := NewMemStore()
	text, err := json.Marshal(studio.DefaultStudioKit())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutKitDocs([]Kit{legacyDoc(studio.DefaultStudioKitID, string(text))}, KitObjects{}); err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateKitTrees(st, nil, nil); err != nil {
		t.Fatal(err)
	}
	ref, err := EnsureDefaultStudioKit(st)
	if err != nil || ref.Version != 1 {
		t.Fatalf("the default kit got a new version: %+v %v", ref, err)
	}
	if ref.Digest != studio.BuildDigest(studio.DefaultStudioKit(), harnessinstall.Default()) {
		t.Fatal("a kit without a Dockerfile must keep its digest")
	}
}

func TestMigrateKitTreesRekeysTheMark(t *testing.T) {
	for _, src := range []*KitSource{nil, {Repo: "https://github.com/acme/kits", Dir: "app", Ref: "main"}} {
		st := NewMemStore()
		text := `{"kind":"studio","prompt":"p"}`
		doc := legacyDoc("app", text)
		doc.Source = src
		if err := st.PutKitDocs([]Kit{doc}, KitObjects{}); err != nil {
			t.Fatal(err)
		}
		if err := st.ApplyConfig(ConfigWrite{Marks: []ConfigMark{{Kind: ConfigKindKit, Name: "app", Commit: "c", Digest: legacyKitCanonDigest(text, src)}}}); err != nil {
			t.Fatal(err)
		}
		if _, err := MigrateKitTrees(st, nil, nil); err != nil {
			t.Fatal(err)
		}
		states, err := ConfigMarkStates(st)
		if err != nil || len(states) != 1 || states[0].State != ConfigInSync {
			t.Fatalf("tracked %v: states = %+v %v", src != nil, states, err)
		}
	}
}

// A managed kit edited in Jam before the upgrade (its mark recorded another
// text) keeps its mark: it still shows as drifted.
func TestMigrateKitTreesKeepsADriftedMark(t *testing.T) {
	st := NewMemStore()
	if err := st.PutKitDocs([]Kit{legacyDoc("app", `{"kind":"studio","prompt":"edited"}`)}, KitObjects{}); err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyConfig(ConfigWrite{Marks: []ConfigMark{{Kind: ConfigKindKit, Name: "app", Commit: "c", Digest: legacyKitCanonDigest(`{"kind":"studio","prompt":"p"}`, nil)}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateKitTrees(st, nil, nil); err != nil {
		t.Fatal(err)
	}
	states, err := ConfigMarkStates(st)
	if err != nil || len(states) != 1 || states[0].State != ConfigDrifted {
		t.Fatalf("states = %+v %v", states, err)
	}
}

// A managed kit whose current version is kept broken has its mark re-keyed
// to the broken canon: not drift (the migration, not an operator, changed
// it). Its plan is an update to the repo's copy, which un-breaks it.
func TestMigrateKitTreesBrokenManagedKit(t *testing.T) {
	st := NewMemStore()
	text := `{"kind":"studio","base":{"context":"!!not base64"}}`
	if err := st.PutKitDocs([]Kit{legacyDoc("app", text)}, KitObjects{}); err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyConfig(ConfigWrite{Marks: []ConfigMark{{Kind: ConfigKindKit, Name: "app", Commit: "c", Digest: legacyKitCanonDigest(text, nil)}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateKitTrees(st, nil, nil); err != nil {
		t.Fatal(err)
	}
	states, err := ConfigMarkStates(st)
	if err != nil || len(states) != 1 || states[0].State != ConfigInSync {
		t.Fatalf("states = %+v %v", states, err)
	}
	p := planOn(t, st, treeOf(t, map[string]string{"kits/app/kit.yml": "kind: studio\n"}), false)
	if op := opOf(p, ConfigKindKit, "app"); op.Action != ConfigUpdate || op.Drift || len(p.Refusals) != 0 {
		t.Fatalf("op %+v refusals %v", op, p.Refusals)
	}
	applyPlan(t, st, p)
	if v, ok := LoadKitVersion(st, "app", 0); !ok || v.Err != nil || v.Version != 2 {
		t.Fatalf("after apply: %+v", v)
	}
}

// A stored config that does not parse once its context is out keeps its
// stripped text as kit.yml: as invalid as before, and the packed base64 is
// in no blob (only the kit's legacy text keeps it).
func TestMigrateKitTreesUnparsableVersionHidesBase64(t *testing.T) {
	st := NewMemStore()
	ctx := tgz(t, "Dockerfile", "644", "FROM x\n")
	text := `{"kind":"studio","bogus":1,"base":{"context":"` + ctx + `"}}`
	if err := st.PutKitDocs([]Kit{legacyDoc("app", text)}, KitObjects{}); err != nil {
		t.Fatal(err)
	}
	rep, err := MigrateKitTrees(st, nil, nil)
	if err != nil || len(rep.Broken) != 0 {
		t.Fatalf("report %+v %v", rep, err)
	}
	v, _ := LoadKitVersion(st, "app", 0)
	if v.TreeHash == "" || v.Err == nil {
		t.Fatalf("the version must be a tree that does not parse: %+v", v)
	}
	if _, err := studio.ParseStudioKit([]byte(text)); err == nil {
		t.Fatal("fixture: the legacy text must not parse")
	}
	if strings.Contains(string(v.KitYML), "context") || !strings.Contains(string(v.KitYML), "bogus") {
		t.Fatalf("kit.yml = %s", v.KitYML)
	}
	if _, ok := v.Tree.Lookup("Dockerfile"); !ok {
		t.Fatalf("the context's files must be kept: %+v", v.Tree)
	}
	for _, e := range v.Tree.Entries {
		b, _ := st.KitBlob(e.SHA256)
		if bytes.Contains(b, []byte(ctx[:24])) {
			t.Fatalf("%s holds the packed base64", e.Path)
		}
	}
	exp, err := ExportConfigTree(st.ExportConfig(), nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(exp); bytes.Contains(b, []byte(ctx[:24])) {
		t.Fatal("the config export holds the packed base64")
	}
}

func TestMigrateKitTreesAppliesTheContextsDockerignore(t *testing.T) {
	st := NewMemStore()
	ctx := tgz(t, "Dockerfile", "644", "FROM x\n", ".dockerignore", "644", "secret.txt\n", "secret.txt", "644", "s\n")
	if err := st.PutKitDocs([]Kit{legacyDoc("app", `{"kind":"studio","base":{"context":"`+ctx+`"}}`)}, KitObjects{}); err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateKitTrees(st, nil, nil); err != nil {
		t.Fatal(err)
	}
	v, _ := LoadKitVersion(st, "app", 0)
	if _, ok := v.Tree.Lookup("secret.txt"); ok || len(v.Tree.Entries) != 3 {
		t.Fatalf("tree %+v", v.Tree)
	}
}

// Hostile packed contexts are kept broken with a fixed reason that names no
// path and quotes no content.
func TestLegacyContextRefusals(t *testing.T) {
	reg := func(name string, body string) func(tw *tar.Writer) {
		return func(tw *tar.Writer) {
			_ = tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))})
			_, _ = tw.Write([]byte(body))
		}
	}
	shrink := func(entries int, file, total int64) func() {
		e, f, tot := kittree.MaxEntries, kittree.MaxFile, kittree.MaxTotal
		kittree.MaxEntries, kittree.MaxFile, kittree.MaxTotal = entries, file, total
		return func() { kittree.MaxEntries, kittree.MaxFile, kittree.MaxTotal = e, f, tot }
	}
	cases := []struct {
		name string
		add  func(tw *tar.Writer)
		lim  func() func()
		want error
	}{
		{"symlink", func(tw *tar.Writer) {
			_ = tw.WriteHeader(&tar.Header{Name: "./evil", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"})
		}, nil, errLegacyLink},
		{"hardlink", func(tw *tar.Writer) {
			_ = tw.WriteHeader(&tar.Header{Name: "./evil", Typeflag: tar.TypeLink, Linkname: "Dockerfile"})
		}, nil, errLegacyLink},
		{"fifo", func(tw *tar.Writer) { _ = tw.WriteHeader(&tar.Header{Name: "./evil", Typeflag: tar.TypeFifo}) }, nil, errLegacyLink},
		{"dotdot", reg("../evil", "x"), nil, errLegacyUnsafe},
		{"absolute", reg("/etc/evil", "x"), nil, errLegacyUnsafe},
		{"control", reg("./ev\x1bil", "x"), nil, errLegacyUnsafe},
		{"root kit.yml", reg("./kit.yml", "kind: studio\n"), nil, errLegacyKitYML},
		{"big file", reg("./big", strings.Repeat("x", 11)), func() func() { return shrink(2000, 10, 100) }, errLegacyLimits},
		{"duplicates count", func(tw *tar.Writer) {
			for range 5 {
				reg("./a", strings.Repeat("x", 8))(tw)
			}
		}, func() func() { return shrink(2000, 10, 30) }, errLegacyLimits},
		{"many files", func(tw *tar.Writer) {
			for i := range 3 {
				reg(fmt.Sprintf("./f%d", i), "x")(tw)
			}
		}, func() func() { return shrink(2, 10, 100) }, errLegacyLimits},
		{"many headers", func(tw *tar.Writer) {
			for i := range 9 {
				_ = tw.WriteHeader(&tar.Header{Name: fmt.Sprintf("./d%d/", i), Typeflag: tar.TypeDir, Mode: 0o755})
			}
		}, func() func() {
			h := legacyMaxHeaders
			legacyMaxHeaders = 8
			return func() { legacyMaxHeaders = h }
		}, errLegacyLimits},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b64 := tgzHeaders(t, c.add)
			if c.lim != nil {
				defer c.lim()()
			}
			_, _, err := legacyKitTree(`{"kind":"studio","base":{"context":"` + b64 + `"}}`)
			if err != c.want {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
	for _, c := range []struct{ text, want string }{
		{`{"kind":"studio","base":{"context":"bm90IGd6aXA="}}`, errLegacyTar.Error()},
		{`{"kind":"studio","base":{"contextFiles":{"../x":"y"}}}`, errLegacyUnsafe.Error()},
		{`{"kind":"studio","base":{"contextFiles":{"a":["y"]}}}`, errLegacyTree.Error()},
		{`{"kind":"studio","base":{"contextFiles":{"a":"y"},"context-files":{"b":"z"}}}`, errLegacyTwice.Error()},
		{"kind: studio\nx: &c {context: abc}\nbase:\n  <<: *c\n", errLegacyAlias.Error()},
	} {
		if _, _, err := legacyKitTree(c.text); err == nil || err.Error() != c.want {
			t.Fatalf("%s: err = %v, want %s", c.text, err, c.want)
		}
	}
}
