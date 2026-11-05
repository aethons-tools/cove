package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/managers/wire"
)

// HostCache is an optional extension of Config.Host: its latest stats,
// without measuring; false when it has none (nothing measured yet, or the
// last measurement failed).
type HostCache interface {
	CachedHostStats() (jam.HostStats, bool)
}

// HostStatsTimeout bounds one host measurement (a colima ssh and a docker
// system df), under wire.StatusEvery: a hung one fails, reporting no host,
// rather than stopping the reports.
const HostStatsTimeout = 20 * time.Second

// reportStatus sends OpStatus reports until ctx ends: one after the first
// host measurement, one per later measurement (every StatusEvery), and one
// per StatusChanged signal, no sooner than statusMinGap after the last
// report. Measuring runs on its own goroutine (counted in wg), so a signal
// reuses the last host stats — or a HostCache's latest, which may carry news
// such as a finished disk cleanup — and never waits on a measurement;
// signals before the first measurement are covered by its report.
func (c *conn) reportStatus(ctx context.Context, wg *sync.WaitGroup) {
	cfg := c.cfg
	ctx, cancel := context.WithCancel(ctx)
	defer cancel() // stops the measurer if a send fails first

	// measured delivers each measurement's host (nil: none or failed).
	measured := make(chan *jam.HostStats)
	wg.Add(1)
	go func() {
		defer wg.Done()
		tick := time.NewTicker(cfg.StatusEvery)
		defer tick.Stop()
		failing := false
		for {
			var host *jam.HostStats
			host, failing = c.measure(ctx, failing)
			select {
			case measured <- host:
			case <-ctx.Done():
				return
			}
			select {
			case <-tick.C:
			case <-ctx.Done():
				return
			}
		}
	}()

	var (
		host  *jam.HostStats
		first = true // no measurement delivered yet
		last  time.Time
	)
	// cached takes a HostCache's latest stats, when it has any, for an
	// on-demand report.
	cached := func() {
		if hc, ok := cfg.Host.(HostCache); ok {
			if h, ok := hc.CachedHostStats(); ok {
				host = &h
			}
		}
	}
	send := func() bool {
		r := wire.StatusReport{Host: host}
		if cfg.Activities != nil {
			r.Activities = cfg.Activities()
		}
		data, err := json.Marshal(r)
		if err != nil {
			cfg.Log.Warn("manager: encode status", "err", err)
			return true
		}
		last = time.Now()
		if err := c.sendFrames(wire.Split(0, wire.OpStatus, data)); err != nil {
			cfg.Log.Debug("manager: send status failed", "err", err)
			return false
		}
		return true
	}

	changed := cfg.StatusChanged
	// due fires a throttled on-demand report; nil while none is pending.
	var due <-chan time.Time
	var dueTimer *time.Timer
	defer func() {
		if dueTimer != nil {
			dueTimer.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case host = <-measured:
			first = false
			if dueTimer != nil {
				dueTimer.Stop()
			}
			due = nil // this report covers any pending signal
			if !send() {
				return
			}
		case _, ok := <-changed:
			if !ok {
				changed = nil // closed: no more signals, never spin on it
				continue
			}
			if due != nil || first {
				continue // already pending, or the first report will cover it
			}
			wait := cfg.statusMinGap - time.Since(last)
			if wait <= 0 {
				cached()
				if !send() {
					return
				}
				continue
			}
			dueTimer = time.NewTimer(wait)
			due = dueTimer.C
		case <-due:
			due = nil
			cached()
			if !send() {
				return
			}
		}
	}
}

// measure takes one host measurement, bounded by hostStatsTimeout; with no
// Host it reports none. failing says the previous one failed: a run of
// failures warns once, the rest at debug. It returns the host (nil on
// failure) and whether this one failed.
func (c *conn) measure(ctx context.Context, failing bool) (*jam.HostStats, bool) {
	cfg := c.cfg
	if cfg.Host == nil {
		return nil, false
	}
	mctx, cancel := context.WithTimeout(ctx, cfg.hostStatsTimeout)
	h, err := cfg.Host.HostStats(mctx)
	timedOut := errors.Is(mctx.Err(), context.DeadlineExceeded)
	cancel()
	if err == nil {
		return &h, false
	}
	if ctx.Err() != nil {
		return nil, failing
	}
	if timedOut {
		err = fmt.Errorf("timed out after %s: %w", cfg.hostStatsTimeout, err)
	}
	if !failing {
		cfg.Log.Warn("manager: host stats failed; reporting none", "err", err)
	} else {
		cfg.Log.Debug("manager: host stats failed", "err", err)
	}
	return nil, true
}
