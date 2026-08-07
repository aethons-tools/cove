package worker

import "testing"

func TestReadTaskJSONAndYAML(t *testing.T) {
	jsonBody := `{"issue":{"key":"AET-33","title":"Add X"},
	  "repo":{"name":"o/r","source-branch":"main","work-branch":"implement/AET-33"},
	  "worker":{"class":"coder"},"task":{"class":"feature","brief":"do it"}}`
	yamlBody := "issue:\n  key: AET-33\n  title: Add X\n" +
		"repo:\n  name: o/r\n  source-branch: main\n  work-branch: implement/AET-33\n" +
		"worker:\n  class: coder\n" +
		"task:\n  class: feature\n  brief: do it\n"
	for _, tc := range []struct{ file, body string }{{"task.json", jsonBody}, {"task.yml", yamlBody}} {
		dir := t.TempDir()
		writeAtTask(t, dir, tc.file, tc.body)
		got, err := ReadTask(dir)
		if err != nil {
			t.Fatalf("%s: %v", tc.file, err)
		}
		if got.Issue.Key != "AET-33" || got.Repo.WorkBranch != "implement/AET-33" ||
			got.Worker.Class != "coder" || got.Task.Brief != "do it" {
			t.Fatalf("%s: %+v", tc.file, got)
		}
	}
}

// The optional issue.closes field round-trips through task.json (and task.yml) —
// the scheduler sets it for a same-repo github-tracker dispatch; at-task honors it.
func TestReadTaskRoundTripsIssueCloses(t *testing.T) {
	jsonBody := `{"issue":{"key":"#42","title":"T","closes":"#42"},
	  "repo":{"name":"o/r","source-branch":"main","work-branch":"implement/42"},
	  "worker":{"class":"implement"},"task":{"brief":"do it"}}`
	yamlBody := "issue:\n  key: '#42'\n  title: T\n  closes: '#42'\n" +
		"repo:\n  name: o/r\n  source-branch: main\n  work-branch: implement/42\n" +
		"worker:\n  class: implement\n" +
		"task:\n  brief: do it\n"
	for _, tc := range []struct{ file, body string }{{"task.json", jsonBody}, {"task.yml", yamlBody}} {
		dir := t.TempDir()
		writeAtTask(t, dir, tc.file, tc.body)
		got, err := ReadTask(dir)
		if err != nil {
			t.Fatalf("%s: %v", tc.file, err)
		}
		if got.Issue.Closes != "#42" {
			t.Fatalf("%s: issue.closes = %q; want %q", tc.file, got.Issue.Closes, "#42")
		}
	}
}

func TestReadTaskRejectsUnknownField(t *testing.T) {
	dir := t.TempDir()
	writeAtTask(t, dir, "task.json", `{"issue":{"key":"K","title":"T"},"repo":{"name":"o/r","source-branch":"main","work-branch":"w"},"worker":{"class":"c"},"task":{"brief":"b"},"bogus":1}`)
	if _, err := ReadTask(dir); err == nil {
		t.Fatal("unknown top-level field must error (strict)")
	}
}
