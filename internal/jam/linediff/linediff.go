// Package linediff is the line-level diff the admin UI and jam show:
// an LCS diff with long unchanged runs collapsed.
package linediff

import (
	"fmt"
	"strings"
)

// Line is one line of a diff: Op is " ", "+", "-" or "…" (a collapsed run
// of unchanged lines, Text says how many).
type Line struct {
	Op   string `json:"op"`
	Text string `json:"text"`
}

// maxCells bounds the LCS table (lines(a) × lines(b)).
const maxCells = 4_000_000

// Context is how many unchanged lines are kept around a change.
const Context = 3

// Diff is a line-level LCS diff of a → b, with unchanged runs longer than
// 2×Context collapsed to a "…" line. tooLarge reports the inputs exceed the
// LCS bound (no lines then).
func Diff(a, b string) (lines []Line, tooLarge bool) {
	al, bl := splitLines(a), splitLines(b)
	n, m := len(al), len(bl)
	if n*m > maxCells {
		return nil, true
	}
	// lcs[i][j] = LCS length of al[i:], bl[j:].
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if al[i] == bl[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var raw []Line
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && al[i] == bl[j]:
			raw = append(raw, Line{" ", al[i]})
			i, j = i+1, j+1
		case i < n && (j == m || lcs[i+1][j] >= lcs[i][j+1]): // removals before additions
			raw = append(raw, Line{"-", al[i]})
			i++
		default:
			raw = append(raw, Line{"+", bl[j]})
			j++
		}
	}
	return collapse(raw), false
}

func splitLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// collapse keeps Context unchanged lines around each change and replaces
// longer unchanged runs with a single "…" line.
func collapse(lines []Line) []Line {
	keep := make([]bool, len(lines))
	for i, l := range lines {
		if l.Op != " " {
			for k := max(0, i-Context); k <= min(len(lines)-1, i+Context); k++ {
				keep[k] = true
			}
		}
	}
	var out []Line
	for i := 0; i < len(lines); {
		if keep[i] {
			out = append(out, lines[i])
			i++
			continue
		}
		j := i
		for j < len(lines) && !keep[j] {
			j++
		}
		out = append(out, Line{"…", fmt.Sprintf("%d unchanged line(s)", j-i)})
		i = j
	}
	return out
}
