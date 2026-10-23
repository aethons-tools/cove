package jam

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// HeaderLog is a destination's troubleshooting header-logging level for one
// direction. Values are never logged raw where they could carry a secret: a
// credential-bearing header (sensitiveHeader) is shown by redactHeaderValue —
// its scheme, a Basic username, and the secret's length and a short sha256
// prefix, so an operator can check which credential went without reading it.
type HeaderLog string

const (
	HeaderLogNone     HeaderLog = "none"     // ("" too) log nothing
	HeaderLogExamined HeaderLog = "examined" // inbound: the header the identity is read from
	HeaderLogChanged  HeaderLog = "changed"  // outbound: the headers the broker set or removed
	HeaderLogAll      HeaderLog = "all"      // every header (sensitive ones redacted)
)

// Rate limit: at most headerLogBurst lines per destination and direction per
// headerLogWindow, so a left-on setting cannot flood the log.
const (
	headerLogBurst  = 60
	headerLogWindow = time.Minute
)

// ValidateHeaderLogging checks the destination's header-logging levels.
func (d Destination) ValidateHeaderLogging() error {
	switch d.LogHeadersIn {
	case "", HeaderLogNone, HeaderLogExamined, HeaderLogAll:
	default:
		return fmt.Errorf("log_headers_in must be none, examined or all")
	}
	switch d.LogHeadersOut {
	case "", HeaderLogNone, HeaderLogChanged, HeaderLogAll:
	default:
		return fmt.Errorf("log_headers_out must be none, changed or all")
	}
	return nil
}

// sensitiveHeaders always carry credentials or sessions (canonical keys).
var sensitiveHeaders = []string{"Authorization", "Proxy-Authorization", "Cookie", "Set-Cookie", "X-Api-Key", "Private-Token"}

// sensitiveNameParts mark a header name as credential-bearing.
var sensitiveNameParts = []string{"token", "secret", "key", "auth", "password", "cookie", "session", "credential"}

// sensitiveHeader reports whether name may carry a secret: a known
// credential header, the destination's inbound or outbound spec header, or a
// name that looks like one.
func sensitiveHeader(name, inHeader, outHeader string) bool {
	c := http.CanonicalHeaderKey(name)
	if slices.Contains(sensitiveHeaders, c) ||
		(inHeader != "" && c == http.CanonicalHeaderKey(inHeader)) ||
		(outHeader != "" && c == http.CanonicalHeaderKey(outHeader)) {
		return true
	}
	l := strings.ToLower(name)
	return slices.ContainsFunc(sensitiveNameParts, func(p string) bool { return strings.Contains(l, p) })
}

// redactHeaderValue shows a credential-bearing value without its secret:
// `Basic user="u" pass=<redacted …>`, `Bearer <redacted …>`, else the whole
// value redacted.
func redactHeaderValue(v string) string {
	if len(v) > 6 && strings.EqualFold(v[:6], "Basic ") {
		if user, pass, ok := decodeBasic(v); ok {
			return fmt.Sprintf("Basic user=%q pass=%s", user, redacted(pass))
		}
		return redacted(v)
	}
	for _, p := range []string{"Bearer ", "token "} {
		if len(v) > len(p) && strings.EqualFold(v[:len(p)], p) {
			return v[:len(p)] + redacted(v[len(p):])
		}
	}
	return redacted(v)
}

// redacted is a secret's length and the first 6 hex digits of its sha256.
func redacted(s string) string {
	sum := sha256.Sum256([]byte(s))
	return fmt.Sprintf("<redacted len=%d sha256:%s>", len(s), hex.EncodeToString(sum[:])[:6])
}

// headerLine renders headers as "Name: value; …" sorted by name, sensitive
// values redacted.
func headerLine(h http.Header, inHeader, outHeader string) string {
	var parts []string
	for _, k := range slices.Sorted(maps.Keys(h)) {
		for _, v := range h[k] {
			parts = append(parts, k+": "+shownValue(k, v, inHeader, outHeader))
		}
	}
	return strings.Join(parts, "; ")
}

func shownValue(name, v, inHeader, outHeader string) string {
	if sensitiveHeader(name, inHeader, outHeader) {
		return redactHeaderValue(v)
	}
	return v
}

// changedHeaders renders what out differs from in by: each header set or
// changed with its (redacted) value, each removed one as <removed>.
func changedHeaders(in, out http.Header, inHeader, outHeader string) string {
	diff := http.Header{}
	for k, vs := range out {
		if !slices.Equal(in[k], vs) {
			diff[k] = vs
		}
	}
	line := headerLine(diff, inHeader, outHeader)
	var removed []string
	for k := range in {
		if _, ok := out[k]; !ok {
			removed = append(removed, k+": <removed>")
		}
	}
	slices.Sort(removed)
	return strings.Join(slices.DeleteFunc(append([]string{line}, removed...), func(s string) bool { return s == "" }), "; ")
}

// logHeadersIn logs the request's headers per d.LogHeadersIn, before the
// identity is checked (so a missing or malformed one shows too).
func (b *Broker) logHeadersIn(d Destination, r *http.Request, in InboundSpec, hasIn bool) {
	if d.LogHeadersIn == "" || d.LogHeadersIn == HeaderLogNone || !b.hdrLog.allow(d.Name, "in") {
		return
	}
	outHeader := ""
	if o, ok := d.OutboundSpec(); ok {
		outHeader = o.Header
	}
	var line string
	switch d.LogHeadersIn {
	case HeaderLogExamined:
		switch v := r.Header.Get(in.Header); {
		case !hasIn:
			line = "no identity-in spec"
		case v == "":
			line = http.CanonicalHeaderKey(in.Header) + ": missing"
		default:
			line = http.CanonicalHeaderKey(in.Header) + ": " + redactHeaderValue(v)
		}
	case HeaderLogAll:
		line = headerLine(r.Header, in.Header, outHeader)
		if hasIn && r.Header.Get(in.Header) == "" {
			line = strings.TrimPrefix(line+"; "+http.CanonicalHeaderKey(in.Header)+": missing", "; ")
		}
	}
	b.log.Info("broker headers in", "destination", d.Name, "method", r.Method, "path", r.URL.Path, "headers", line)
}

// logHeadersOut logs what goes upstream per d.LogHeadersOut: orig is the
// client's headers, out the broker's rewrite of them.
func (b *Broker) logHeadersOut(d Destination, actor string, out *http.Request, orig http.Header, inHeader, outHeader string) {
	if d.LogHeadersOut == "" || d.LogHeadersOut == HeaderLogNone || !b.hdrLog.allow(d.Name, "out") {
		return
	}
	var line string
	switch d.LogHeadersOut {
	case HeaderLogChanged:
		line = changedHeaders(orig, out.Header, inHeader, outHeader)
	case HeaderLogAll:
		line = headerLine(out.Header, inHeader, outHeader)
	}
	b.log.Info("broker headers out", "actor", actor, "destination", d.Name, "method", out.Method, "upstream", out.URL.Host, "path", out.URL.Path, "headers", line)
}

// headerLogLimiter is a fixed-window line budget per destination+direction.
type headerLogLimiter struct {
	mu   sync.Mutex
	now  func() time.Time
	wins map[string]headerLogWin
}

type headerLogWin struct {
	start time.Time
	n     int
}

func newHeaderLogLimiter(now func() time.Time) *headerLogLimiter {
	return &headerLogLimiter{now: now, wins: map[string]headerLogWin{}}
}

func (l *headerLogLimiter) allow(dest, dir string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	k, now := dest+"\x00"+dir, l.now()
	w := l.wins[k]
	if now.Sub(w.start) >= headerLogWindow {
		w = headerLogWin{start: now}
	}
	if w.n >= headerLogBurst {
		return false
	}
	w.n++
	l.wins[k] = w
	return true
}
