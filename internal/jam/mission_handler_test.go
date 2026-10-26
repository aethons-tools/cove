package jam

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeMissionSetter records SetMission calls.
type fakeMissionSetter struct {
	actorID, mission string
	called           bool
	err              error
}

func (f *fakeMissionSetter) SetMission(actorID, mission string) error {
	f.called, f.actorID, f.mission = true, actorID, mission
	return f.err
}

func TestMissionHandler(t *testing.T) {
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
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := newFakeEscStore()
			st.actors[HashToken("tok")] = Actor{ID: "a1"}
			if tc.instance {
				st.instances["a1"] = Instance{}
			}
			set := &fakeMissionSetter{}
			h := NewMissionHandler(st, set, testLogger())
			req := httptest.NewRequest(tc.method, "/mission", strings.NewReader(tc.body))
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
			if tc.wantCalled && (set.actorID != "a1" || set.mission != "x") {
				t.Fatalf("setter got (%q, %q)", set.actorID, set.mission)
			}
			if tc.wantCode == http.StatusMethodNotAllowed && rec.Header().Get("Allow") != "PUT" {
				t.Fatalf("Allow = %q", rec.Header().Get("Allow"))
			}
		})
	}
}

func TestMissionHandlerSetterError(t *testing.T) {
	st := newFakeEscStore()
	st.actors[HashToken("tok")] = Actor{ID: "a1"}
	st.instances["a1"] = Instance{}
	h := NewMissionHandler(st, &fakeMissionSetter{err: http.ErrAbortHandler}, testLogger())
	req := httptest.NewRequest(http.MethodPut, "/mission", strings.NewReader(`{"text":"x"}`))
	req.Header.Set("Authorization", "Bearer tok")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, want 500", rec.Code)
	}
}
