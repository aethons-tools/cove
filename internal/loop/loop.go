// Package loop drives an at-cove loop: drain all available work, then poll on an
// interval for more. It is pure — the caller injects how to do one unit of work
// (tick) and how to wait (sleep) — so the scheduling is testable without real
// time or ssh.
package loop

import "time"

// Run drives the loop. It drains — calls tick until tick reports false (nothing
// triggered) — then, unless once, sleeps interval and drains again, repeating
// until sleep reports a stop.
//
// tick performs one check-and-maybe-run and returns whether it triggered (did
// work). sleep blocks for d and returns true normally, or false if a stop was
// requested (e.g. a signal), which ends the loop.
func Run(once bool, interval time.Duration, tick func() bool, sleep func(time.Duration) bool) {
	for {
		for tick() {
		}
		if once {
			return
		}
		if !sleep(interval) {
			return
		}
	}
}
