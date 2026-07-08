package scheduler

import "strings"

// assembleBrief renders the self-contained markdown brief that is assembled
// into the worker's input.json (the issue.brief field).
func assembleBrief(iss Issue, repo string, comments []Comment) string {
	var b strings.Builder
	b.WriteString("# " + iss.Identifier + " — " + iss.Title + "\n\n")
	b.WriteString("**Class:** " + iss.Class + "  **Repo:** " + repo + "\n\n")
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
