package loop

import (
	"testing"
	"time"
)

func TestRunOnceDrainsThenStops(t *testing.T) {
	calls := 0
	tick := func() bool { calls++; return calls < 3 } // triggers twice, then idle
	slept := 0
	sleep := func(time.Duration) bool { slept++; return true }
	Run(true, time.Minute, tick, sleep)
	if calls != 3 {
		t.Fatalf("--once should drain until idle (3 ticks: 2 work + 1 idle), got %d", calls)
	}
	if slept != 0 {
		t.Fatalf("--once must not sleep, slept %d", slept)
	}
}

func TestRunContinuousPollsUntilStop(t *testing.T) {
	ticks := 0
	tick := func() bool { ticks++; return false } // always idle
	sleeps := 0
	sleep := func(time.Duration) bool { sleeps++; return sleeps < 2 } // stop on the 2nd sleep
	Run(false, time.Minute, tick, sleep)
	// drain(1 idle tick) -> sleep#1(true) -> drain(1 idle tick) -> sleep#2(false=stop)
	if ticks != 2 || sleeps != 2 {
		t.Fatalf("ticks=%d sleeps=%d, want 2 and 2", ticks, sleeps)
	}
}

func TestRunContinuousDrainsBeforeSleeping(t *testing.T) {
	// First wake drains 2 triggers then idles; then a stop is requested.
	seq := []bool{true, false} // tick returns: true, false, then false forever
	i := 0
	tick := func() bool {
		v := false
		if i < len(seq) {
			v = seq[i]
		}
		i++
		return v
	}
	sleep := func(time.Duration) bool { return false } // stop immediately after first drain
	Run(false, time.Minute, tick, sleep)
	if i != 2 {
		t.Fatalf("should drain (true then false) before sleeping, ticks=%d", i)
	}
}
