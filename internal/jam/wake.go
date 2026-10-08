package jam

// WakeReason is one cause of a Wake, carried down the Attach stream so the
// cove's resume prompt can say why the agent was woken.
type WakeReason struct {
	Kind   string `json:"kind"`             // one of the Wake* kinds
	Alarm  string `json:"alarm,omitempty"`  // alarm name (WakeAlarm, WakeGateFailed)
	Note   string `json:"note,omitempty"`   // the alarm's note
	Detail string `json:"detail,omitempty"` // gate stdout, failure cause, …
}

// Wake reason kinds.
const (
	WakeSquawk         = "squawk"
	WakeAlarm          = "alarm"
	WakeGateFailed     = "gate-failed"
	WakeIdle           = "idle"
	WakeContextChanged = "context-changed"
)
