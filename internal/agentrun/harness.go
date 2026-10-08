package agentrun

import "github.com/aethons-tools/cove/internal/jam/modelspec"

// Harness is the seam between the episode loop and one agent CLI. Workload
// owns the loop (episodes, Wakes, stdin lifetime, idle detection); the Harness
// owns everything CLI-specific: the pre-flight check, the argv, how a text
// message is encoded on stdin, and how a stdout line maps to a harness-neutral
// Event. Config.Harness nil selects Claude.
type Harness interface {
	// Validate is the pre-flight check for a model-spec: run before the first
	// spawn with the raise-time spec, and again before any episode whose
	// delivered spec changed. An error stops Run before that agent process
	// starts. spec nil = none delivered (a Jam predating model-specs, or no
	// default seeded): the harness's built-in defaults.
	Validate(spec *modelspec.Spec) error
	// Prepare (re)writes the per-run files Command's argv names for spec (the
	// one in effect, already Validated). Run before every spawn: the agent
	// shares the cove's filesystem and may have removed them since. An error
	// stops Run before that agent process starts.
	Prepare(spec *modelspec.Spec) error
	// Command returns the binary, argv and the extra env (set over the spawn
	// env; never over a key the connector owns) for one episode.
	Command(ep Episode) (bin string, args []string, env map[string]string)
	// EncodeInput encodes text as one message on the agent's stdin.
	EncodeInput(text string) []byte
	// ParseEvent maps one non-empty stdout line to an Event; an error means the
	// line was unparseable (the idle tracker warns and ignores it).
	ParseEvent(line []byte) (Event, error)
}

// Episode is what one agent spawn is built from.
type Episode struct {
	// Continued is true for every episode after a resume-on-wake.
	Continued bool
	// ContextCore, when non-empty, is the written session-context core file to
	// append to the system prompt.
	ContextCore string
	// Spec is the model-spec in effect (already Validated); nil = none.
	Spec *modelspec.Spec
}

// EventKind classifies a normalized agent event.
type EventKind int

const (
	EventOther           EventKind = iota // not relevant to idle detection
	EventTurnStart                        // the agent started (or is in) a turn
	EventTurnEnd                          // a turn ended; QueuedEmpty says nothing is queued after it
	EventBackgroundTasks                  // the full snapshot of outstanding background tasks (Tasks)
	EventBackgroundDone                   // background task TaskID's completion was delivered; this starts a turn
)

// Event is a harness-neutral agent stdout event, consumed by the idle tracker.
type Event struct {
	Kind        EventKind
	QueuedEmpty bool             // EventTurnEnd
	Tasks       []BackgroundTask // EventBackgroundTasks
	TaskID      string           // EventBackgroundDone
	// Reply marks the agent's own output (a model reply): its conversation
	// exists and can be continued.
	Reply bool
}

// BackgroundTask is one outstanding background task.
type BackgroundTask struct {
	ID, Description string
}
