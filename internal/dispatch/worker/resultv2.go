package worker

import (
	"errors"
	"os"
)

// WorkerResult is the parsed .at-work/worker-result.json (or .yml) — the worker's
// self-report. LENIENT: unknown fields are accepted (and preserved in the echo).
// See docs/usage/at-work-inputs.md.
type WorkerResult struct {
	Status WorkerStatus `json:"status" yaml:"status"`
}

// WorkerStatus is a tagged union — exactly one of ok / needs-input / error.
//
// NOTE: the variant payload types are named WorkerNeedsInput/WorkerError, not the
// brief's literal NeedsInput/StatusError — those names collide with pre-existing
// declarations in types.go (the v1 `type NeedsInput struct{...}` and
// `const StatusError = "ERROR"`), which this plan is additive to and must not edit.
// Wire format (json/yaml kebab-case tags) is unchanged from the brief.
type WorkerStatus struct {
	OK         *WorkerOK         `json:"ok,omitempty" yaml:"ok,omitempty"`
	NeedsInput *WorkerNeedsInput `json:"needs-input,omitempty" yaml:"needs-input,omitempty"`
	Error      *WorkerError      `json:"error,omitempty" yaml:"error,omitempty"`
}

// Active returns the name of the single set variant, or an error if not exactly one.
func (s WorkerStatus) Active() (string, error) {
	n := 0
	name := ""
	if s.OK != nil {
		n++
		name = "ok"
	}
	if s.NeedsInput != nil {
		n++
		name = "needs-input"
	}
	if s.Error != nil {
		n++
		name = "error"
	}
	if n != 1 {
		return "", errors.New("status must set exactly one of ok / needs-input / error")
	}
	return name, nil
}

type WorkerOK struct {
	PullRequest *PullRequest `json:"pull-request,omitempty" yaml:"pull-request,omitempty"`
}

type PullRequest struct {
	Title   string `json:"title" yaml:"title"`
	Message string `json:"message" yaml:"message"`
}

type WorkerNeedsInput struct {
	Doing   string `json:"doing" yaml:"doing"`
	Blocker string `json:"blocker" yaml:"blocker"`
	Need    string `json:"need" yaml:"need"`
	Tried   string `json:"tried" yaml:"tried"`
}

type WorkerError struct {
	Message string `json:"message" yaml:"message"`
}

// ReadWorkerResult reads .at-work/worker-result.{json,yml} leniently. It returns the
// typed result (recognized fields) AND raw (the whole document, for the task-result
// echo, so unknown worker fields survive). ok is false if the file is absent.
func ReadWorkerResult(dir string) (wr WorkerResult, raw any, ok bool, err error) {
	path, _, err := resolveContract(dir, "worker-result")
	if errors.Is(err, os.ErrNotExist) {
		return WorkerResult{}, nil, false, nil
	}
	if err != nil {
		return WorkerResult{}, nil, false, err
	}
	if err := decodeFile(path, false, &wr); err != nil {
		return WorkerResult{}, nil, false, err
	}
	if err := decodeFile(path, false, &raw); err != nil {
		return WorkerResult{}, nil, false, err
	}
	return wr, raw, true, nil
}
