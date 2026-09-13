package harbor

import (
	"encoding/json"
	"testing"
	"time"
)

func TestInstanceJSONRoundTrip(t *testing.T) {
	in := Instance{
		ActorID: "spider-42", Project: "acme", Role: "guest", Unit: "AET-7",
		Backend: "", Location: "placeholder:spider-42",
		Phase: PhaseLive, Activity: ActivityWaiting,
		Lease:    Lease{Holder: "host/1/abcd", Expiry: time.Unix(1000, 0).UTC()},
		RaisedAt: time.Unix(10, 0).UTC(), LastSeen: time.Unix(20, 0).UTC(),
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Instance
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", out, in)
	}
}

func TestPhaseActivityConstants(t *testing.T) {
	// Guards the wire strings callers and the gRPC slice will depend on.
	cases := map[string]string{
		string(PhaseRaising): "raising", string(PhaseLive): "live",
		string(PhaseTerminating): "terminating", string(PhaseGone): "gone", string(PhaseLost): "lost",
		string(ActivityRunning): "running", string(ActivityWaiting): "waiting",
		string(ActivityBlocked): "blocked", string(ActivityDone): "done",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("constant = %q, want %q", got, want)
		}
	}
}
