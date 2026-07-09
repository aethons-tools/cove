package worker

import (
	"errors"
	"os"
	"path/filepath"
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

// TaskResult is what `at-work complete` writes to .at-work/task-result.{json,yml} —
// the authoritative outcome. STRICT on parse (see the schema in
// docs/usage/at-work-output.md). WorkerResult is the raw worker-result, echoed.
type TaskResult struct {
	Status       TaskStatus `json:"status" yaml:"status"`
	WorkerResult any        `json:"worker-result,omitempty" yaml:"worker-result,omitempty"`
}

// TaskStatus is a tagged union — exactly one of ok / needs-input / error.
type TaskStatus struct {
	OK         *TaskOK         `json:"ok,omitempty" yaml:"ok,omitempty"`
	NeedsInput *TaskNeedsInput `json:"needs-input,omitempty" yaml:"needs-input,omitempty"`
	Error      *TaskError      `json:"error,omitempty" yaml:"error,omitempty"`
}

// ActiveTask returns the name of the single set variant, or an error otherwise.
func (s TaskStatus) ActiveTask() (string, error) {
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

type TaskOK struct {
	Message string `json:"message,omitempty" yaml:"message,omitempty"`
	PRURL   string `json:"pr-url,omitempty" yaml:"pr-url,omitempty"`
}

type TaskNeedsInput struct {
	Message string `json:"message" yaml:"message"`
	Commit  string `json:"commit" yaml:"commit"`
}

type TaskError struct {
	Message string `json:"message" yaml:"message"`
	Detail  string `json:"detail,omitempty" yaml:"detail,omitempty"`
}

// WriteTaskResult writes tr to .at-work/task-result<ext> (ext is ".json" or ".yml",
// mirroring the task file). The .at-work dir must already exist.
func WriteTaskResult(dir, ext string, tr TaskResult) error {
	return encodeFile(filepath.Join(dir, workSubdir, "task-result"+ext), tr)
}

// TaskExt returns the extension (".json" or ".yml") of the task file in dir's .at-work,
// so complete can write task-result in the same format.
func TaskExt(dir string) (string, error) {
	_, ext, err := resolveContract(dir, "task")
	return ext, err
}
