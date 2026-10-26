// Package numfmt formats amounts for display with thousands separators:
// 1,234,567 tokens, $12,345.67.
package numfmt

import (
	"fmt"
	"strconv"
	"strings"
)

// Int is n with comma thousands separators.
func Int[T ~int | ~int32 | ~int64 | ~uint | ~uint32 | ~uint64](n T) string {
	s := strconv.FormatInt(int64(n), 10)
	if n < 0 {
		return "-" + group(s[1:])
	}
	return group(s)
}

// Float is v with prec decimals and separators in its integer part.
func Float(v float64, prec int) string {
	s := strconv.FormatFloat(v, 'f', prec, 64)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	whole, frac, _ := strings.Cut(s, ".")
	s = group(whole)
	if frac != "" {
		s += "." + frac
	}
	if neg {
		s = "-" + s
	}
	return s
}

// USD is v in dollars: 2 decimals, or 4 below a cent so tiny costs don't
// read as $0.00.
func USD(v float64) string {
	if v > 0 && v < 0.01 {
		return "$" + Float(v, 4)
	}
	if v < 0 {
		return "-$" + Float(-v, 2)
	}
	return "$" + Float(v, 2)
}

// group inserts a comma every three digits from the right of digits.
func group(digits string) string {
	if len(digits) <= 3 {
		return digits
	}
	var b strings.Builder
	head := len(digits) % 3
	if head > 0 {
		b.WriteString(digits[:head])
	}
	for i := head; i < len(digits); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(digits[i : i+3])
	}
	return b.String()
}

// Of is Int for any integer type, for templates; other values print as is.
func Of(v any) string {
	switch n := v.(type) {
	case int:
		return Int(n)
	case int32:
		return Int(n)
	case int64:
		return Int(n)
	case uint:
		return Int(n)
	case uint32:
		return Int(n)
	case uint64:
		return Int(n)
	}
	return fmt.Sprint(v)
}
