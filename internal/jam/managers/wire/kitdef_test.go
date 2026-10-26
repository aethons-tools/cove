package wire

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"

	"github.com/aethons-tools/cove/internal/harnessinstall"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/studio"
)

func put(t *testing.T, fs billy.Filesystem, p string, mode os.FileMode, data string) {
	t.Helper()
	f, err := fs.OpenFile(p, os.O_CREATE|os.O_WRONLY, mode)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(data)); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func TestKitDefinitionRoundTrip(t *testing.T) {
	fs := memfs.New()
	put(t, fs, "Dockerfile", 0o644, "FROM scratch\n")
	put(t, fs, "bin/run.sh", 0o755, "#!/bin/sh\necho hi\n")
	if err := fs.Symlink("bin/run.sh", "latest"); err != nil {
		t.Fatal(err)
	}
	ref := jam.KitRef{ID: "top", Version: 2, Digest: "dd"}
	kit := studio.StudioKit{Kind: "studio", BuildContext: "abc"}
	def := jam.KitDefinition{
		Ref: ref, Kit: kit,
		Harness: harnessinstall.Install{Version: "1.2.3", Plugins: []string{"p"}},
		Chain: []jam.KitLink{
			{Ref: jam.KitRef{ID: "root", Version: 1, Digest: "r"}, Kit: studio.StudioKit{Kind: "studio"}, Follow: true},
			{Ref: ref, Kit: kit, Files: fs},
		},
	}
	enc, err := EncodeKitDefinition(def)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(KitRequest{Def: enc})
	if err != nil {
		t.Fatal(err)
	}
	var back KitRequest
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	got, err := back.Def.Definition()
	if err != nil {
		t.Fatal(err)
	}
	if got.Chain[0].Files != nil {
		t.Fatal("root link Files must stay nil")
	}
	if got.Kit.BuildContext != "abc" || got.Chain[1].Kit.BuildContext != "abc" {
		t.Fatalf("BuildContext lost: %q %q", got.Kit.BuildContext, got.Chain[1].Kit.BuildContext)
	}
	if got.Ref != def.Ref || !reflect.DeepEqual(got.Harness, def.Harness) {
		t.Fatal("ref/harness differ")
	}
	var w1, w2 bytes.Buffer
	if err := kittree.ContextTar(&w1, fs); err != nil {
		t.Fatal(err)
	}
	if err := kittree.ContextTar(&w2, got.Chain[1].Files); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(w1.Bytes(), w2.Bytes()) {
		t.Fatal("context tar differs")
	}
	// The tar may not carry symlinks or exec bits visibly; check them directly.
	fi, err := got.Chain[1].Files.Lstat("bin/run.sh")
	if err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("run.sh mode: %v %v", fi, err)
	}
	l, err := got.Chain[1].Files.Readlink("latest")
	if err != nil || l != "bin/run.sh" {
		t.Fatalf("symlink: %q %v", l, err)
	}
}
