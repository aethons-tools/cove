// Package covemaster is the in-cove client of Jam's Attach stream — the first
// limb of cove-master, the cove's primary process. It dials Jam's runtime
// listener, authenticates with the cove's identity token + per-instance launch
// secret, reports activity up and reacts to control down, reconnecting across
// transient drops. It depends only on the generated attachpb types + grpc, never
// on internal/jam.
package covemaster

import (
	"context"
	"io"
	"log/slog"
	"time"

	"google.golang.org/grpc"

	"github.com/aethons-tools/cove/internal/jam/attach/attachpb"
)

// Activity is the cove's self-reported status (maps to attachpb.Activity).
type Activity int

const (
	Running Activity = iota
	Waiting
	Blocked
	Done
	Holding // turn ended, background tasks still running
)

// ControlKind is a control message the workload reacts to. (TierChanged and
// RotateToken are decoded off the wire but not surfaced here this slice.)
type ControlKind int

const (
	Wake ControlKind = iota
	Teardown
	RunGate // run an alarm's gate and answer with Handle.GateResult
)

// gateBuffer bounds gate results queued for the stream.
const gateBuffer = 16

// GateRequest is a RunGate control: run Command (sh -c) in the workspace,
// killed after Timeout.
type GateRequest struct {
	RunID, Alarm, Command string
	Timeout               time.Duration
}

// GateResult answers a GateRequest: the shell's exit status (-1 when killed),
// whether it was killed at the timeout, and its capped output.
type GateResult struct {
	RunID     string
	Exit      int
	TimedOut  bool
	Output    []byte
	Truncated bool
}

// WakeReason mirrors attachpb.WakeReason (covemaster never imports internal/jam).
type WakeReason struct{ Kind, Alarm, Note, Detail string }

type Control struct {
	Kind    ControlKind
	Reasons []WakeReason // Wake only; nil from an older Jam
	Gate    *GateRequest // RunGate only
}

// controlFromPB decodes a control message the workload reacts to (false for
// TierChanged/RotateToken, which are not surfaced).
func controlFromPB(cd *attachpb.ControlDown) (Control, bool) {
	switch m := cd.GetMsg().(type) {
	case *attachpb.ControlDown_Teardown:
		return Control{Kind: Teardown}, true
	case *attachpb.ControlDown_Wake:
		var rs []WakeReason
		for _, r := range m.Wake.GetReasons() {
			rs = append(rs, WakeReason{Kind: r.GetKind(), Alarm: r.GetAlarm(), Note: r.GetNote(), Detail: r.GetDetail()})
		}
		return Control{Kind: Wake, Reasons: rs}, true
	case *attachpb.ControlDown_Gate:
		g := m.Gate
		return Control{Kind: RunGate, Gate: &GateRequest{RunID: g.GetRunId(), Alarm: g.GetAlarm(), Command: g.GetCommand(),
			Timeout: time.Duration(g.GetTimeoutS()) * time.Second}}, true
	}
	return Control{}, false
}

func gateResultMsg(g GateResult) *attachpb.StatusUp {
	return &attachpb.StatusUp{Msg: &attachpb.StatusUp_Gate{Gate: &attachpb.GateResult{
		RunId: g.RunID, Exit: int32(g.Exit), TimedOut: g.TimedOut, Output: g.Output, Truncated: g.Truncated}}}
}

// Handle lets the workload report activity and session events to the client.
type Handle interface {
	Report(Activity)
	// Event hands one line of the agent's stream-json stdout to the client.
	// It never blocks. raw is only valid during the call; the client copies it.
	Event(turn uint32, raw []byte, truncatedBytes uint64)
	// ConnectorApplied records the fingerprint of the connector the workload
	// applied to its latest agent spawn. It never blocks; the newest value wins
	// and is re-sent on every (re)connect.
	ConnectorApplied(fingerprint string)
	// GateResult reports a RunGate's result. It never blocks; a result with no
	// stream to carry it is dropped (Jam times the gate out).
	GateResult(GateResult)
}

// Workload is what cove-master supervises (the agent, in a later slice).
type Workload interface {
	// Run does the work, reporting activity via h, until it completes (return
	// nil → the client reports Done and exits) or ctx is cancelled (a Teardown,
	// or the parent shutting down). A non-nil error is logged; the unit is still
	// considered over.
	Run(ctx context.Context, h Handle) error
	// Control delivers a decoded control message. Teardown also cancels Run's
	// ctx; Control(Teardown) is the cooperative hook before the unwind.
	Control(c Control)
}

// Config is the client's connection + behavior configuration.
type Config struct {
	Addr         string
	Token        string
	LaunchSecret string
	Heartbeat    time.Duration     // default 10s
	DialOptions  []grpc.DialOption // injected (bufconn in tests); must include transport creds

	EventBufferEvents int           // max buffered unacked events; default 10000
	EventBufferBytes  int           // max buffered unacked raw bytes; default 64 MiB
	FlushTimeout      time.Duration // Done waits this long for the final ack; default 5s
}

func connectorMsg(fp string) *attachpb.StatusUp {
	return &attachpb.StatusUp{Msg: &attachpb.StatusUp_Connector{Connector: &attachpb.ConnectorApplied{Fingerprint: fp}}}
}

func toPBActivity(a Activity) attachpb.Activity {
	switch a {
	case Running:
		return attachpb.Activity_RUNNING
	case Waiting:
		return attachpb.Activity_WAITING
	case Blocked:
		return attachpb.Activity_BLOCKED
	case Done:
		return attachpb.Activity_DONE
	case Holding:
		return attachpb.Activity_HOLDING
	}
	return attachpb.Activity_ACTIVITY_UNSPECIFIED
}

func statusMsg(a Activity) *attachpb.StatusUp {
	return &attachpb.StatusUp{Msg: &attachpb.StatusUp_Status{Status: toPBActivity(a)}}
}
func eventMsg(ev *attachpb.SessionEvent) *attachpb.StatusUp {
	return &attachpb.StatusUp{Msg: &attachpb.StatusUp_Event{Event: ev}}
}
func heartbeatMsg() *attachpb.StatusUp {
	return &attachpb.StatusUp{Msg: &attachpb.StatusUp_Heartbeat{Heartbeat: &attachpb.Heartbeat{}}}
}

func newLogger(log *slog.Logger) *slog.Logger {
	if log != nil {
		return log
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
