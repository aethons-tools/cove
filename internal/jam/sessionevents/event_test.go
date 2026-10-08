package sessionevents

import (
	"encoding/json"
	"testing"
	"time"
)

func TestValidStreamID(t *testing.T) {
	for _, ok := range []string{"0123456789abcdef0123456789abcdef"} {
		if !ValidStreamID(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "../../etc/passwd", "0123456789ABCDEF0123456789ABCDEF", "0123456789abcdef0123456789abcde", "0123456789abcdef0123456789abcdef0"} {
		if ValidStreamID(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}

func TestEventJSONRoundTripValidRaw(t *testing.T) {
	in := Event{ActorID: "w1", StreamID: "s", Seq: 3, Kind: KindEvent, Turn: 2,
		ReceivedAt: time.Unix(10, 0).UTC(), Raw: []byte(`{"type":"result","x":"<b>"}`),
		Stamp: Stamp{Project: "p"}, Index: Index{Type: "result", CostUSD: 0.5}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(b, &m)
	if _, ok := m["raw"].(map[string]any); !ok || m["raw_text"] != nil {
		t.Fatalf("valid JSON must encode as an object under raw: %s", b)
	}
	var out Event
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if !RawEqual(out.Raw, in.Raw) || out.Seq != 3 || out.Stamp.Project != "p" || out.Index.CostUSD != 0.5 {
		t.Fatalf("round trip: %+v", out)
	}
}

func TestEventJSONRoundTripInvalidRaw(t *testing.T) {
	in := Event{ActorID: "w1", StreamID: "s", Seq: 1, Kind: KindEvent, Raw: []byte(`{"type":"assist`), TruncatedBytes: 99}
	b, _ := json.Marshal(in)
	var m map[string]any
	json.Unmarshal(b, &m)
	if m["raw_text"] != `{"type":"assist` || m["raw"] != nil {
		t.Fatalf("invalid JSON must encode under raw_text: %s", b)
	}
	var out Event
	json.Unmarshal(b, &out)
	if string(out.Raw) != `{"type":"assist` || out.TruncatedBytes != 99 {
		t.Fatalf("round trip: %+v", out)
	}
}
