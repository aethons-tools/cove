package colimacfg

import "strings"

// diffContext is how many unchanged lines Diff keeps around each change.
const diffContext = 3

// Diff renders a minimal line diff of a → b for --dry-run: unchanged lines
// prefixed "  ", removed "- ", added "+ ". Only changed regions plus up to
// diffContext lines of context are printed; non-adjacent hunks are separated by
// a "@@" line. Colima configs are small, so a plain O(n·m) LCS is fine.
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
	var ops []string
	i, j := 0, 0
	for i < len(x) || j < len(y) {
		switch {
		case i < len(x) && j < len(y) && x[i] == y[j]:
			ops = append(ops, "  "+x[i])
			i, j = i+1, j+1
		case i < len(x) && (j == len(y) || lcs[i+1][j] >= lcs[i][j+1]):
			ops = append(ops, "- "+x[i])
			i++
		default:
			ops = append(ops, "+ "+y[j])
			j++
		}
	}
	// Keep every changed line and the context lines within reach of one.
	keep := make([]bool, len(ops))
	for k, op := range ops {
		if op[0] == ' ' {
			continue
		}
		for c := max(0, k-diffContext); c <= min(len(ops)-1, k+diffContext); c++ {
			keep[c] = true
		}
	}
	var sb strings.Builder
	gap := false
	for k, op := range ops {
		if !keep[k] {
			gap = sb.Len() > 0
			continue
		}
		if gap {
			sb.WriteString("@@\n")
			gap = false
		}
		sb.WriteString(op + "\n")
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
