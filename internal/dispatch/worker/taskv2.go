package worker

// Task is the parsed .at-task/task.json (or .yml) — the work specification.
// See docs/usage/at-task-inputs.md for the authoritative schema.
type Task struct {
	Issue  TaskIssue  `json:"issue" yaml:"issue"`
	Repo   TaskRepo   `json:"repo" yaml:"repo"`
	Worker TaskWorker `json:"worker" yaml:"worker"`
	Task   TaskSpec   `json:"task" yaml:"task"`
}

type TaskIssue struct {
	Key   string `json:"key" yaml:"key"`
	Title string `json:"title" yaml:"title"`
	// Closes is an optional code-host auto-close reference (e.g. "#42") appended
	// to the PR body by `at-task complete` so merging the PR closes the tracker
	// issue. at-task is tracker-agnostic: it only honors a value set upstream by
	// the scheduler (for a same-repo GitHub-tracker dispatch); it never computes it.
	Closes string `json:"closes,omitempty" yaml:"closes,omitempty"`
}

type TaskRepo struct {
	Provider     string `json:"provider,omitempty" yaml:"provider,omitempty"` // "github" | "gitlab"; empty => github (legacy)
	Host         string `json:"host,omitempty" yaml:"host,omitempty"`
	Name         string `json:"name" yaml:"name"`
	SourceBranch string `json:"source-branch" yaml:"source-branch"`
	WorkBranch   string `json:"work-branch" yaml:"work-branch"`
}

type TaskWorker struct {
	Class string `json:"class" yaml:"class"`
}

type TaskSpec struct {
	Class string `json:"class,omitempty" yaml:"class,omitempty"`
	Brief string `json:"brief" yaml:"brief"`
}

// ReadTask reads .at-task/task.{json,yml} (strict — unknown fields error).
func ReadTask(dir string) (Task, error) {
	path, _, err := resolveContract(dir, "task")
	if err != nil {
		return Task{}, err
	}
	var t Task
	if err := decodeFile(path, true, &t); err != nil {
		return Task{}, err
	}
	return t, nil
}
