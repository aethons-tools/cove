// Package worker implements at-task: the git/PR steps (prepare, complete) that wrap
// a worker run at-cove performs. It never runs the worker; the handoff is a cwd file
// convention under .at-task/ (task.json in, worker-result → task-result out).
package worker

// taskSubdir is the per-work directory holding the .at-task/ handoff files.
const taskSubdir = ".at-task"
