package worker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestReadInput(t *testing.T) {
	p := filepath.Join(t.TempDir(), "in.json")
	os.WriteFile(p, []byte(`{"issue":{"key":"AET-1","title":"T","work-class":"implement","brief":"do it"},"repo":{"name":"o/r","source-branch":"main","work-branch":"implement/AET-1"}}`), 0o600)
	in, err := ReadInput(p)
	if err != nil {
		t.Fatalf("ReadInput: %v", err)
	}
	if in.Issue.Key != "AET-1" || in.Issue.Brief != "do it" || in.Issue.Title != "T" || in.Issue.WorkClass != "implement" || in.Repo.Name != "o/r" || in.Repo.SourceBranch != "main" || in.Repo.WorkBranch != "implement/AET-1" {
		t.Fatalf("parsed wrong: key=%s brief=%s title=%s workclass=%s name=%s sourcebranch=%s workbranch=%s", in.Issue.Key, in.Issue.Brief, in.Issue.Title, in.Issue.WorkClass, in.Repo.Name, in.Repo.SourceBranch, in.Repo.WorkBranch)
	}
}

func TestReadOutcome(t *testing.T) {
	dir := t.TempDir()
	// absent → ok=false, no error
	if _, ok, err := ReadOutcome(dir); ok || err != nil {
		t.Fatalf("absent outcome: ok=%v err=%v; want false,nil", ok, err)
	}
	// present
	os.MkdirAll(filepath.Join(dir, workSubdir), 0o755)
	os.WriteFile(filepath.Join(dir, workSubdir, "outcome.json"), []byte(`{"status":"OK","pr-message":"body"}`), 0o600)
	oc, ok, err := ReadOutcome(dir)
	if err != nil || !ok || oc.Status != "OK" || oc.PRMessage != "body" {
		t.Fatalf("present outcome: %+v ok=%v err=%v", oc, ok, err)
	}
}

func TestWriteBriefAndOutput(t *testing.T) {
	dir := t.TempDir()
	if err := WriteBrief(dir, "the brief"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, workSubdir, "brief.md"))
	if string(got) != "the brief" {
		t.Fatalf("brief = %q", got)
	}
	out := filepath.Join(t.TempDir(), "out.json")
	if err := WriteOutput(out, Output{Status: StatusOK, Work: Work{PRURL: "u"}}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	var jsonOut map[string]any
	if err := json.Unmarshal(b, &jsonOut); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if jsonOut["status"] != "OK" {
		t.Fatalf("status = %v; want OK", jsonOut["status"])
	}
	work, _ := jsonOut["work"].(map[string]any)
	if work["pr-url"] != "u" {
		t.Fatalf("work.pr-url = %v; want u (kebab-case key)", work["pr-url"])
	}
}
