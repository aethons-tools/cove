package adminui

import (
	"time"

	"github.com/aethons-tools/cove/internal/jam/condition"
)

type healthRow struct {
	condition.Condition
	Age, Lasted string
}

func healthData(t *condition.Tracker, amURL string) map[string]any {
	now := time.Now()
	var open, resolved []healthRow
	for _, c := range t.Open() {
		open = append(open, healthRow{Condition: c, Age: fmtDur(now.Sub(c.Since))})
	}
	for _, c := range t.Resolved() {
		resolved = append(resolved, healthRow{Condition: c, Age: fmtDur(now.Sub(*c.ResolvedAt)), Lasted: fmtDur(c.ResolvedAt.Sub(c.Since))})
	}
	silences := ""
	if amURL != "" {
		silences = amURL + "/#/silences"
	}
	return map[string]any{"Title": "Health", "Open": open, "Resolved": resolved, "Silences": silences}
}
