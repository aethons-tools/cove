package jam

import (
	"bytes"
	"encoding/json"
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

// secretKeyRE is the spec's secret-named key rule.
var secretKeyRE = regexp.MustCompile(`(?i)token|secret|key|password|passwd|credential`)

// knownPlaceholder is a connector template placeholder (ValidateEnv's set).
var knownPlaceholder = regexp.MustCompile(`\{(?:token|base|host|url)\}`)

// safeTemplateRest is what may remain of a secret-named template once its
// placeholders are removed: nothing, or one auth scheme word.
var safeTemplateRest = regexp.MustCompile(`^(?:(?:Bearer|token) )?$`)

// embeddedUserinfo matches "scheme://userinfo@" inside free text.
var embeddedUserinfo = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^/\s@]*@`)

// stripUserinfo re-parses a URL and drops its userinfo, query and fragment.
// ok=false when s does not parse as a URL.
func stripUserinfo(s string) (string, bool) {
	u, err := url.Parse(s)
	if err != nil {
		return "", false
	}
	u.User = nil
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	u.RawFragment = ""
	return u.String(), true
}

// stripURLSecrets removes URL userinfo from a value containing "://": a
// whole URL is re-parsed (userinfo, query and fragment dropped); free text
// has embedded userinfo removed.
func stripURLSecrets(v string) string {
	if !strings.Contains(v, "://") {
		return v
	}
	if !strings.ContainsAny(v, " \t") {
		if s, ok := stripUserinfo(v); ok {
			return s
		}
	}
	return embeddedUserinfo.ReplaceAllString(v, "$1")
}

// RedactEnv is THE redaction of one value for a setup render and for a stored
// snapshot (env, image ENV, build args, label values, settings.json strings).
// The identity-token vars and every {token} read MarkIdentityToken; a value
// under a secret-named key is MarkMasked unless it is only placeholders (plus
// at most a "Bearer "/"token " scheme); anything else is shown as is, with
// URL userinfo removed.
func RedactEnv(key, value string) string {
	for _, v := range snippet.IdentityTokenVars() {
		if key == v {
			return MarkIdentityToken
		}
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
	if err := json.Unmarshal(b, &v); err != nil {
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
		if unicode.IsControl(r) {
			return MarkInvalid
		}
	}
	return s
}

// RedactLabel re-parses an image label for display: an unsafe key hides the
// whole label; a URL value loses its userinfo (unparsable: invalid); the
// value then goes through RedactEnv under the label key.
func RedactLabel(key, value string) (string, string) {
	if SafeText(key) == MarkInvalid {
		return MarkInvalid, MarkInvalid
	}
	v := SafeText(value)
	if v == MarkInvalid {
		return key, MarkInvalid
	}
	if strings.Contains(v, "://") {
		s, ok := stripUserinfo(v)
		if !ok {
			return key, MarkInvalid
		}
		v = s
	}
	return key, RedactEnv(key, v)
}
