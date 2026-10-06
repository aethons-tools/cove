package jam_test

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
)

const (
	testIssuer  = "https://idp.example"
	testSubject = "sub-alice"
)

// meFakeStore is a participantSendStore: canned per-project rosters and a flat
// instance list. No file, network, or VM.
type meFakeStore struct {
	rosters   map[string]jam.Roster
	instances []jam.Instance
}

func (s *meFakeStore) GetRoster(project string) (jam.Roster, bool) {
	r, ok := s.rosters[project]
	return r, ok
}
func (s *meFakeStore) ListInstances() []jam.Instance { return s.instances }

// meFakeAppender records the appended message, and can be scripted to fail.
type meFakeAppender struct {
	got []intercom.LegacySquawk
	err error
}

func (a *meFakeAppender) Append(m intercom.LegacySquawk) (intercom.LegacySquawk, error) {
	a.got = append(a.got, m)
	return m, a.err
}

// meWorld builds a one-project world (acme): alice bound to the test OIDC
// identity, a named channel #eng, a waiting studio cove-1 on ACME-1, and a
// running studio cove-2 on ACME-2.
func meWorld() *meFakeStore {
	return &meFakeStore{
		rosters: map[string]jam.Roster{
			"acme": {
				Humans: []jam.Human{
					{Name: "alice", Handle: "alice", Identity: []jam.OIDCIdentity{{Issuer: testIssuer, Subject: testSubject}}},
					{Name: "bob"},
				},
				Channels: []jam.RosterChannel{{Name: "eng", Service: "linear", Ref: "ACME-9"}},
			},
		},
		instances: []jam.Instance{
			{ActorID: "cove-1", Project: "acme", Unit: "ACME-1", Phase: jam.PhaseLive, Activity: jam.ActivityWaiting},
			{ActorID: "cove-2", Project: "acme", Unit: "ACME-2", Phase: jam.PhaseLive, Activity: jam.ActivityRunning},
		},
	}
}

func aliceParticipant() jam.Participant {
	return jam.Participant{Issuer: testIssuer, Subject: testSubject, Projects: []string{"acme"}, Name: "alice"}
}

// postSend drives the handler with a participant injected (as the gate does).
func postSend(h *jam.ParticipantSendHandler, p jam.Participant, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/me/send", strings.NewReader(body))
	r = jam.WithParticipant(r, p)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestParticipantSend_ReplyToWaitingStudioAppendsToSessionActor(t *testing.T) {
	app := &meFakeAppender{}
	h := jam.NewParticipantSendHandler(meWorld(), app, nil)

	w := postSend(h, aliceParticipant(), `{"to":"studio:ACME-1","body":"on it"}`)

	if w.Code != 204 {
		t.Fatalf("status = %d, want 204 (%s)", w.Code, w.Body.String())
	}
	if len(app.got) != 1 {
		t.Fatalf("appended %d messages, want 1", len(app.got))
	}
	m := app.got[0]
	// From the participant's roster identity (never the body), addressed to the
	// studio's SESSION actor so wake-on resumes it, external-origin, project set.
	if m.From != (intercom.Target{Kind: "human", Ref: "alice"}) {
		t.Errorf("from = %q, want human:alice", m.From.String())
	}
	if len(m.To) != 1 || m.To[0] != (intercom.Target{Kind: "actor", Ref: "cove-1"}) {
		t.Errorf("to = %v, want [actor:cove-1]", m.To)
	}
	if intercom.Classify(m.From) != intercom.External {
		t.Errorf("from must classify External (so wake-on treats it as a reply)")
	}
	if m.Body != "on it" || m.Project != "acme" {
		t.Errorf("body/project = %q/%q, want %q/acme", m.Body, m.Project, "on it")
	}
}

func TestParticipantSend_NewMessageToHumanAndSession(t *testing.T) {
	for _, tc := range []struct {
		name string
		to   string
		want intercom.Target
	}{
		{"human recipient", "human:bob", intercom.Target{Kind: "human", Ref: "bob"}},
		{"named channel", "channel:eng", intercom.Target{Kind: "channel", Ref: "eng"}},
		{"session DM", "actor:cove-2", intercom.Target{Kind: "actor", Ref: "cove-2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := &meFakeAppender{}
			h := jam.NewParticipantSendHandler(meWorld(), app, nil)
			w := postSend(h, aliceParticipant(), `{"to":"`+tc.to+`","body":"hello"}`)
			if w.Code != 204 {
				t.Fatalf("status = %d, want 204 (%s)", w.Code, w.Body.String())
			}
			if len(app.got) != 1 || app.got[0].To[0] != tc.want {
				t.Fatalf("to = %v, want [%s]", app.got, tc.want.String())
			}
		})
	}
}

func TestParticipantSend_FromRefUsesTargetProjectRosterName(t *testing.T) {
	// A global person bound in two projects under different roster names. A
	// recipient that lives only in beta must be attributed from beta's name.
	store := &meFakeStore{
		rosters: map[string]jam.Roster{
			"alpha": {Humans: []jam.Human{{Name: "alice", Identity: []jam.OIDCIdentity{{Issuer: testIssuer, Subject: testSubject}}}}},
			"beta":  {Humans: []jam.Human{{Name: "alice-b", Identity: []jam.OIDCIdentity{{Issuer: testIssuer, Subject: testSubject}}}}},
		},
		instances: []jam.Instance{
			{ActorID: "cove-b", Project: "beta", Unit: "BETA-1", Phase: jam.PhaseLive, Activity: jam.ActivityWaiting},
		},
	}
	app := &meFakeAppender{}
	h := jam.NewParticipantSendHandler(store, app, nil)
	p := jam.Participant{Issuer: testIssuer, Subject: testSubject, Projects: []string{"alpha", "beta"}, Name: "alice"}

	w := postSend(h, p, `{"to":"studio:BETA-1","body":"hi"}`)

	if w.Code != 204 {
		t.Fatalf("status = %d, want 204 (%s)", w.Code, w.Body.String())
	}
	m := app.got[0]
	if m.From != (intercom.Target{Kind: "human", Ref: "alice-b"}) {
		t.Errorf("from = %q, want human:alice-b (the sender's name in the TARGET's project)", m.From.String())
	}
	if m.Project != "beta" {
		t.Errorf("project = %q, want beta", m.Project)
	}
}

func TestParticipantSend_Errors(t *testing.T) {
	t.Run("no participant fails closed", func(t *testing.T) {
		h := jam.NewParticipantSendHandler(meWorld(), &meFakeAppender{}, nil)
		r := httptest.NewRequest("POST", "/me/send", strings.NewReader(`{"to":"human:bob","body":"x"}`))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r) // no WithParticipant
		if w.Code != 401 {
			t.Fatalf("status = %d, want 401", w.Code)
		}
	})
	t.Run("wrong method", func(t *testing.T) {
		h := jam.NewParticipantSendHandler(meWorld(), &meFakeAppender{}, nil)
		r := httptest.NewRequest("GET", "/me/send", nil)
		r = jam.WithParticipant(r, aliceParticipant())
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 405 {
			t.Fatalf("status = %d, want 405", w.Code)
		}
	})
	t.Run("empty body", func(t *testing.T) {
		h := jam.NewParticipantSendHandler(meWorld(), &meFakeAppender{}, nil)
		if w := postSend(h, aliceParticipant(), `{"to":"human:bob","body":""}`); w.Code != 400 {
			t.Fatalf("status = %d, want 400", w.Code)
		}
	})
	t.Run("empty to", func(t *testing.T) {
		h := jam.NewParticipantSendHandler(meWorld(), &meFakeAppender{}, nil)
		if w := postSend(h, aliceParticipant(), `{"to":"","body":"x"}`); w.Code != 400 {
			t.Fatalf("status = %d, want 400", w.Code)
		}
	})
	t.Run("unknown recipient", func(t *testing.T) {
		h := jam.NewParticipantSendHandler(meWorld(), &meFakeAppender{}, nil)
		if w := postSend(h, aliceParticipant(), `{"to":"human:nobody","body":"x"}`); w.Code != 404 {
			t.Fatalf("status = %d, want 404", w.Code)
		}
	})
	t.Run("nil appender → 503", func(t *testing.T) {
		h := jam.NewParticipantSendHandler(meWorld(), nil, nil) // nil appender
		if w := postSend(h, aliceParticipant(), `{"to":"human:bob","body":"x"}`); w.Code != 503 {
			t.Fatalf("status = %d, want 503", w.Code)
		}
	})
	t.Run("append failure → 502", func(t *testing.T) {
		h := jam.NewParticipantSendHandler(meWorld(), &meFakeAppender{err: errors.New("disk full")}, nil)
		if w := postSend(h, aliceParticipant(), `{"to":"human:bob","body":"x"}`); w.Code != 502 {
			t.Fatalf("status = %d, want 502", w.Code)
		}
	})
}

func TestParticipantSend_ContentType(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		code             int
	}{
		{"default", `{"to":"human:bob","body":"**hi**"}`, "", 204},
		{"plain opt-out", `{"to":"human:bob","body":"a_b","content_type":"text/plain"}`, intercom.ContentPlain, 204},
		{"unknown", `{"to":"human:bob","body":"x","content_type":"image/png"}`, "", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := &meFakeAppender{}
			h := jam.NewParticipantSendHandler(meWorld(), app, nil)
			w := postSend(h, aliceParticipant(), tc.body)
			if w.Code != tc.code {
				t.Fatalf("status = %d, want %d (%s)", w.Code, tc.code, w.Body.String())
			}
			if tc.code == 204 && (len(app.got) != 1 || app.got[0].ContentType != tc.want) {
				t.Fatalf("appended = %+v, want content type %q", app.got, tc.want)
			}
			if tc.code != 204 && len(app.got) != 0 {
				t.Fatal("a rejected send must not append")
			}
		})
	}
}
