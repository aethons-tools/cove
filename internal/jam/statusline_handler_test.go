package jam

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeLineSetter records SetMission and SetFocus calls.
type fakeLineSetter struct {
	which, actorID, text string
	called               bool
	err                  error
}

func (f *fakeLineSetter) SetMission(actorID, mission string) error {
	f.called, f.which, f.actorID, f.text = true, "mission", actorID, mission
	return f.err
}

func (f *fakeLineSetter) SetFocus(actorID, focus string) error {
	f.called, f.which, f.actorID, f.text = true, "focus", actorID, focus
	return f.err
}

// lineHandlers are the status-line endpoints under test, by path.
var lineHandlers = []struct {
	path string
	mk   func(escalateStore, *fakeLineSetter) http.Handler
}{
	{"mission", func(st escalateStore, set *fakeLineSetter) http.Handler {
		return NewMissionHandler(st, set, testLogger())
	}},
	{"focus", func(st escalateStore, set *fakeLineSetter) http.Handler {
		return NewFocusHandler(st, set, testLogger())
	}},
}

func TestStatusLineHandlers(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		tok        string
		instance   bool
		body       string
		wantCode   int
		wantCalled bool
	}{
		{"ok", http.MethodPut, "tok", true, `{"text":"x"}`, http.StatusNoContent, true},
		{"post not allowed", http.MethodPost, "tok", true, `{"text":"x"}`, http.StatusMethodNotAllowed, false},
		{"no token", http.MethodPut, "", true, `{"text":"x"}`, http.StatusUnauthorized, false},
		{"unknown token", http.MethodPut, "nope", true, `{"text":"x"}`, http.StatusUnauthorized, false},
		{"no instance", http.MethodPut, "tok", false, `{"text":"x"}`, http.StatusForbidden, false},
		{"too large", http.MethodPut, "tok", true, `{"text":"` + strings.Repeat("a", 3000) + `"}`, http.StatusRequestEntityTooLarge, false},
		{"bad json", http.MethodPut, "tok", true, `{`, http.StatusBadRequest, false},
	}
	for _, lh := range lineHandlers {
		for _, tc := range tests {
			t.Run(lh.path+"/"+tc.name, func(t *testing.T) {
				st := newFakeEscStore()
				st.actors[HashToken("tok")] = Actor{ID: "a1"}
				if tc.instance {
					st.instances["a1"] = Instance{}
				}
				set := &fakeLineSetter{}
				h := lh.mk(st, set)
				req := httptest.NewRequest(tc.method, "/"+lh.path, strings.NewReader(tc.body))
				if tc.tok != "" {
					req.Header.Set("Authorization", "Bearer "+tc.tok)
				}
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				if rec.Code != tc.wantCode {
					t.Fatalf("code = %d, want %d", rec.Code, tc.wantCode)
				}
				if set.called != tc.wantCalled {
					t.Fatalf("setter called = %v, want %v", set.called, tc.wantCalled)
				}
				if tc.wantCalled && (set.which != lh.path || set.actorID != "a1" || set.text != "x") {
					t.Fatalf("setter got %s(%q, %q)", set.which, set.actorID, set.text)
				}
				if tc.wantCode == http.StatusMethodNotAllowed && rec.Header().Get("Allow") != "PUT" {
					t.Fatalf("Allow = %q", rec.Header().Get("Allow"))
				}
			})
		}
	}
}

func TestStatusLineHandlerSetterError(t *testing.T) {
	for _, lh := range lineHandlers {
		st := newFakeEscStore()
		st.actors[HashToken("tok")] = Actor{ID: "a1"}
		st.instances["a1"] = Instance{}
		h := lh.mk(st, &fakeLineSetter{err: http.ErrAbortHandler})
		req := httptest.NewRequest(http.MethodPut, "/"+lh.path, strings.NewReader(`{"text":"x"}`))
		req.Header.Set("Authorization", "Bearer tok")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("%s: code = %d, want 500", lh.path, rec.Code)
		}
	}
}
