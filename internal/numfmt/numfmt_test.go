package numfmt

import "testing"

func TestInt(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 84300: "84,300", 1234567: "1,234,567", -1234: "-1,234"} {
		if got := Int(n); got != want {
			t.Errorf("Int(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestUSD(t *testing.T) {
	for v, want := range map[float64]string{0: "$0.00", 0.0042: "$0.0042", 0.42: "$0.42", 9783.62: "$9,783.62", 1234567.891: "$1,234,567.89", -0.42: "-$0.42"} {
		if got := USD(v); got != want {
			t.Errorf("USD(%v) = %q, want %q", v, got, want)
		}
	}
	if got := Float(12345.6789, 4); got != "12,345.6789" {
		t.Errorf("Float = %q", got)
	}
}
