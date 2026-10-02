package sessionevents_test

import (
	"context"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/sessionevents"
)

func TestParseRetention(t *testing.T) {
	cases := map[string]time.Duration{"": 0, "90d": 90 * 24 * time.Hour, "36h": 36 * time.Hour}
	for in, want := range cases {
		got, err := sessionevents.ParseRetention(in)
		if err != nil || got != want {
			t.Errorf("%q: got %v %v want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"soon", "-1h", "xd", "-3d"} {
		if _, err := sessionevents.ParseRetention(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

func TestRunRetentionSweepsOnStart(t *testing.T) {
	st, _ := sessionevents.OpenFileStore(t.TempDir())
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	st.Append(sessionevents.Event{ActorID: "w1", StreamID: sid, Seq: 1, Kind: sessionevents.KindEvent, ReceivedAt: old, Raw: []byte(`{}`)})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		sessionevents.RunRetention(ctx, st, 24*time.Hour, time.Hour, func() time.Time { return old.Add(72 * time.Hour) }, nil)
		close(done)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got, _ := st.List(sessionevents.Filter{ActorID: "w1", StreamID: sid}); len(got) == 0 {
			cancel()
			<-done
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	t.Fatal("retention did not sweep on start")
}
