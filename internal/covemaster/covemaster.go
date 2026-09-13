// Package covemaster is the in-cove client of harbor's Attach stream — the first
// limb of cove-master, the cove's primary process. It dials harbor's runtime
// listener, authenticates with the cove's identity token + per-instance launch
// secret, reports activity up and reacts to control down, reconnecting across
// transient drops. It depends only on the generated attachpb types + grpc, never
// on internal/harbor.
package covemaster

import (
	"context"
	"io"
	"log/slog"
	"time"

	"google.golang.org/grpc"

	"github.com/aethons-tools/cove/internal/harbor/attach/attachpb"
)

// Activity is the cove's self-reported status (maps to attachpb.Activity).
type Activity int

const (
	Running Activity = iota
	Waiting
	Blocked
	Done
)

// ControlKind is a control message the workload reacts to. (TierChanged and
// RotateToken are decoded off the wire but not surfaced here this slice.)
type ControlKind int

const (
	Wake ControlKind = iota
	Teardown
)

type Control struct{ Kind ControlKind }

// Handle lets the workload report activity to the client.
type Handle interface{ Report(Activity) }

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
	}
	return attachpb.Activity_ACTIVITY_UNSPECIFIED
}

func statusMsg(a Activity) *attachpb.StatusUp {
	return &attachpb.StatusUp{Msg: &attachpb.StatusUp_Status{Status: toPBActivity(a)}}
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
