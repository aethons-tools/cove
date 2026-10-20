// Package attach is the Jam-side managed-cove Attach gRPC stream: a cove dials
// in and holds one bidirectional stream — StatusUp from the cove (activity +
// heartbeats), ControlDown from Jam (lifecycle control). grpc is isolated to
// this package; internal/jam stays grpc-free.
package attach

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/attach/attachpb"
	"github.com/aethons-tools/cove/internal/jam/sessionevents"
)

const (
	mdAuthorization = "authorization"
	// mdLaunchSecret keeps its pre-rename name: it is the gRPC metadata key
	// cove-master in already-built images sends (see
	// docs/usage/jam/renamed-from-harbor.md).
	mdLaunchSecret = "x-harbor-launch-secret"
	sendBuffer     = 8
)

type Server struct {
	attachpb.UnimplementedRuntimeServer
	store jam.Store
	sup   *jam.Supervisor
	log   *slog.Logger
	mu    sync.Mutex
	conns map[string]chan *attachpb.ControlDown
	ports map[string][]int32 // latest exposed-port set per actor (absent = none known)
	// exposureSource seeds an actor's set on connect when none is stored
	// (called under mu, so it must not call back into the Server).
	exposureSource func(actorID string) []int32
	// portsOwed marks connected actors whose stream has not yet been sent the
	// current port set; portsNotify wakes that stream's send loop (buffer 1).
	// Delivery is coalesced and lossless: the send loop sends the latest set
	// before the next control message, so it precedes any later OpenTunnel.
	portsOwed   map[string]bool
	portsNotify map[string]chan struct{}

	events  *sessionevents.Ingest
	tunnels *TunnelBroker
}

// ackEvery is how often a connection flushes cumulative EventAcks.
var ackEvery = 250 * time.Millisecond

// SetSessionEvents enables session-event ingest. Call before serving. Without
// it, events are ignored (never acked) — at-jam always sets one, using a
// no-op store when storage is not configured.
func (s *Server) SetSessionEvents(in *sessionevents.Ingest) { s.events = in }

// ackState collects the latest durable seq per stream for one connection.
type ackState struct {
	mu      sync.Mutex
	pending map[string]uint64
}

func (a *ackState) set(stream string, seq uint64) {
	a.mu.Lock()
	a.pending[stream] = seq
	a.mu.Unlock()
}

func (a *ackState) drain() map[string]uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.pending
	a.pending = map[string]uint64{}
	return out
}

// stampOf labels a session's events; the project by name (the instance holds
// its id).
func stampOf(store jam.Store, inst jam.Instance) sessionevents.Stamp {
	pid, _ := jam.ProjectIDOf(store, inst.Project)
	return sessionevents.Stamp{Project: jam.ProjectName(store, inst.Project), ProjectID: string(pid), OwnerID: string(inst.OwnerID), Role: inst.Role, Unit: inst.Unit, Owner: inst.Owner,
		SessionKind: inst.SessionKind, RaisedAt: inst.RaisedAt}
}

var _ jam.ControlSink = (*Server)(nil)
var _ jam.ExposureSink = (*Server)(nil)

func NewServer(store jam.Store, sup *jam.Supervisor, log *slog.Logger) *Server {
	return &Server{store: store, sup: sup, log: log, conns: map[string]chan *attachpb.ControlDown{}, ports: map[string][]int32{}, portsOwed: map[string]bool{}, portsNotify: map[string]chan struct{}{}}
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
	actor, ok := s.store.Lookup(jam.HashToken(token))
	if !ok {
		return "", status.Error(codes.Unauthenticated, "unknown identity")
	}
	inst, ok := s.store.GetInstance(actor.ID)
	if !ok {
		return "", status.Error(codes.Unauthenticated, "no live instance")
	}
	if inst.LaunchSecretHash == "" || jam.HashToken(secret) != inst.LaunchSecretHash {
		return "", status.Error(codes.Unauthenticated, "bad launch secret")
	}
	return actor.ID, nil
}

func (s *Server) Attach(stream attachpb.Runtime_AttachServer) error {
	actorID, err := s.authenticate(stream.Context())
	if err != nil {
		return err
	}
	ch, notify := s.register(actorID)
	defer s.deregister(actorID, ch)
	_ = s.sup.Heartbeat(actorID) // the cove is talking to us now

	acks := &ackState{pending: map[string]uint64{}}
	go func() { // send loop: control messages + coalesced event acks
		tick := time.NewTicker(ackEvery)
		defer tick.Stop()
		for {
			select {
			case <-stream.Context().Done():
				return
			case <-notify:
				if !s.flushPorts(actorID, ch, stream) {
					return
				}
			case cd, ok := <-ch:
				if !ok {
					return
				}
				if !s.flushPorts(actorID, ch, stream) { // the set precedes any later control (OpenTunnel)
					return
				}
				if err := stream.Send(cd); err != nil {
					return
				}
			case <-tick.C:
				for id, seq := range acks.drain() {
					if err := stream.Send(&attachpb.ControlDown{Msg: &attachpb.ControlDown_Ack{Ack: &attachpb.EventAck{StreamId: id, Seq: seq}}}); err != nil {
						return
					}
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
		case *attachpb.StatusUp_Connector:
			_ = s.sup.RecordConnector(actorID, m.Connector.GetFingerprint())
		case *attachpb.StatusUp_Heartbeat:
			_ = s.sup.Heartbeat(actorID)
		case *attachpb.StatusUp_Gate:
			g := m.Gate
			if err := s.sup.ResolveGate(actorID, g.GetRunId(), jam.GateOutcome{
				At: time.Now(), Exit: int(g.GetExit()), TimedOut: g.GetTimedOut(),
				Output: jam.CleanGateOutput(g.GetOutput(), g.GetTruncated())}); err != nil {
				s.log.Warn("gate result not recorded", "actor", actorID, "run", g.GetRunId(), "err", err.Error())
			}
		case *attachpb.StatusUp_Event:
			if s.events == nil {
				continue
			}
			ev := m.Event
			inst, _ := s.store.GetInstance(actorID)
			hw, err := s.events.Append(actorID, stampOf(s.store, inst), sessionevents.Incoming{
				StreamID: ev.GetStreamId(), Seq: ev.GetSeq(), Turn: ev.GetTurn(),
				ObservedAt: time.UnixMilli(ev.GetObservedUnixMs()), Raw: ev.GetRaw(), TruncatedBytes: ev.GetTruncatedBytes(),
			})
			if err != nil {
				// Never log raw content — it is agent output (see session-events.md).
				s.log.Warn("session event not stored", "actor", actorID, "seq", ev.GetSeq(), "err", err.Error())
				if errors.Is(err, sessionevents.ErrBadStreamID) || errors.Is(err, sessionevents.ErrBadSeq) {
					continue // validation: this event can never be stored; drop it
				}
				// Store failure: end the stream so the cove reconnects and replays
				// from its last ack. Continuing would let the next seq write a gap
				// row and its cumulative ack trim the unstored event.
				return status.Error(codes.Unavailable, "session events: store unavailable")
			}
			acks.set(ev.GetStreamId(), hw)
		}
	}
}

// register installs a fresh send channel for actorID, superseding (and closing)
// any prior one — a reconnect replaces the old stream (the lease-steal analog).
// The map always points at an OPEN channel.
func (s *Server) register(actorID string) (chan *attachpb.ControlDown, chan struct{}) {
	ch := make(chan *attachpb.ControlDown, sendBuffer)
	s.mu.Lock()
	old := s.conns[actorID]
	s.conns[actorID] = ch
	// Seed the studio's tunnel allow-list before anything else can be queued:
	// ch is fresh, and every sender takes mu, so this is its first message and
	// precedes any OpenTunnel.
	if _, ok := s.ports[actorID]; !ok && s.exposureSource != nil {
		if ports := s.exposureSource(actorID); len(ports) > 0 {
			s.ports[actorID] = slices.Clone(ports)
		}
	}
	notify := make(chan struct{}, 1)
	notify <- struct{}{}
	s.portsNotify[actorID] = notify
	s.portsOwed[actorID] = true
	s.mu.Unlock()
	if old != nil {
		close(old) // old send loop sees !ok and exits; map no longer references it
	}
	return ch, notify
}

// flushPorts sends actorID's current port set on stream if it is owed. Only
// the stream that owns the registered channel may consume the debt, so a
// superseded stream cannot swallow what its successor must be sent.
func (s *Server) flushPorts(actorID string, ch chan *attachpb.ControlDown, stream attachpb.Runtime_AttachServer) bool {
	s.mu.Lock()
	if s.conns[actorID] != ch || !s.portsOwed[actorID] {
		s.mu.Unlock()
		return true
	}
	s.portsOwed[actorID] = false
	ports := slices.Clone(s.ports[actorID])
	s.mu.Unlock()
	return stream.Send(tunnelPortsMsg(ports)) == nil
}

func (s *Server) deregister(actorID string, ch chan *attachpb.ControlDown) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conns[actorID] == ch {
		delete(s.conns, actorID)
		delete(s.portsOwed, actorID)
		delete(s.portsNotify, actorID)
		close(ch)
	}
}

// enqueue is the ControlSink delivery primitive: non-blocking send under the lock
// (so the channel can't be closed mid-send); drop if full or no stream.
// It reports whether the message was queued.
func (s *Server) enqueue(actorID string, cd *attachpb.ControlDown) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, ok := s.conns[actorID]
	if !ok {
		return false
	}
	select {
	case ch <- cd:
		return true
	default: // buffer full — drop (best-effort)
		return false
	}
}

// RequestTeardown and Wake implement jam.ControlSink.
func (s *Server) RequestTeardown(actorID string) {
	s.enqueue(actorID, &attachpb.ControlDown{Msg: &attachpb.ControlDown_Teardown{Teardown: &attachpb.Teardown{}}})
}
func (s *Server) Wake(actorID string, reasons ...jam.WakeReason) {
	pb := make([]*attachpb.WakeReason, 0, len(reasons))
	for _, r := range reasons {
		pb = append(pb, &attachpb.WakeReason{Kind: r.Kind, Alarm: r.Alarm, Note: r.Note, Detail: r.Detail})
	}
	s.enqueue(actorID, &attachpb.ControlDown{Msg: &attachpb.ControlDown_Wake{Wake: &attachpb.Wake{Reasons: pb}}})
}

// RunGate asks the cove to run an alarm's gate (best-effort and non-blocking
// like Wake: with no stream the request is dropped, and Jam resolves the gate
// as failed after its grace period).
func (s *Server) RunGate(actorID, runID, alarm, command string, timeout time.Duration) {
	s.enqueue(actorID, &attachpb.ControlDown{Msg: &attachpb.ControlDown_Gate{Gate: &attachpb.RunGate{
		RunId: runID, Alarm: alarm, Command: command, TimeoutS: uint32(timeout / time.Second)}}})
}

// Connected reports whether a live Attach stream is registered for actorID
// (wake-on sends RunGate only then).
func (s *Server) Connected(actorID string) bool { return s.connected(actorID) }

// connected reports whether a live Attach stream is registered for actorID.
func (s *Server) connected(actorID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.conns[actorID]
	return ok
}

func fromPBActivity(a attachpb.Activity) (jam.Activity, bool) {
	switch a {
	case attachpb.Activity_RUNNING:
		return jam.ActivityRunning, true
	case attachpb.Activity_WAITING:
		return jam.ActivityWaiting, true
	case attachpb.Activity_BLOCKED:
		return jam.ActivityBlocked, true
	case attachpb.Activity_DONE:
		return jam.ActivityDone, true
	case attachpb.Activity_HOLDING:
		return jam.ActivityHolding, true
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

// SetTunnelBroker wires the studio-collaboration tunnel broker (nil = Tunnel RPC refused).
func (s *Server) SetTunnelBroker(b *TunnelBroker) { s.tunnels = b }

// PushOpenTunnel asks actorID's studio to dial a Tunnel back for id -> 127.0.0.1:port.
// False when the studio has no live Attach stream or its queue is full.
func (s *Server) PushOpenTunnel(actorID, id string, port int32) bool {
	return s.enqueue(actorID, &attachpb.ControlDown{Msg: &attachpb.ControlDown_OpenTunnel{OpenTunnel: &attachpb.OpenTunnel{Id: id, Port: port}}})
}

// Tunnel is a studio's dial-back for one human connection, authenticated like Attach.
func (s *Server) Tunnel(stream attachpb.Runtime_TunnelServer) error {
	if s.tunnels == nil {
		return status.Error(codes.Unavailable, "tunnels not enabled")
	}
	actorID, err := s.authenticate(stream.Context())
	if err != nil {
		return err
	}
	return s.tunnels.Claim(actorID, stream)
}

func tunnelPortsMsg(ports []int32) *attachpb.ControlDown {
	return &attachpb.ControlDown{Msg: &attachpb.ControlDown_TunnelPorts{TunnelPorts: &attachpb.SetTunnelPorts{Ports: ports}}}
}

// SetExposureSource lets a restarted Jam seed an actor's tunnel allow-list from
// the store when no change has been recorded since it started (nil = none).
// Call before serving. The source runs under the server's lock: it must be
// fast and must not call back into the Server.
func (s *Server) SetExposureSource(f func(actorID string) []int32) { s.exposureSource = f }

// ExposuresChanged implements jam.ExposureSink: record actorID's latest exposed
// ports (an empty set forgets them) and, if its studio is connected, mark the
// set owed and wake the stream's send loop. It only stores and signals without
// blocking (the supervisor calls it holding a mutex). Rapid changes coalesce
// into one send of the latest set; nothing is dropped on a full control queue.
func (s *Server) ExposuresChanged(actorID string, ports []int32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(ports) == 0 {
		delete(s.ports, actorID)
	} else {
		s.ports[actorID] = slices.Clone(ports)
	}
	if n, ok := s.portsNotify[actorID]; ok {
		s.portsOwed[actorID] = true
		select {
		case n <- struct{}{}:
		default:
		}
	}
}
