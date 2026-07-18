package install

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func TestCurrencyHashDeterministicAndFieldSensitive(t *testing.T) {
	base := CurrencyInputs{
		KitSourceTree:       "kit-a",
		AtCoveBuildIdentity: "id-a",
		BaseRef:             "ref-a",
	}
	if CurrencyHash(base) != CurrencyHash(base) {
		t.Fatal("CurrencyHash must be deterministic for identical inputs")
	}
	// Every field must independently move the hash.
	for name, mut := range map[string]func(CurrencyInputs) CurrencyInputs{
		"kitSourceTree":       func(c CurrencyInputs) CurrencyInputs { c.KitSourceTree = "kit-b"; return c },
		"atCoveBuildIdentity": func(c CurrencyInputs) CurrencyInputs { c.AtCoveBuildIdentity = "id-b"; return c },
		"baseRef":             func(c CurrencyInputs) CurrencyInputs { c.BaseRef = "ref-b"; return c },
	} {
		if CurrencyHash(mut(base)) == CurrencyHash(base) {
			t.Errorf("changing %s must change the currency hash", name)
		}
	}
}

// The field encoding must be unambiguous: shifting a boundary between fields must
// not collide (a naive concatenation would let "a"+"bc" == "ab"+"c").
func TestCurrencyHashUnambiguousFieldBoundaries(t *testing.T) {
	x := CurrencyInputs{KitSourceTree: "a", AtCoveBuildIdentity: "bc", BaseRef: "d"}
	y := CurrencyInputs{KitSourceTree: "ab", AtCoveBuildIdentity: "c", BaseRef: "d"}
	if CurrencyHash(x) == CurrencyHash(y) {
		t.Fatal("distinct field splits must not hash equal")
	}
}

func TestHashTreeDeterministicAndContentSensitive(t *testing.T) {
	a := fstest.MapFS{
		"Dockerfile":        {Data: []byte("FROM base")},
		"image-files/a.txt": {Data: []byte("one")},
		"image-files/b.txt": {Data: []byte("two")},
	}
	h1, err := HashTree(a)
	if err != nil {
		t.Fatal(err)
	}
	// A byte-identical tree hashes equal regardless of declaration order.
	b := fstest.MapFS{
		"image-files/b.txt": {Data: []byte("two")},
		"image-files/a.txt": {Data: []byte("one")},
		"Dockerfile":        {Data: []byte("FROM base")},
	}
	h2, err := HashTree(b)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatalf("identical trees hashed differently: %s != %s", h1, h2)
	}

	// An edited file, an added file, and a renamed path must each change the hash.
	for name, mut := range map[string]fstest.MapFS{
		"edited":  {"Dockerfile": {Data: []byte("FROM other")}, "image-files/a.txt": {Data: []byte("one")}, "image-files/b.txt": {Data: []byte("two")}},
		"added":   {"Dockerfile": {Data: []byte("FROM base")}, "image-files/a.txt": {Data: []byte("one")}, "image-files/b.txt": {Data: []byte("two")}, "image-files/c.txt": {Data: []byte("three")}},
		"renamed": {"Dockerfile": {Data: []byte("FROM base")}, "image-files/a.txt": {Data: []byte("one")}, "image-files/renamed.txt": {Data: []byte("two")}},
	} {
		got, err := HashTree(mut)
		if err != nil {
			t.Fatal(err)
		}
		if got == h1 {
			t.Errorf("%s tree must not match the original hash", name)
		}
	}
}

func TestKitSourceTreeHashesConfigAndImageTree(t *testing.T) {
	writeKit := func(cfg string, image map[string]string) string {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
		for rel, data := range image {
			p := filepath.Join(dir, "image", rel)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	hash := func(dir string) string {
		h, err := KitSourceTree(dir)
		if err != nil {
			t.Fatalf("KitSourceTree(%s): %v", dir, err)
		}
		return h
	}

	baseCfg := "name: box\n"
	baseImg := map[string]string{"Dockerfile": "FROM x", "image-files/motd": "hi"}

	// Same inputs (in two separate dirs) hash equal — the hash is of content, not path.
	if hash(writeKit(baseCfg, baseImg)) != hash(writeKit(baseCfg, baseImg)) {
		t.Fatal("identical kit source must hash equal across dirs")
	}
	unchanged := hash(writeKit(baseCfg, baseImg))

	// Editing config.yml changes the hash.
	if hash(writeKit("name: other\n", baseImg)) == unchanged {
		t.Error("edited config.yml must change the kit-source hash")
	}
	// Editing a file under image/ changes the hash.
	if hash(writeKit(baseCfg, map[string]string{"Dockerfile": "FROM y", "image-files/motd": "hi"})) == unchanged {
		t.Error("edited image/ file must change the kit-source hash")
	}
	// A kit with no image/ dir is valid and hashes over config.yml alone.
	noImg := hash(writeKit(baseCfg, nil))
	if noImg == "" || noImg == unchanged {
		t.Error("a kit without image/ must still hash (config-only), distinct from one with image/")
	}
}

func TestKitSourceTreeErrorsWithoutConfig(t *testing.T) {
	if _, err := KitSourceTree(t.TempDir()); err == nil {
		t.Fatal("KitSourceTree must error when config.yml is absent")
	}
}

func TestAtCoveIdentityDeterministicAndNonEmpty(t *testing.T) {
	id1, err := AtCoveIdentity()
	if err != nil {
		t.Fatal(err)
	}
	id2, err := AtCoveIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if id1 == "" {
		t.Fatal("AtCoveIdentity must be non-empty")
	}
	if id1 != id2 {
		t.Fatalf("AtCoveIdentity must be stable: %s != %s", id1, id2)
	}
}
