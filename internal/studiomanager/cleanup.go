package studiomanager

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/managers/client"
	"github.com/aethons-tools/cove/internal/numfmt"
)

var _ client.HostCache = (*diskPolicy)(nil)

// The disk policy's timings (docs/usage/jam-colima.md#disk-cleanup).
const (
	// kitImageMinAge keeps kit images this young: one just prepared is
	// about to be raised.
	kitImageMinAge = time.Hour
	// pressureCooldown follows every high or critical cleanup: no high one
	// runs within it, nor a critical one after a critical one, so a disk no
	// cleanup can bring under low is not pruned on every sample.
	pressureCooldown = 5 * time.Minute
	// dailyFirst is when the first daily light pass runs after start;
	// dailyEvery the time between passes.
	dailyFirst = 10 * time.Minute
	dailyEvery = 24 * time.Hour
)

// Cleanup reasons (jam.CleanupRecord.Reason).
const (
	reasonHigh     = "high"
	reasonCritical = "critical"
	reasonDaily    = "daily"
)

// diskPolicy is the manager's HostReporter: it passes each stats sample
// through, and from it decides when to free disk and whether the disk is too
// full to raise on. A sample at or over cfg.High starts a cleanup in the
// background (one at a time), which runs the HostCleaner's steps in order —
// dangling images, unused kit images oldest first, anonymous volumes, the
// build cache — re-measuring after each and stopping once under cfg.Low. The
// daily light pass prunes dangling images and anonymous volumes only. Each
// run is reported as the samples' LastCleanup.
type diskPolicy struct {
	host  jam.HostReporter
	clean jam.HostCleaner // nil: never cleans, only gates
	cfg   CleanupConfig
	log   *slog.Logger
	ctx   context.Context // cleanups run under it, not under a sample's ctx

	changed chan<- struct{}  // nil: none; a non-blocking send after each run
	work    *kitWork         // nil: none tracked; images stay while it is busy
	now     func() time.Time // time.Now

	mu           sync.Mutex
	latest       *jam.HostStats // the last measurement, nil after a failed one
	disk         int            // its disk percent
	known        bool           // it measured the disk
	running      bool
	last         *jam.CleanupRecord
	nextPressure time.Time // pressureCooldown after the last high/critical run
	lastPressure string    // that run's reason
	nextDaily    time.Time
	wg           sync.WaitGroup
}

func newDiskPolicy(ctx context.Context, host jam.HostReporter, clean jam.HostCleaner, cfg CleanupConfig, log *slog.Logger) *diskPolicy {
	return &diskPolicy{host: host, clean: clean, cfg: cfg, log: log, ctx: ctx,
		now: time.Now, nextDaily: time.Now().Add(dailyFirst)}
}

// HostStats measures the host, records its disk percent for the gate,
// starts a cleanup when one is due, and returns the sample with the last
// cleanup's record. It never waits for a cleanup.
func (p *diskPolicy) HostStats(ctx context.Context) (jam.HostStats, error) {
	h, err := p.host.HostStats(ctx)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.record(h, err)
	if err != nil {
		return h, err
	}
	if reason := p.due(); reason != "" {
		p.running = true
		p.wg.Add(1)
		go p.run(reason, h)
	}
	return p.withLast(h), nil
}

// record keeps a measurement for the gate and the cache; a failed one
// clears them, so raises are never declined on a stale sample. p.mu is held.
func (p *diskPolicy) record(h jam.HostStats, err error) {
	if err != nil {
		p.latest, p.known = nil, false
		return
	}
	p.latest = &h
	p.disk, p.known = h.DiskPercent()
}

// withLast is h with the last cleanup's record. p.mu is held.
func (p *diskPolicy) withLast(h jam.HostStats) jam.HostStats {
	if p.last != nil {
		rec := *p.last
		h.LastCleanup = &rec
	}
	return h
}

// CachedHostStats is the latest measurement (a sample's or a cleanup's) with
// the last cleanup's record, so the report a cleanup asks for carries it.
func (p *diskPolicy) CachedHostStats() (jam.HostStats, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.latest == nil {
		return jam.HostStats{}, false
	}
	return p.withLast(*p.latest), true
}

// due is the reason a cleanup should start now, "" for none. p.mu is held.
func (p *diskPolicy) due() string {
	if p.clean == nil || p.cfg.Disabled || p.running {
		return ""
	}
	now := p.now()
	if p.known && p.disk >= p.cfg.High {
		reason := reasonHigh
		if p.disk >= p.cfg.Critical {
			reason = reasonCritical
		}
		// Every high or critical run holds the next high one for
		// pressureCooldown; a critical one is held only by a critical run.
		cooling := now.Before(p.nextPressure)
		if reason == reasonHigh && !cooling || reason == reasonCritical && !(cooling && p.lastPressure == reasonCritical) {
			return reason
		}
	}
	if p.cfg.DailyPass() && !now.Before(p.nextDaily) {
		return reasonDaily
	}
	return ""
}

// full reports the last sample's disk percent and whether it is at or over
// cfg.Critical (raises are declined); an unmeasured disk is not full.
func (p *diskPolicy) full() (int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.disk, p.known && p.disk >= p.cfg.Critical
}

// wait blocks until no cleanup is running (tests).
func (p *diskPolicy) wait() { p.wg.Wait() }

// errStop ends a run early: under low, or the disk cannot be measured.
var errStop = errors.New("stop")

// run is one cleanup, from the sample before that started it.
func (p *diskPolicy) run(reason string, before jam.HostStats) {
	defer p.wg.Done()
	ctx := p.ctx
	rec := jam.CleanupRecord{At: p.now(), Reason: reason}
	after := before
	var errs []error
	light := reason == reasonDaily
	// check re-measures after a step: errStop once under low (or the disk
	// cannot be measured), so the run ends.
	check := func() error {
		h, err := p.host.HostStats(ctx)
		p.mu.Lock()
		p.record(h, err)
		p.mu.Unlock()
		if err != nil {
			errs = append(errs, err)
			return errStop
		}
		after = h
		pct, ok := h.DiskPercent()
		if ok && pct < p.cfg.Low && !light {
			return errStop
		}
		return nil
	}
	steps := []struct {
		name  string
		light bool
		do    func() error
	}{
		{"dangling images", true, func() error { return p.pruneDangling(ctx) }},
		{"kit images", false, func() error { return p.removeKitImages(ctx, check) }},
		{"anonymous volumes", true, func() error { return p.clean.PruneVolumes(ctx) }},
		{"build cache", false, func() error { return p.clean.PruneBuildCache(ctx) }},
	}
	for _, s := range steps {
		if light && !s.light {
			continue
		}
		err := s.do()
		if errors.Is(err, errStop) {
			break
		}
		if err != nil {
			errs = append(errs, err)
		}
		if check() != nil {
			break
		}
	}
	if before.DiskUsed > after.DiskUsed {
		rec.Freed = before.DiskUsed - after.DiskUsed
	}
	if err := errors.Join(errs...); err != nil {
		rec.Err = err.Error()
	}

	now := p.now()
	p.mu.Lock()
	p.last, p.running = &rec, false
	if light {
		p.nextDaily = now.Add(dailyEvery)
	} else {
		p.nextPressure, p.lastPressure = now.Add(pressureCooldown), reason
	}
	p.mu.Unlock()

	pctBefore, _ := before.DiskPercent()
	pctAfter, _ := after.DiskPercent()
	attrs := []any{"reason", reason, "freed", numfmt.Bytes(rec.Freed), "disk_before", pctBefore, "disk_after", pctAfter}
	if rec.Err != "" {
		p.log.Warn("manager: disk cleanup had errors", append(attrs, "err", rec.Err)...)
	} else {
		p.log.Info("manager: disk cleanup", attrs...)
	}
	if p.changed != nil {
		select {
		case p.changed <- struct{}{}:
		default: // a report is already pending
		}
	}
}

// kitWork counts the work in flight that needs images left alone — raises,
// kit prepares, draft builds — and lets the disk policy remove images only
// while there is none, atomically: work cannot start mid-removal.
type kitWork struct {
	mu sync.Mutex
	n  int
}

// start counts one piece of work in (waiting out a removal in progress);
// the returned func counts it out.
func (w *kitWork) start() func() {
	w.mu.Lock()
	w.n++
	w.mu.Unlock()
	return func() {
		w.mu.Lock()
		w.n--
		w.mu.Unlock()
	}
}

// whileIdle runs f, holding off new work across it, when no work is in
// flight; false (f not run) when some is.
func (w *kitWork) whileIdle(f func()) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.n > 0 {
		return false
	}
	f()
	return true
}

// idle runs f via p.work.whileIdle, or at once when no work is tracked.
func (p *diskPolicy) idle(f func()) bool {
	if p.work == nil {
		f()
		return true
	}
	return p.work.whileIdle(f)
}

// pruneDangling prunes dangling images unless kit work is in flight (a
// build's image may not be tagged yet).
func (p *diskPolicy) pruneDangling(ctx context.Context) error {
	var err error
	if !p.idle(func() { err = p.clean.PruneDanglingImages(ctx) }) {
		p.log.Debug("manager: disk cleanup skips dangling images: a kit build or raise is in flight")
	}
	return err
}

// removeKitImages removes unused kit images oldest first, one at a time,
// calling check after each removal; it skips images in use or younger than
// kitImageMinAge, and stops once kit work is in flight (each removal runs
// with new work held off). An image the runtime refuses to remove is skipped
// (an image whose child is still there, say).
func (p *diskPolicy) removeKitImages(ctx context.Context, check func() error) error {
	if !p.idle(func() {}) {
		p.log.Debug("manager: disk cleanup skips kit images: a kit build or raise is in flight")
		return nil
	}
	imgs, err := p.clean.KitImages(ctx)
	if err != nil {
		return err
	}
	cutoff := p.now().Add(-kitImageMinAge)
	imgs = slices.DeleteFunc(imgs, func(k jam.KitImage) bool { return k.InUse || k.Created.After(cutoff) })
	slices.SortStableFunc(imgs, func(a, b jam.KitImage) int { return a.Created.Compare(b.Created) })
	for _, img := range imgs {
		var err error
		if !p.idle(func() { err = p.clean.RemoveImage(ctx, img.ID) }) {
			p.log.Debug("manager: disk cleanup stops removing kit images: a kit build or raise started")
			return nil
		}
		if err != nil {
			p.log.Debug("manager: disk cleanup kept a kit image", "image", img.ID, "tags", img.Tags, "err", err)
			continue
		}
		p.log.Debug("manager: disk cleanup removed a kit image", "image", img.ID, "tags", img.Tags, "size", numfmt.Bytes(img.Size))
		if err := check(); err != nil {
			return err
		}
	}
	return nil
}
