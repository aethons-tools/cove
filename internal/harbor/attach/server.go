// Package attach is the harbor-side managed-cove Attach gRPC stream: a cove dials
// in and holds one bidirectional stream — StatusUp from the cove (activity +
// heartbeats), ControlDown from harbor (lifecycle control). grpc is isolated to
// this package; internal/harbor stays grpc-free.
package attach

import (
	"context"
	"log/slog"
	"strings"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/aethons-tools/cove/internal/harbor"
	"github.com/aethons-tools/cove/internal/harbor/attach/attachpb"
)

const (
	mdAuthorization = "authorization"
	mdLaunchSecret  = "x-harbor-launch-secret"
	sendBuffer      = 8
)

type Server struct {
	attachpb.UnimplementedRuntimeServer
	store harbor.Store
	sup   *harbor.Supervisor
	log   *slog.Logger
	mu    sync.Mutex
	conns map[string]chan *attachpb.ControlDown
}

var _ harbor.ControlSink = (*Server)(nil)

func NewServer(store harbor.Store, sup *harbor.Supervisor, log *slog.Logger) *Server {
	return &Server{store: store, sup: sup, log: log, conns: map[string]chan *attachpb.ControlDown{}}
}

// authenticate resolves the actorID from stream metadata: bearer identity token
// (→ Actor) AND the per-instance launch secret (→ Instance.LaunchSecretHash).
func (s *Server) authenticate(ctx context.Context) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", status.Error(codes.Unauthenticated, "missing metadata")
	}
	token, secret := bearer(md.Get(mdAuthorization)), first(md.Get(mdLaunchSecret))
	if token == "" || secret == "" {
		return "", status.Error(codes.Unauthenticated, "missing identity token or launch secret")
	}
	actor, ok := s.store.Lookup(harbor.HashToken(token))
	if !ok {
		return "", status.Error(codes.Unauthenticated, "unknown identity")
	}
	inst, ok := s.store.GetInstance(actor.ID)
	if !ok {
		return "", status.Error(codes.Unauthenticated, "no live instance")
	}
	if inst.LaunchSecretHash == "" || harbor.HashToken(secret) != inst.LaunchSecretHash {
		return "", status.Error(codes.Unauthenticated, "bad launch secret")
	}
	return actor.ID, nil
}

func (s *Server) Attach(stream attachpb.Runtime_AttachServer) error {
	actorID, err := s.authenticate(stream.Context())
	if err != nil {
		return err
	}
	ch := s.register(actorID)
	defer s.deregister(actorID, ch)
	_ = s.sup.Heartbeat(actorID) // the cove is talking to us now

	go func() { // send loop
		for {
			select {
			case <-stream.Context().Done():
				return
			case cd, ok := <-ch:
				if !ok {
					return
				}
				if err := stream.Send(cd); err != nil {
					return
				}
			}
		}
	}()

	for { // recv loop
		msg, err := stream.Recv()
		if err != nil {
			return err // EOF / client gone; defer deregisters
		}
		switch m := msg.GetMsg().(type) {
		case *attachpb.StatusUp_Status:
			if act, ok := fromPBActivity(m.Status); ok {
				_ = s.sup.Report(stream.Context(), actorID, act)
			}
		case *attachpb.StatusUp_Heartbeat:
			_ = s.sup.Heartbeat(actorID)
		}
	}
}

// register installs a fresh send channel for actorID, superseding (and closing)
// any prior one — a reconnect replaces the old stream (the lease-steal analog).
// The map always points at an OPEN channel.
func (s *Server) register(actorID string) chan *attachpb.ControlDown {
	ch := make(chan *attachpb.ControlDown, sendBuffer)
	s.mu.Lock()
	old := s.conns[actorID]
	s.conns[actorID] = ch
	s.mu.Unlock()
	if old != nil {
		close(old) // old send loop sees !ok and exits; map no longer references it
	}
	return ch
}

func (s *Server) deregister(actorID string, ch chan *attachpb.ControlDown) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conns[actorID] == ch {
		delete(s.conns, actorID)
		close(ch)
	}
}

// enqueue is the ControlSink delivery primitive: non-blocking send under the lock
// (so the channel can't be closed mid-send); drop if full or no stream.
func (s *Server) enqueue(actorID string, cd *attachpb.ControlDown) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, ok := s.conns[actorID]
	if !ok {
		return
	}
	select {
	case ch <- cd:
	default: // buffer full — drop (best-effort)
	}
}

// RequestTeardown and Wake implement harbor.ControlSink.
func (s *Server) RequestTeardown(actorID string) {
	s.enqueue(actorID, &attachpb.ControlDown{Msg: &attachpb.ControlDown_Teardown{Teardown: &attachpb.Teardown{}}})
}
func (s *Server) Wake(actorID string) {
	s.enqueue(actorID, &attachpb.ControlDown{Msg: &attachpb.ControlDown_Wake{Wake: &attachpb.Wake{}}})
}

// connected reports whether a live Attach stream is registered for actorID.
func (s *Server) connected(actorID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.conns[actorID]
	return ok
}

func fromPBActivity(a attachpb.Activity) (harbor.Activity, bool) {
	switch a {
	case attachpb.Activity_RUNNING:
		return harbor.ActivityRunning, true
	case attachpb.Activity_WAITING:
		return harbor.ActivityWaiting, true
	case attachpb.Activity_BLOCKED:
		return harbor.ActivityBlocked, true
	case attachpb.Activity_DONE:
		return harbor.ActivityDone, true
	}
	return "", false
}

func first(vals []string) string {
	if len(vals) > 0 {
		return vals[0]
	}
	return ""
}
func bearer(vals []string) string {
	v := first(vals)
	const p = "bearer "
	if len(v) >= len(p) && strings.EqualFold(v[:len(p)], p) {
		return strings.TrimSpace(v[len(p):])
	}
	return v
}
