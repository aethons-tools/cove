# Task 6 report
Implemented per brief verbatim: WithConditions option, Health page/template, attnAlert kind + conditionItems, frameSource.items() used at the 3 call sites (rail, fragmentFrame, frameFor), Health in jamTabs, chip label, alertmanagerURL() in cmd/at-jam/config.go, WithConditions passed in main.go, ui.md tab list + Health bullet, updated: bumped.
Tests: go test ./internal/jam/adminui/ ./cmd/at-jam/ -count=1 -> both ok; go build ./... ok; just lint clean. No existing test pinned the tab list. getHX helper added to health_test.go (was absent).
TDD: tests were written together with the implementation in one pass; no separate RED run was captured.
Concerns: ui.md links monitoring.md, which doesn't exist yet (planned for a later docs task) - dangling until then. -race not runnable (no C compiler).
