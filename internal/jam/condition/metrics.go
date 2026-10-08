package condition

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"
)

// Gauge is an extra single-value gauge the exposition appends (e.g. jam_studios).
type Gauge struct {
	Name, Help string
	Value      float64
}

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

// MetricsHandler serves the Prometheus text exposition of the open conditions,
// gated on a static bearer token (constant-time compare). Only GET/HEAD.
func MetricsHandler(t *Tracker, token string, gauges func() []Gauge) http.Handler {
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var b strings.Builder
		b.WriteString("# HELP jam_up Jam is serving.\n# TYPE jam_up gauge\njam_up 1\n")
		open := t.Open()
		b.WriteString("# HELP jam_attention_condition An open operator-attention condition (always 1).\n# TYPE jam_attention_condition gauge\n")
		count := map[Severity]int{}
		for _, c := range open {
			count[c.Severity]++
			fmt.Fprintf(&b, "jam_attention_condition{key=%q,kind=%q,severity=%q,summary=\"%s\",fix=\"%s\"} 1\n",
				c.Key, c.Kind(), string(c.Severity), labelEscaper.Replace(c.Summary), labelEscaper.Replace(c.Fix))
		}
		b.WriteString("# HELP jam_attention_open Open conditions by severity.\n# TYPE jam_attention_open gauge\n")
		for _, s := range []Severity{Critical, Warning, Info} {
			fmt.Fprintf(&b, "jam_attention_open{severity=%q} %d\n", string(s), count[s])
		}
		if gauges != nil {
			for _, g := range gauges() {
				fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n%s %g\n", g.Name, g.Help, g.Name, g.Name, g.Value)
			}
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(b.String()))
	})
}
