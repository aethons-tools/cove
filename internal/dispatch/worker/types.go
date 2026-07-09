// Package worker implements at-work: the git/PR steps (prepare, complete) that wrap
// a worker run at-cove performs. It never runs the worker; the handoff is a cwd file
// convention under .at-work/ (task.json in, worker-result → task-result out).
package worker

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const workSubdir = ".at-work"

// Input is the worker's task description (both subcommands read it).
type Input struct {
	Issue IssueInput `json:"issue"`
	Repo  RepoInput  `json:"repo"`
}
type IssueInput struct {
	Key       string `json:"key"`
	Title     string `json:"title"`
	WorkClass string `json:"work-class"`
	Brief     string `json:"brief"`
}
type RepoInput struct {
	Name         string `json:"name"`
	SourceBranch string `json:"source-branch"`
	WorkBranch   string `json:"work-branch"`
}

// Outcome is the agent's self-report (.at-work/outcome.json) and the "agent" block.
type Outcome struct {
	Status     string      `json:"status"`
	PRMessage  string      `json:"pr-message,omitempty"`
	NeedsInput *NeedsInput `json:"needs-input,omitempty"`
	Message    string      `json:"message,omitempty"`
}
type NeedsInput struct {
	Doing   string `json:"doing"`
	Blocker string `json:"blocker"`
	Need    string `json:"need"`
	Tried   string `json:"tried"`
}

// Output is what complete writes. Status is authoritative.
type Output struct {
	Status  string   `json:"status"`
	Message string   `json:"message,omitempty"`
	Agent   *Outcome `json:"agent,omitempty"`
	Work    Work     `json:"work"`
}
type Work struct {
	Branch    string `json:"branch,omitempty"`
	PRURL     string `json:"pr-url,omitempty"`
	SafeState string `json:"safe-state,omitempty"`
	Error     string `json:"error,omitempty"`
}

const (
	StatusOK         = "OK"
	StatusNeedsInput = "NEEDS_INPUT"
	StatusError      = "ERROR"
)

func outcomePath(dir string) string { return filepath.Join(dir, workSubdir, "outcome.json") }

// ReadInput reads and decodes input.json.
func ReadInput(path string) (Input, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Input{}, fmt.Errorf("read input %s: %w", path, err)
	}
	var in Input
	if err := json.Unmarshal(data, &in); err != nil {
		return Input{}, fmt.Errorf("parse input %s: %w", path, err)
	}
	return in, nil
}

// ReadOutcome reads .at-work/outcome.json from dir. ok is false if the file is absent.
func ReadOutcome(dir string) (Outcome, bool, error) {
	data, err := os.ReadFile(outcomePath(dir))
	if os.IsNotExist(err) {
		return Outcome{}, false, nil
	}
	if err != nil {
		return Outcome{}, false, err
	}
	var oc Outcome
	if err := json.Unmarshal(data, &oc); err != nil {
		return Outcome{}, false, fmt.Errorf("parse outcome: %w", err)
	}
	return oc, true, nil
}

// WriteOutput marshals out to path (pretty-printed).
func WriteOutput(path string, out Output) error {
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}
