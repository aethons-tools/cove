package studio

import (
	"reflect"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-billy/v5/util"

	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/kittree/kittreetest"
)

func TestParseTreeResolvesNoteFilesAndSetsBuildContext(t *testing.T) {
	tr, blobs := kittreetest.Tree(t,
		"kit.yml", "kind: studio\nnotes:\n  - name: release.md\n    read-when: releasing\n    file: ./docs/release.md\n",
		"docs/release.md", "# Release\n",
		"Dockerfile", "ARG COVE_BASE_IMAGE\nFROM ${COVE_BASE_IMAGE}\n",
	)
	sk, err := ParseTree(tr, kittree.MapBlobs(blobs))
	if err != nil {
		t.Fatal(err)
	}
	if sk.Notes[0].Body != "# Release\n" || sk.Notes[0].File != "" {
		t.Fatalf("note = %+v", sk.Notes[0])
	}
	if sk.BuildContext == "" || sk.BuildContext != tr.ContextHash() {
		t.Fatalf("BuildContext = %q", sk.BuildContext)
	}
}

func TestParseTreeWithoutDockerfileHasNoBuildContext(t *testing.T) {
	tr, blobs := kittreetest.Tree(t, "kit.yml", "kind: studio\n", "run.sh*", "echo\n")
	sk, err := ParseTree(tr, kittree.MapBlobs(blobs))
	if err != nil || sk.BuildContext != "" {
		t.Fatalf("sk = %+v, %v", sk, err)
	}
}

func TestParseKitFSRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		files map[string]string
		want  string
	}{
		"missing kit.yml":    {map[string]string{"Dockerfile": "FROM x"}, "kit.yml missing at the root"},
		"note not in kit":    {map[string]string{"kit.yml": "kind: studio\nnotes:\n  - {name: a.md, read-when: x, file: a.md}\n"}, `note "a.md": file a.md is not in the kit`},
		"note escapes":       {map[string]string{"kit.yml": "kind: studio\nnotes:\n  - {name: a.md, read-when: x, file: ../a.md}\n"}, `note "a.md": file must be a relative path inside the kit directory`},
		"note absolute":      {map[string]string{"kit.yml": "kind: studio\nnotes:\n  - {name: a.md, read-when: x, file: /etc/passwd}\n"}, `note "a.md": file must be a relative path inside the kit directory`},
		"note body and file": {map[string]string{"kit.yml": "kind: studio\nnotes:\n  - {name: a.md, read-when: x, body: b, file: a.md}\n", "a.md": "x"}, `note "a.md": set body or file, not both`},
		"note files too big": {map[string]string{"kit.yml": "kind: studio\nnotes:\n  - {name: a.md, read-when: x, file: a.md}\n", "a.md": strings.Repeat("x", maxNoteFileBytes+1)}, `notes: note files exceed the 1048576-byte total cap (at note "a.md")`},
		"image + Dockerfile": {map[string]string{"kit.yml": "kind: studio\nbase: {image: ghcr.io/x@sha256:1}\n", "Dockerfile": "FROM x"}, "studio kit: base.image and a Dockerfile next to kit.yml are mutually exclusive"},
	} {
		t.Run(name, func(t *testing.T) {
			fsys := memfs.New()
			for p, b := range tc.files {
				if err := util.WriteFile(fsys, p, []byte(b), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := ParseKitFS(fsys); err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestParseFiles(t *testing.T) {
	tr, blobs, sk, err := ParseFiles(kittreetest.Files("kit.yml", "kind: studio\nprompt: hi\n", "Dockerfile", "FROM x\n"))
	if err != nil {
		t.Fatal(err)
	}
	if sk.Prompt != "hi" || sk.BuildContext != tr.ContextHash() || len(blobs) != 2 || len(tr.Entries) != 2 {
		t.Fatalf("ParseFiles = %+v %+v %v", tr, sk, blobs)
	}
	for files, want := range map[string]string{
		"kit.yml\x00kind: studio\nbase:\n  context: H4sI\n": "base.context is no longer supported; put a Dockerfile next to kit.yml",
		"Dockerfile\x00FROM x\n":                            "kit.yml missing at the root",
		"kit.yml\x00kind: nope\n":                           "",
	} {
		kv := strings.Split(files, "\x00")
		_, _, _, err := ParseFiles(kittreetest.Files(kv...))
		if err == nil || (want != "" && err.Error() != want) {
			t.Fatalf("ParseFiles(%q) err = %v, want %q", kv[0], err, want)
		}
	}
}

func TestRefuseContextFields(t *testing.T) {
	for key, want := range map[string]string{
		"context: H4sI":                  "base.context is no longer supported; put a Dockerfile next to kit.yml",
		"context-files: {dockerfile: x}": "base.context-files is no longer supported; put a Dockerfile next to kit.yml",
		"contextFiles: {dockerfile: x}":  "base.context-files is no longer supported; put a Dockerfile next to kit.yml",
		"context-dir: ctx":               "base.context-dir is no longer supported; put a Dockerfile next to kit.yml",
	} {
		if err := RefuseContextFields([]byte("kind: studio\nbase:\n  " + key + "\n")); err == nil || err.Error() != want {
			t.Fatalf("%s: err = %v", key, err)
		}
	}
	if err := RefuseContextFields([]byte("kind: studio\nbase: {kit: web}\n")); err != nil {
		t.Fatal(err)
	}
}

func TestRenderKitYAMLParsesBack(t *testing.T) {
	sk := DefaultStudioKit()
	sk.LegacyName = "x"
	b, err := RenderKitYAML(sk)
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseStudioKit(b)
	if err != nil {
		t.Fatal(err)
	}
	sk.LegacyName = ""
	if !reflect.DeepEqual(back, sk) {
		t.Fatalf("round trip:\n%+v\n%+v", back, sk)
	}
	if strings.Contains(string(b), "name:") {
		t.Fatalf("rendered a legacy name:\n%s", b)
	}
}

func TestSetBaseKitEditsOnlyTheScalar(t *testing.T) {
	for in, want := range map[string]string{
		"kind: studio\nbase:\n  kit: ../web # the parent\nprompt: hi\n": "kind: studio\nbase:\n  kit: web@v3 # the parent\nprompt: hi\n",
		"kind: studio\nbase:\n  kit: \"../web\"\n":                      "kind: studio\nbase:\n  kit: \"web@v3\"\n",
		"kind: studio\nbase:\n  kit: '../web'\n":                        "kind: studio\nbase:\n  kit: 'web@v3'\n",
		`{"kind":"studio","base":{"kit":"../web"}}`:                     `{"kind":"studio","base":{"kit":"web@v3"}}`,
		"prompt: \"é\"\nbase: {image: \"ü\", kit: ../web}\n":            "prompt: \"é\"\nbase: {image: \"ü\", kit: web@v3}\n",
	} {
		got, err := SetBaseKit([]byte(in), "web@v3")
		if err != nil || string(got) != want {
			t.Fatalf("SetBaseKit(%q) = %q, %v; want %q", in, got, err, want)
		}
		back, err := SetBaseKit(got, "../web")
		if err != nil || string(back) != in {
			t.Fatalf("not reversible: %q → %q", got, back)
		}
	}
	for _, in := range []string{
		"kind: studio\n",
		"kind: studio\nbase:\n  kit: |\n    ../web\n",
		"kind: studio\nbase: {kit: \"..\\/web\"}\n",
	} {
		if _, err := SetBaseKit([]byte(in), "web@v3"); err == nil {
			t.Fatalf("SetBaseKit(%q) must refuse", in)
		}
	}
}
