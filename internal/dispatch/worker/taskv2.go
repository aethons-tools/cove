package worker

// Task is the parsed .at-work/task.json (or .yml) — the work specification.
// See docs/usage/at-work-inputs.md for the authoritative schema.
type Task struct {
	Issue  TaskIssue  `json:"issue" yaml:"issue"`
	Repo   TaskRepo   `json:"repo" yaml:"repo"`
	Worker TaskWorker `json:"worker" yaml:"worker"`
	Task   TaskSpec   `json:"task" yaml:"task"`
}

type TaskIssue struct {
	Key   string `json:"key" yaml:"key"`
	Title string `json:"title" yaml:"title"`
}

type TaskRepo struct {
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

// ReadTask reads .at-work/task.{json,yml} (strict — unknown fields error).
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
