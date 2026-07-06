package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// Result is the structured handoff a dispatch command writes to DISPATCH_RESULT.
type Result struct {
	Status     string      `json:"status"`
	Artifacts  Artifacts   `json:"artifacts"`
	NeedsInput *NeedsInput `json:"needsInput,omitempty"`
	Summary    string      `json:"summary"`
	Usage      Usage       `json:"usage"`
}

type Artifacts struct {
	Branch  string `json:"branch"`
	PRURL   string `json:"prUrl"`
	DocPath string `json:"docPath"`
}

type NeedsInput struct {
	Doing     string `json:"doing"`
	Blocker   string `json:"blocker"`
	Need      string `json:"need"`
	Tried     string `json:"tried"`
	SafeState string `json:"safeState"`
}

type Usage struct {
	Tokens int `json:"tokens"`
	WallMs int `json:"wallMs"`
}

// Result status values.
const (
	StatusOK         = "ok"
	StatusNeedsInput = "needs_input"
	StatusError      = "error"
)

// ReadResult reads and validates the result file at path. A present, valid file
// with a known status is authoritative; an absent, unparseable, or unknown-status
// result is reported as StatusError (with a diagnostic Summary). ReadResult never
// returns an error — the coarse outcome is always a Result.
func ReadResult(path string) Result {
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{Status: StatusError, Summary: fmt.Sprintf("no result file at %s: %v", path, err)}
	}
	var r Result
	if err := json.Unmarshal(data, &r); err != nil {
		return Result{Status: StatusError, Summary: fmt.Sprintf("unparseable result at %s: %v", path, err)}
	}
	switch r.Status {
	case StatusOK, StatusNeedsInput, StatusError:
		return r
	default:
		return Result{Status: StatusError, Summary: fmt.Sprintf("invalid status %q in result at %s", r.Status, path)}
	}
}
