package jam

import (
	"bytes"
	"encoding/json"
	"io"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/aethons-tools/cove/internal/jam/snippet"
)

// The fixed marks a setup render shows in place of a value.
const (
	MarkIdentityToken = "«identity token»"
	MarkMasked        = "«masked»"
	MarkInvalid       = "invalid"
)

// Known limits (documented in inspect.md): a secret under a key that does not
// look secret ("lookalike" keys such as "cfg") and credentials without a URL
// scheme or an auth-scheme word (user:pw@host, a bare password) are not
// detected, nor is a password containing an unencoded space; only the key rule and the value patterns below apply.

// secretKeyRE is the spec's secret-named key rule, plus auth/authorization
// (whole segment, e.g. _auth, X_AUTH, Authorization).
var secretKeyRE = regexp.MustCompile(`(?i)token|secret|key|password|passwd|credential|authorization|(?:^|[^a-z0-9])auth(?:$|[^a-z0-9])`)

// authValueRE finds "<scheme> <credential>" in header/assignment position: the
// scheme word starts the value or follows ':', '=' or '"', and a
// credential-like token (>= 8 chars) follows.
var authValueRE = regexp.MustCompile(`(?i)(?:^|[:="])\s*(?:bearer|basic|token)(?:\s+|=)[A-Za-z0-9._~+/=-]{8,}`)

// authHeaderRE finds an Authorization header line in a value.
var authHeaderRE = regexp.MustCompile(`(?i)authorization\s*:`)

// knownPlaceholder is a connector template placeholder (ValidateEnv's set).
var knownPlaceholder = regexp.MustCompile(`\{(?:token|base|host|url)\}`)

// safeTemplateRest is what may remain of a secret-named template once its
// placeholders are removed: nothing, or one auth scheme word.
var safeTemplateRest = regexp.MustCompile(`^(?:(?:Bearer|token) )?$`)

var schemeRE = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://`)

// MarkQuery replaces a URL's query and fragment.
const markQuery = "?" + MarkMasked

// queryEnd is where a masked query/fragment ends: the next list delimiter
// (',', ';', '|') or whitespace at or after from, else the end of v.
func queryEnd(v string, from int) int {
	if i := strings.IndexFunc(v[from:], func(r rune) bool {
		return r == ',' || r == ';' || r == '|' || unicode.IsSpace(r)
	}); i >= 0 {
		return from + i
	}
	return len(v)
}

// stripURLSecrets, for EVERY scheme:// occurrence in v, removes the userinfo
// (up to the last '@' before the first '/' or whitespace) and replaces a
// query or fragment (also on a path-less URL) with markQuery, continuing
// after the next list delimiter. It does not depend on url.Parse succeeding,
// and handles lists (",", ";", "|") and free text.
func stripURLSecrets(v string) string {
	if !strings.Contains(v, "://") {
		return v
	}
	var out strings.Builder
	pos := 0
	for pos < len(v) {
		loc := schemeRE.FindStringIndex(v[pos:])
		if loc == nil {
			break
		}
		out.WriteString(v[pos : pos+loc[1]])
		start := pos + loc[1]
		// authority: up to '/', whitespace or end
		authEnd := len(v)
		if i := strings.IndexFunc(v[start:], func(r rune) bool { return r == '/' || unicode.IsSpace(r) }); i >= 0 {
			authEnd = start + i
		}
		// a later scheme inside the authority starts the next URL of a list
		if m := schemeRE.FindStringIndex(v[start:]); m != nil && start+m[0] < authEnd {
			authEnd = start + m[0]
		}
		hostStart := start
		if i := strings.LastIndexByte(v[start:authEnd], '@'); i >= 0 {
			hostStart = start + i + 1
		}
		if q := strings.IndexAny(v[hostStart:authEnd], "?#"); q >= 0 {
			out.WriteString(v[hostStart : hostStart+q])
			out.WriteString(markQuery)
			pos = queryEnd(v, hostStart+q)
			continue
		}
		out.WriteString(v[hostStart:authEnd])
		pos = authEnd
		// a query/fragment in this whitespace-free run, before any later scheme
		runEnd := len(v)
		if i := strings.IndexFunc(v[pos:], unicode.IsSpace); i >= 0 {
			runEnd = pos + i
		}
		q := strings.IndexAny(v[pos:runEnd], "?#")
		if q < 0 {
			continue
		}
		if next := schemeRE.FindStringIndex(v[pos:runEnd]); next != nil && next[0] < q {
			continue
		}
		out.WriteString(v[pos : pos+q])
		out.WriteString(markQuery)
		pos = queryEnd(v, pos+q)
	}
	out.WriteString(v[pos:])
	return out.String()
}

// stripUserinfo re-parses a single URL and returns it without userinfo, with
// any query/fragment replaced by markQuery. ok=false when s does not parse.
func stripUserinfo(s string) (string, bool) {
	if _, err := url.Parse(s); err != nil {
		return "", false
	}
	return stripURLSecrets(s), true
}

// valueLooksSecret reports a credential-shaped value: an auth scheme word
// followed by anything but a placeholder, or an Authorization header.
func valueLooksSecret(v string) bool {
	if authHeaderRE.MatchString(v) {
		return true
	}
	if authValueRE.MatchString(v) {
		return true
	}
	return false
}

// RedactEnv is THE redaction of one value for a setup render and for a stored
// snapshot (env, image ENV, build args, label values, settings.json strings).
// The identity-token vars and every {token} read MarkIdentityToken; a
// credential-shaped value, or a value under a secret-named key that is not
// only placeholders (plus at most a "Bearer "/"token " scheme), is
// MarkMasked; anything else is shown as is, with URL userinfo, query and
// fragment removed from every URL in it.
func RedactEnv(key, value string) string {
	for _, v := range snippet.IdentityTokenVars() {
		if key == v {
			return MarkIdentityToken
		}
	}
	if valueLooksSecret(value) {
		return MarkMasked
	}
	shown := strings.ReplaceAll(value, "{token}", MarkIdentityToken)
	if !secretKeyRE.MatchString(key) {
		return stripURLSecrets(shown)
	}
	if knownPlaceholder.MatchString(value) && safeTemplateRest.MatchString(knownPlaceholder.ReplaceAllString(value, "")) {
		return shown
	}
	return MarkMasked
}

// RedactJSON re-renders a JSON document (settings.json) indented, every
// string value passed through RedactEnv under its nearest object key and
// every scalar under a secret-named key masked.
// ok=false when b is not JSON.
func RedactJSON(b []byte) (string, bool) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return "", false
	}
	if _, err := dec.Token(); err != io.EOF {
		return "", false
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(redactJSONValue("", v)); err != nil {
		return "", false
	}
	return strings.TrimRight(out.String(), "\n"), true
}

func redactJSONValue(key string, v any) any {
	switch t := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, x := range t {
			ck := k // a secret-named parent taints its whole subtree
			if !secretKeyRE.MatchString(k) && secretKeyRE.MatchString(key) {
				ck = key
			}
			m[k] = redactJSONValue(ck, x)
		}
		return m
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = redactJSONValue(key, x)
		}
		return out
	case string:
		return RedactEnv(key, t)
	case nil:
		return nil
	}
	// number or bool
	if secretKeyRE.MatchString(key) {
		return MarkMasked
	}
	return v
}

// SafeText is s when it is valid UTF-8, at most 256 bytes and free of control
// characters; MarkInvalid otherwise. For names and labels from outside Jam.
func SafeText(s string) string {
	if len(s) > 256 || !utf8.ValidString(s) {
		return MarkInvalid
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == 0x2028 || r == 0x2029 {
			return MarkInvalid
		}
	}
	return s
}

// RedactLabel re-parses an image label for display: an unsafe key hides the
// whole label; a URL value loses its userinfo (unparsable: invalid); the
// value then goes through RedactEnv under the label key (which strips every
// URL's userinfo, query and fragment).
func RedactLabel(key, value string) (string, string) {
	if SafeText(key) == MarkInvalid {
		return MarkInvalid, MarkInvalid
	}
	v := SafeText(value)
	if v == MarkInvalid {
		return key, MarkInvalid
	}
	// invalid only for a single-token URL that fails to parse
	if strings.Count(v, "://") == 1 && !strings.ContainsFunc(v, unicode.IsSpace) {
		if _, err := url.Parse(v); err != nil {
			return key, MarkInvalid
		}
	}
	return key, RedactEnv(key, v)
}
