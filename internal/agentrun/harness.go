package agentrun

// Harness is the seam between the episode loop and one agent CLI. Workload
// owns the loop (episodes, Wakes, stdin lifetime, idle detection); the Harness
// owns everything CLI-specific: the pre-flight check, the argv, how a text
// message is encoded on stdin, and how a stdout line maps to a harness-neutral
// Event. Config.Harness nil selects Claude.
type Harness interface {
	// Validate is the pre-flight check run once before the first spawn; an
	// error stops Run before any agent process starts.
	Validate() error
	// Command returns the binary and argv for one episode. continued is true
	// for every episode after a resume-on-wake; contextCore, when non-empty, is
	// the written session-context core file to append to the system prompt.
	Command(continued bool, contextCore string) (bin string, args []string)
	// EncodeInput encodes text as one message on the agent's stdin.
	EncodeInput(text string) []byte
	// ParseEvent maps one non-empty stdout line to an Event; an error means the
	// line was unparseable (the idle tracker warns and ignores it).
	ParseEvent(line []byte) (Event, error)
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
}

// BackgroundTask is one outstanding background task.
type BackgroundTask struct {
	ID, Description string
}
