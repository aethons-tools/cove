package managers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/fleet"
	"github.com/aethons-tools/cove/internal/jam/managers/wire"
)

var (
	_ fleet.Manager        = (*Remote)(nil)
	_ jam.StateKeeper      = (*Remote)(nil)
	_ jam.ImageTagger      = (*Remote)(nil)
	_ jam.KitDraftBuilder  = (*Remote)(nil)
	_ jam.ImageFactsReader = (*Remote)(nil)
)

// lookupTimeout bounds the calls whose interfaces carry no context.
const lookupTimeout = 5 * time.Second

// Remote is a manager reached over a stream the manager opened to Jam. It
// lives as long as the manager is registered; conns come and go under it.
type Remote struct {
	name string
	log  *slog.Logger

	mu   sync.Mutex
	cur  *conn
	tags map[jam.KitRef]string
}

func NewRemote(name string, log *slog.Logger) *Remote {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Remote{name: name, log: log, tags: map[jam.KitRef]string{}}
}

func (r *Remote) Name() string { return r.name }

func (r *Remote) Available() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cur != nil
}

// attach makes c current, closes the previous conn and clears the tag cache.
func (r *Remote) attach(c *conn) {
	r.mu.Lock()
	old := r.cur
	r.cur = c
	r.tags = map[jam.KitRef]string{}
	r.mu.Unlock()
	if old != nil && old != c {
		old.close(errors.New("replaced by a newer stream"))
	}
}

// detach clears the current conn only if it is c.
func (r *Remote) detach(c *conn) {
	r.mu.Lock()
	if r.cur == c {
		r.cur = nil
		r.tags = map[jam.KitRef]string{}
	}
	r.mu.Unlock()
}

func (r *Remote) conn() *conn {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cur
}

func (r *Remote) errUnavailable() error {
	return fmt.Errorf("%w: %s", jam.ErrManagerUnavailable, r.name)
}

// do runs one call on the current conn, or fails as unavailable.
func (r *Remote) do(ctx context.Context, op string, req, resp any) error {
	c := r.conn()
	if c == nil {
		return r.errUnavailable()
	}
	return c.call(ctx, op, req, resp, nil)
}

func (r *Remote) Raise(ctx context.Context, spec jam.RaiseSpec, creds jam.LaunchCreds) (string, error) {
	c := r.conn()
	if c == nil {
		return "", r.errUnavailable()
	}
	spec.Placement = jam.Placement{}
	var res wire.RaiseResult
	if err := c.call(ctx, wire.OpRaise, wire.RaiseRequest{Spec: spec, Creds: creds}, &res, nil); err != nil {
		if errors.Is(err, errInterrupted) {
			return "", fmt.Errorf("%w: manager %s", jam.ErrRaiseInterrupted, r.name)
		}
		return "", err
	}
	return res.Location, nil
}

func (r *Remote) Teardown(ctx context.Context, inst jam.Instance) error {
	return r.do(ctx, wire.OpTeardown, wire.InstanceRequest{Instance: inst}, nil)
}

func (r *Remote) Pause(ctx context.Context, inst jam.Instance) error {
	return r.do(ctx, wire.OpPause, wire.InstanceRequest{Instance: inst}, nil)
}

func (r *Remote) Unpause(ctx context.Context, inst jam.Instance) error {
	return r.do(ctx, wire.OpUnpause, wire.InstanceRequest{Instance: inst}, nil)
}

// Probe never reports a transport error: an unreachable manager is Unknown,
// and Unknown is never reaped.
func (r *Remote) Probe(ctx context.Context, inst jam.Instance) (jam.Liveness, error) {
	c := r.conn()
	if c == nil {
		return jam.LivenessUnknown, nil
	}
	var res wire.ProbeResult
	if err := c.call(ctx, wire.OpProbe, wire.InstanceRequest{Instance: inst}, &res, nil); err != nil {
		return jam.LivenessUnknown, nil
	}
	return res.Liveness, nil
}

func (r *Remote) ApplyEgress(ctx context.Context, inst jam.Instance, p *jam.EgressPolicy) error {
	return r.do(ctx, wire.OpApplyEgress, wire.EgressRequest{Instance: inst, Policy: p}, nil)
}

func (r *Remote) PrepareKit(ctx context.Context, def jam.KitDefinition) (jam.KitStatus, error) {
	enc, err := wire.EncodeKitDefinition(def)
	if err != nil {
		return jam.KitStatus{}, err
	}
	var st jam.KitStatus
	if err := r.do(ctx, wire.OpPrepareKit, wire.KitRequest{Def: enc}, &st); err != nil {
		return jam.KitStatus{}, err
	}
	return st, nil
}

func (r *Remote) PurgeState(ctx context.Context, actorID string) error {
	return r.do(ctx, wire.OpPurgeState, wire.ActorRequest{ActorID: actorID}, nil)
}

func (r *Remote) StateOwners(ctx context.Context) ([]string, error) {
	var res wire.OwnersResult
	if err := r.do(ctx, wire.OpStateOwners, nil, &res); err != nil {
		return nil, err
	}
	return res.ActorIDs, nil
}

// ImageTag names the image a raise of ref runs; "" when disconnected or the
// lookup fails. The answer is cached per ref for the current conn.
func (r *Remote) ImageTag(ref jam.KitRef) string {
	r.mu.Lock()
	c := r.cur
	tag, ok := r.tags[ref]
	r.mu.Unlock()
	if c == nil {
		return ""
	}
	if ok {
		return tag
	}
	ctx, cancel := context.WithTimeout(context.Background(), lookupTimeout)
	defer cancel()
	var res wire.TagResult
	if err := c.call(ctx, wire.OpImageTag, wire.ImageTagRequest{Ref: ref}, &res, nil); err != nil {
		return ""
	}
	r.mu.Lock()
	if r.cur == c {
		r.tags[ref] = res.Tag
	}
	r.mu.Unlock()
	return res.Tag
}

func (r *Remote) BuildKitDraft(ctx context.Context, def jam.KitDefinition, tag string, log io.Writer) (string, error) {
	c := r.conn()
	if c == nil {
		return "", r.errUnavailable()
	}
	enc, err := wire.EncodeKitDefinition(def)
	if err != nil {
		return "", err
	}
	var res wire.DraftBuildResult
	if err := c.call(ctx, wire.OpBuildKitDraft, wire.DraftBuildRequest{Def: enc, Tag: tag}, &res, log); err != nil {
		return "", err
	}
	return res.ImageID, nil
}

func (r *Remote) HasKitDraftImage(imageID string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), lookupTimeout)
	defer cancel()
	var res wire.HasResult
	if err := r.do(ctx, wire.OpHasKitDraftImage, wire.DraftImageRequest{ImageID: imageID}, &res); err != nil {
		return false, err
	}
	return res.Has, nil
}

func (r *Remote) ImageFacts(ctx context.Context, image string) (jam.ImageFacts, error) {
	var f jam.ImageFacts
	if err := r.do(ctx, wire.OpImageFacts, wire.ImageFactsRequest{Image: image}, &f); err != nil {
		return jam.ImageFacts{}, err
	}
	return f, nil
}
