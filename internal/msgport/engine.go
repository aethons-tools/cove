package msgport

import (
	"context"
	"io"
	"log/slog"
	"time"

	"github.com/aethons-tools/cove/internal/msglog"
)

const (
	defaultEgressPoll  = 2 * time.Second
	defaultIngressPoll = 15 * time.Second
)

// Engine bridges the message Log to one external Service (surf). It runs an
// egress loop (deliver outbound) and an ingress loop (append inbound) on
// independent tickers.
//
// seen is populated once by New (before Run is ever called) and thereafter
// mutated ONLY by the ingress loop, which runs on a single goroutine — so no
// other goroutine ever touches seen and no mutex guards it. Do not read or
// write seen from the egress loop or from any other caller.
type Engine struct {
	surf Surface
	lg   msglog.Store
	mk   Markers
	cur  Cursors
	dir  Directory
	cfg  Config
	log  *slog.Logger
	seen map[string]bool // inbound ids already in the Log for surf.Service()
}

// New constructs an Engine for surf, rebuilding seen from the Log's existing
// "in:"+surf.Service()+":" prefixed ids so a restart never re-ingests events
// already recorded. Zero Config fields and a nil logger get defaults.
func New(surf Surface, lg msglog.Store, mk Markers, cur Cursors, dir Directory, cfg Config, log *slog.Logger) *Engine {
	if cfg.EgressPoll <= 0 {
		cfg.EgressPoll = defaultEgressPoll
	}
	if cfg.IngressPoll <= 0 {
		cfg.IngressPoll = defaultIngressPoll
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	e := &Engine{surf: surf, lg: lg, mk: mk, cur: cur, dir: dir, cfg: cfg, log: log, seen: map[string]bool{}}
	prefix := "in:" + surf.Service() + ":"
	for _, id := range lg.SeenIDs(prefix) {
		e.seen[id] = true
	}
	return e
}

// Run drives both loops until ctx is cancelled (egress in a goroutine,
// ingress inline so Run blocks until ingress observes cancellation). The
// egress loop only starts when cfg.EgressEnabled; ingress always runs.
func (e *Engine) Run(ctx context.Context) {
	if e.cfg.EgressEnabled {
		go e.loop(ctx, e.cfg.EgressPoll, e.egressTick)
	}
	e.loop(ctx, e.cfg.IngressPoll, e.ingressTick)
}

// loop runs tick immediately, then again on every interval tick, until
// ctx.Done() fires.
func (e *Engine) loop(ctx context.Context, interval time.Duration, tick func(context.Context)) {
	tick(ctx)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick(ctx)
		}
	}
}
