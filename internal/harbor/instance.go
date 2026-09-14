package harbor

import "time"

// Phase is the supervisor-owned lifecycle of a managed cove. Only the supervisor
// writes it (via raise/teardown/reconcile); it is never set from a cove-reported
// value.
type Phase string

const (
	PhaseRaising     Phase = "raising"     // Launcher.Raise called, not yet confirmed Live
	PhaseLive        Phase = "live"        // running and leased; Activity is meaningful
	PhaseTerminating Phase = "terminating" // teardown decided (Done or Lost); Launcher.Teardown in flight
	PhaseGone        Phase = "gone"        // torn down + deregistered (terminal)
	PhaseLost        Phase = "lost"        // reconciler declared dead (lease expired + Probe dead) → Terminating
	PhaseIdled       Phase = "idled"       // paused; intentionally idle — lease-reaping suspended
)

// Activity is cove-reported and only meaningful while Phase == Live. The
// supervisor records what the cove says it is doing; the only phase effect of a
// report is ActivityDone ⇒ PhaseTerminating.
type Activity string

const (
	ActivityRunning Activity = "running"
	ActivityWaiting Activity = "waiting"
	ActivityBlocked Activity = "blocked"
	ActivityDone    Activity = "done"
)

// Lease records which harbor process owns an Instance and until when. Past
// Expiry, any process may steal it (the holder is presumed dead). This is also
// the reconnect handoff for the later gRPC-stream slice.
type Lease struct {
	Holder string    `json:"holder"`
	Expiry time.Time `json:"expiry"`
}

// Instance is the runtime record of one managed cove, keyed by the roster
// Actor's id. Identity (token, grants, expiry) lives on the Actor; this is the
// operational half (location, status, lease). It has no slice/map fields, so a
// value copy is a full copy.
type Instance struct {
	ActorID          string    `json:"actor_id"`
	Project          string    `json:"project"`
	Role             string    `json:"role"`
	Unit             string    `json:"unit,omitempty"`
	Backend          string    `json:"backend,omitempty"`  // populated by the real launcher (later slice)
	Location         string    `json:"location,omitempty"` // opaque handle from Launcher.Raise
	Phase            Phase     `json:"phase"`
	Activity         Activity  `json:"activity,omitempty"`
	Lease            Lease     `json:"lease"`
	LaunchSecretHash string    `json:"launch_secret_hash,omitempty"` // hash of the per-instance launch secret (COV-153)
	RaisedAt         time.Time `json:"raised_at"`
	LastSeen         time.Time `json:"last_seen"`
	WaitingSince     time.Time `json:"waiting_since,omitempty"`   // set when Activity enters Waiting (B1)
	WaitCursor       string    `json:"wait_cursor,omitempty"`     // opaque wake-on baseline set by the wake-on engine
	EscalationTier   int       `json:"escalation_tier,omitempty"` // last-pinged tier index; meaningful only when TierPingedAt is non-zero
	TierPingedAt     time.Time `json:"tier_pinged_at,omitempty"`  // when EscalationTier was pinged; zero = no escalation open
}
