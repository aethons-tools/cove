package client

import (
	"context"
	"encoding/json"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/managers/wire"
)

// reportStatus sends OpStatus reports until ctx ends: one at once, one every
// StatusEvery (each measuring the host afresh), and one per StatusChanged
// signal, no sooner than statusMinGap after the last report; a signal
// reuses the last host stats, so activity changes never wait on a
// measurement.
func (c *conn) reportStatus(ctx context.Context) {
	cfg := c.cfg
	var (
		host    *jam.HostStats
		failing bool
		last    time.Time
	)
	measure := func() {
		if cfg.Host == nil {
			return
		}
		h, err := cfg.Host.HostStats(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// Warn once per run of failures; the rest at debug.
			if !failing {
				cfg.Log.Warn("manager: host stats failed; reporting none", "err", err)
			} else {
				cfg.Log.Debug("manager: host stats failed", "err", err)
			}
			failing, host = true, nil
			return
		}
		failing, host = false, &h
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

	measure()
	if !send() {
		return
	}
	tick := time.NewTicker(cfg.StatusEvery)
	defer tick.Stop()
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
		case <-tick.C:
			measure()
			if ctx.Err() != nil {
				return
			}
			due = nil // this report covers any pending signal
			if !send() {
				return
			}
		case <-cfg.StatusChanged:
			if due != nil {
				continue // already pending
			}
			wait := cfg.statusMinGap - time.Since(last)
			if wait <= 0 {
				if !send() {
					return
				}
				continue
			}
			dueTimer = time.NewTimer(wait)
			due = dueTimer.C
		case <-due:
			due = nil
			if !send() {
				return
			}
		}
	}
}
