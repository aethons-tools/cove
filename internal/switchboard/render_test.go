package switchboard

import "testing"

func TestRenderInbox(t *testing.T) {
	if got := RenderInbox(nil); got != "There are no messages." {
		t.Fatalf("empty batch = %q", got)
	}
	got := RenderInbox([]Message{
		{ID: "1", Channel: "project-x", Author: "brent", Content: "fix the flaky test"},
		{ID: "2", Channel: "project-x", Author: "sam", Content: "which one?"},
	})
	want := "New Discord messages:\n[#project-x] brent: fix the flaky test\n[#project-x] sam: which one?\n"
	if got != want {
		t.Fatalf("RenderInbox:\n got: %q\nwant: %q", got, want)
	}
}

func TestParseTurnResult(t *testing.T) {
	r, err := ParseTurnResult([]byte(`{"messages":[{"channel":"project-x","content":"on it"}],"action":"wait"}`))
	if err != nil {
		t.Fatal(err)
	}
	if r.Action != ActionWait || len(r.Messages) != 1 || r.Messages[0].Content != "on it" {
		t.Fatalf("parsed = %+v", r)
	}
	if _, err := ParseTurnResult([]byte(`{"action":"frobnicate"}`)); err == nil {
		t.Fatal("expected error for invalid action")
	}
	if _, err := ParseTurnResult([]byte(`not json`)); err == nil {
		t.Fatal("expected error for bad json")
	}
}
