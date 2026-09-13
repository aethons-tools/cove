package scheduler

import "strings"

// AssembleBrief renders the self-contained markdown brief describing an issue —
// used as the worker's task.json `task.brief` field (dispatch) and as the
// managed-cove prompt (the resident dispatcher, COV-146).
func AssembleBrief(iss Issue, comments []Comment) string {
	var b strings.Builder
	b.WriteString("# " + iss.Identifier + " — " + iss.Title + "\n\n")
	b.WriteString("**Class:** " + iss.Class + "\n\n")
	b.WriteString("## Description\n\n")
	b.WriteString(strings.TrimSpace(iss.Description) + "\n")
	if len(comments) > 0 {
		b.WriteString("\n## Thread\n\n")
		for _, c := range comments {
			b.WriteString("- **" + c.Author + ":** " + c.Body + "\n")
		}
	}
	return b.String()
}
