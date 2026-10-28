package adminui

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

// When the live Jam cannot be compared, Tracking says so: every mark is
// listed with an unknown state, none counts as drifted, and the reason is
// logged.
func TestBuildConfigPageStatesUnknown(t *testing.T) {
	st := jam.NewMemStore()
	at := time.Unix(1000, 0).UTC()
	if err := st.ApplyConfig(jam.ConfigWrite{Marks: []jam.ConfigMark{
		{Kind: jam.ConfigKindKit, Name: "web", Commit: "abc1234def", Digest: "d", AppliedAt: at},
		{Kind: jam.ConfigKindDestination, Name: "gh", Commit: "fedcba9876", Digest: "d", AppliedAt: at.Add(time.Hour)},
	}}); err != nil {
		t.Fatal(err)
	}
	prev := configMarkStates
	configMarkStates = func(jam.Store) ([]jam.ConfigMarkState, error) {
		return nil, errors.New("config drift: kit x cannot be rendered")
	}
	defer func() { configMarkStates = prev }()
	var logs bytes.Buffer
	d := buildConfigPage(st, false, slog.New(slog.NewTextHandler(&logs, nil)))
	if !d.StatesErr || d.Drifted != 0 || len(d.Rows) != 2 || d.LastCommit != "fedcba9" || d.LastApplied == "" {
		t.Fatalf("page = %+v", d)
	}
	for _, r := range d.Rows {
		if r.State != "" || r.Href == "" {
			t.Errorf("row = %+v, want an unknown state and its page", r)
		}
	}
	if !strings.Contains(logs.String(), "ui config: drift not computed") || !strings.Contains(logs.String(), "kit x cannot be rendered") {
		t.Errorf("log = %s", logs.String())
	}
	tr := trackingOf(st, d)
	if !tr.StatesErr || tr.Rows[0].Path == "" {
		t.Errorf("tracking = %+v", tr)
	}
}
