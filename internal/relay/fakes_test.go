package relay

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
)

// fakeSurface records Deliver calls and serves scripted Poll results.
type fakeSurface struct {
	mu            sync.Mutex
	service       string
	delivers      []deliverCall
	deliverErrFor map[string]bool // Delivery.Address -> return an error on Deliver
	events        []Event
	pollErr       error
	next          string
}
type deliverCall struct {
	MsgID      string
	Address    string
	BodyPrefix string
}

func (f *fakeSurface) Service() string { return f.service }
func (f *fakeSurface) Deliver(ctx context.Context, d Delivery, m intercom.Squawk) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.delivers = append(f.delivers, deliverCall{MsgID: m.ID, Address: d.Address, BodyPrefix: d.BodyPrefix})
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

// fakeDirectory: scripted mapping. surfaces keyed by channel; route keyed by
// Event.ForeignID; posts go to lg with audience (a fixed reader), or fail.
type fakeDirectory struct {
	projects  []string
	surfaces  map[ident.ID][]Delivery // channel → its surfaces (origin-filtered like the real one)
	route     map[string]Routed
	lg        intercom.Store
	audience  []ident.ID
	postErr   bool
	permanent bool
}

func (f *fakeDirectory) Projects(service string) []string { return f.projects }
func (f *fakeDirectory) Surfaces(service string, m intercom.Squawk) []Delivery {
	var out []Delivery
	for _, d := range f.surfaces[m.Channel] {
		if m.Origin == "con_origin" && d.Address == m.OriginRef {
			continue
		}
		out = append(out, d)
	}
	return out
}
func (f *fakeDirectory) Route(service, project string, e Event) (Routed, bool) {
	r, ok := f.route[e.ForeignID]
	return r, ok
}
func (f *fakeDirectory) Post(r Routed, m intercom.Squawk) error {
	if f.postErr {
		return errors.New("store down")
	}
	if f.permanent {
		return fmt.Errorf("%w: channel archived", ErrPermanent)
	}
	m.Channel = r.Channel
	_, err := f.lg.Append(m, f.audience)
	return err
}
