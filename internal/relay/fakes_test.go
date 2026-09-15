package relay

import (
	"context"
	"sync"

	"github.com/aethons-tools/cove/internal/intercom"
)

// fakeSurface records Deliver calls and serves scripted Poll results.
type fakeSurface struct {
	mu            sync.Mutex
	service       string
	delivers      []deliverCall
	deliverErrFor map[string]bool // target.String() -> return an error on Deliver
	events        []Event
	pollErr       error
	next          string
}
type deliverCall struct {
	MsgID      string
	Address    string
	Target     string // resolved target of this delivery, for assertions (set by tests via Directory)
	Sender     string
	BodyPrefix string
}

func (f *fakeSurface) Service() string { return f.service }
func (f *fakeSurface) Deliver(ctx context.Context, d Delivery, m intercom.Squawk) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.delivers = append(f.delivers, deliverCall{MsgID: m.ID, Address: d.Address, Sender: d.SenderName, BodyPrefix: d.BodyPrefix})
	if f.deliverErrFor[d.Address] {
		return "", context.DeadlineExceeded
	}
	return "fid-" + m.ID, nil
}
func (f *fakeSurface) Poll(ctx context.Context, project, since string) ([]Event, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pollErr != nil {
		return nil, since, f.pollErr
	}
	return f.events, f.next, nil
}
func (f *fakeSurface) Close() error      { return nil }
func (f *fakeSurface) deliverCount() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.delivers) }

// fakeMarkers / fakeCursors: in-memory persistence.
type fakeMarkers struct{ m map[string]EgressMark }

func (f *fakeMarkers) Egress(service string) EgressMark { return f.m[service] }
func (f *fakeMarkers) SetEgress(service string, mk EgressMark) error {
	if f.m == nil {
		f.m = map[string]EgressMark{}
	}
	f.m[service] = mk
	return nil
}

type fakeCursors struct{ c map[string]string }

func (f *fakeCursors) Ingress(service, project string) string { return f.c[service+"/"+project] }
func (f *fakeCursors) SetIngress(service, project, cursor string) error {
	if f.c == nil {
		f.c = map[string]string{}
	}
	f.c[service+"/"+project] = cursor
	return nil
}

// fakeDirectory: scripted mapping. projects, resolve (target->Delivery), route (Event->msg).
type fakeDirectory struct {
	projects []string
	// resolve: keyed by target.String(); absent => not owned/unreachable.
	resolve map[string]Delivery
	// route: keyed by Event.ForeignID => (from, to, replyTo); absent => unrouted.
	route map[string]routed
}
type routed struct {
	from    intercom.Target
	to      []intercom.Target
	replyTo string
}

func (f *fakeDirectory) Projects(service string) []string { return f.projects }
func (f *fakeDirectory) Resolve(service, project string, to, from intercom.Target) (Delivery, bool) {
	d, ok := f.resolve[to.String()]
	return d, ok
}
func (f *fakeDirectory) Route(service, project string, e Event) (intercom.Target, []intercom.Target, string, bool) {
	r, ok := f.route[e.ForeignID]
	return r.from, r.to, r.replyTo, ok
}
