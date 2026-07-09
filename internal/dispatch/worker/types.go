// Package worker implements at-work: the git/PR steps (prepare, complete) that wrap
// a worker run at-cove performs. It never runs the worker; the handoff is a cwd file
// convention under .at-work/ (task.json in, worker-result → task-result out).
package worker

// workSubdir is the per-work directory holding the .at-work/ handoff files.
const workSubdir = ".at-work"
