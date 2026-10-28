package adminui_test

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/adminui"
	"github.com/aethons-tools/cove/internal/jam/sessionevents"
	"github.com/aethons-tools/cove/internal/jam/userui"
)

func jsonKeys(v any) []string {
	var out []string
	t := reflect.TypeOf(v)
	for i := range t.NumField() {
		out = append(out, strings.Split(t.Field(i).Tag.Get("json"), ",")[0])
	}
	slices.Sort(out)
	return out
}

// The timeline's events are the Thinking tab's, field for field.
func TestSessionEventMatchesThinkingEvent(t *testing.T) {
	if a, u := jsonKeys(adminui.SessionEvent{}), jsonKeys(userui.ThinkingEvent{}); !slices.Equal(a, u) {
		t.Errorf("SessionEvent keys %v, ThinkingEvent keys %v", a, u)
	}
}

func TestSessionAPI(t *testing.T) {
	h, _, _, ing := sessionUI(t)
	for i := uint64(1); i <= 3; i++ {
		ing.Append("w1", sessionevents.Stamp{}, sessIn(i, `{"type":"assistant","message":{"content":[{"type":"text","text":"line"}]}}`))
	}
	var info adminui.SessionInfo
	apiDo(t, h, "GET", "/app/admin/api/agents/w1/session", nil, &info)
	if !info.Enabled || len(info.Streams) != 1 || info.Stream != sessSID {
		t.Fatalf("info = %+v", info)
	}
	var page adminui.SessionPage
	apiDo(t, h, "GET", "/app/admin/api/agents/w1/session/page?stream="+sessSID+"&before=3", nil, &page)
	if len(page.Events) != 2 || page.Events[0].Seq != 1 || page.Events[1].Text == "" {
		t.Fatalf("page = %+v", page)
	}
	var raw adminui.SessionRaw
	apiDo(t, h, "GET", "/app/admin/api/agents/w1/session/raw?stream="+sessSID+"&seq=2", nil, &raw)
	if !strings.Contains(raw.Raw, `"assistant"`) {
		t.Errorf("raw = %+v", raw)
	}
	stop := sse(t, h, "/app/admin/api/agents/w1/session/events?stream="+sessSID, "")
	time.Sleep(150 * time.Millisecond)
	body := stop()
	var first adminui.SessionEvent
	for _, l := range strings.Split(body, "\n") {
		if d, ok := strings.CutPrefix(l, "data: "); ok && strings.HasPrefix(d, "{\"stream\"") {
			if err := json.Unmarshal([]byte(d), &first); err != nil {
				t.Fatalf("ev data %q: %v", d, err)
			}
			break
		}
	}
	if first.Seq == 0 || first.Stream != sessSID {
		t.Errorf("no ev rows in:\n%s", body)
	}
}

func TestSessionAPINotConfigured(t *testing.T) {
	h, _ := adminHandler(t)
	var info adminui.SessionInfo
	apiDo(t, h, "GET", "/app/admin/api/agents/w1/session", nil, &info)
	if info.Enabled {
		t.Error("enabled without a store")
	}
	if rec := apiDo(t, h, "GET", "/app/admin/api/agents/w1/session/events", nil, nil); rec.Code != 204 {
		t.Errorf("events = %d", rec.Code)
	}
}
