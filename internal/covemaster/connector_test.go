package covemaster

import (
	"context"
	"testing"
	"time"
)

// connWorkload reports a connector fingerprint, then blocks until released.
type connWorkload struct {
	fp      string
	release chan struct{}
}

func (w *connWorkload) Run(ctx context.Context, h Handle) error {
	h.Report(Running)
	h.ConnectorApplied(w.fp)
	select {
	case <-w.release:
	case <-ctx.Done():
	}
	return nil
}
func (w *connWorkload) Control(Control) {}

func (f *fakeRuntime) connectorList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.connectors...)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestConnectorAppliedSent(t *testing.T) {
	f := &fakeRuntime{autoAck: true}
	c := fakeClient(t, f, nil)
	w := &connWorkload{fp: "fp-1", release: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- c.Run(context.Background(), w) }()
	waitFor(t, func() bool { l := f.connectorList(); return len(l) > 0 && l[len(l)-1] == "fp-1" })
	close(w.release)
	<-done
}

func TestConnectorAppliedResentOnReconnect(t *testing.T) {
	f := &fakeRuntime{autoAck: true, dropOnConnector: true}
	c := fakeClient(t, f, nil)
	w := &connWorkload{fp: "fp-1", release: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- c.Run(context.Background(), w) }()
	waitFor(t, func() bool { return len(f.connectorList()) >= 2 })
	close(w.release)
	<-done
	for _, fp := range f.connectorList() {
		if fp != "fp-1" {
			t.Fatalf("unexpected fingerprint %q", fp)
		}
	}
}
