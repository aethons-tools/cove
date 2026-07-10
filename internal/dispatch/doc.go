// Package dispatch owns the Linear-driven dispatcher's control plane — the
// scheduler that drives at-cove worker containers, wired up as the
// `at-cove dispatch` subcommand. The webhook receiver remains future work.
//
// The scheduler/config/linear/exec packages here are live; see the design:
//   - docs/orchestration/at-cove-work-interface.md (the at-cove work contract)
//   - docs/orchestration/linear-agent-workflow.md   (the workflow)
package dispatch
