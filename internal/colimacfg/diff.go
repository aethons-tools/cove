package colimacfg

import "strings"

// Diff renders a minimal line diff of a → b for --dry-run: unchanged lines
// prefixed "  ", removed "- ", added "+ ". Colima configs are small, so a plain
// O(n·m) LCS is fine.
func Diff(a, b []byte) string {
	x, y := lines(a), lines(b)
	lcs := make([][]int, len(x)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(y)+1)
	}
	for i := len(x) - 1; i >= 0; i-- {
		for j := len(y) - 1; j >= 0; j-- {
			if x[i] == y[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var sb strings.Builder
	i, j := 0, 0
	for i < len(x) || j < len(y) {
		switch {
		case i < len(x) && j < len(y) && x[i] == y[j]:
			sb.WriteString("  " + x[i] + "\n")
			i, j = i+1, j+1
		case i < len(x) && (j == len(y) || lcs[i+1][j] >= lcs[i][j+1]):
			sb.WriteString("- " + x[i] + "\n")
			i++
		default:
			sb.WriteString("+ " + y[j] + "\n")
			j++
		}
	}
	return sb.String()
}

func lines(b []byte) []string {
	s := strings.TrimSuffix(string(b), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
