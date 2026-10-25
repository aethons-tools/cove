package jam

// SessionCommand is a session-control press from the session header
// (docs/usage/jam/session-view.md): sent to the session over its Attach
// stream, never through ControlSink.
type SessionCommand string

const (
	CmdInterrupt SessionCommand = "interrupt" // stop the current turn
	CmdCompact   SessionCommand = "compact"   // summarise the conversation in place
	CmdClear     SessionCommand = "clear"     // start a fresh conversation at the next wake
)

// CompactInstructions are what a Compact asks the summary to keep.
const CompactInstructions = "Keep: the current task and its goal; decisions made and why; work done and work still to do; open questions; the people involved and their latest requests; anything you were told to remember."

// CommandAllowed is the header's enablement table: Interrupt while running;
// Compact while running, holding or waiting; Clear while waiting.
func CommandAllowed(c SessionCommand, a Activity) bool {
	switch c {
	case CmdInterrupt:
		return a == ActivityRunning
	case CmdCompact:
		return a == ActivityRunning || a == ActivityHolding || a == ActivityWaiting
	case CmdClear:
		return a == ActivityWaiting
	}
	return false
}

// CommandActivity is the activity CommandAllowed judges inst by: a paused
// session is idle (waiting); a session that isn't live or paused is its phase
// (raising, terminating…), which allows nothing.
func CommandActivity(inst Instance) Activity {
	switch inst.Phase {
	case PhaseIdled:
		return ActivityWaiting
	case PhaseLive:
		return inst.Activity
	}
	return Activity(inst.Phase)
}
