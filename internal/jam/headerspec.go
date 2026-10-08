package jam

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"golang.org/x/net/http/httpguts"
)

// ApplyMethod names how a credential (the inbound identity token, or the
// outbound real credential) is carried on an HTTP request: a preset that
// expands to a header spec, or ApplyCustom, meaning the destination's own
// spec (IdentityInSpec / ApplySpec) applies.
type ApplyMethod string

const (
	ApplyBearer        ApplyMethod = "bearer"         // Authorization: Bearer <value> (inbound also accepts "token <value>")
	ApplyBasicPassword ApplyMethod = "basic-password" // HTTP basic auth, <value> as the password (user x-access-token)
	ApplyXAPIKey       ApplyMethod = "x-api-key"      // X-Api-Key: <value> (Anthropic API keys)
	ApplyRaw           ApplyMethod = "raw"            // Authorization: <value> (e.g. Linear personal API keys)
	ApplyCustom        ApplyMethod = "custom"         // the destination's own IdentityInSpec / ApplySpec
)

// Presets are the named methods, in display order (ApplyCustom excluded).
var Presets = []ApplyMethod{ApplyBearer, ApplyBasicPassword, ApplyXAPIKey, ApplyRaw}

// HeaderEncoding is how a header value carries the credential.
type HeaderEncoding string

const (
	EncodingRaw   HeaderEncoding = ""      // the value as-is (also spelled "raw")
	EncodingBasic HeaderEncoding = "basic" // HTTP basic auth: "Basic base64(user:value)"
)

// InboundSpec says where a studio presents its Jam identity token: the header,
// and (raw encoding) the accepted value prefixes — the first that matches is
// cut off; none means the bare value. With basic encoding the token is the
// basic-auth password and prefixes don't apply. The inverse of OutboundSpec.
type InboundSpec struct {
	Header   string         `json:"header"             yaml:"header"`
	Prefixes []string       `json:"prefixes,omitempty" yaml:"prefixes,omitempty"`
	Encoding HeaderEncoding `json:"encoding,omitempty" yaml:"encoding,omitempty"`
}

// OutboundSpec says how Jam's real credential is set on the upstream request:
// Template (containing {cred} exactly once) is rendered and set on Header; with
// basic encoding the rendered value is the password under BasicUser.
type OutboundSpec struct {
	Header    string         `json:"header"               yaml:"header"`
	Template  string         `json:"template"             yaml:"template"`
	Encoding  HeaderEncoding `json:"encoding,omitempty"   yaml:"encoding,omitempty"`
	BasicUser string         `json:"basic_user,omitempty" yaml:"basic_user,omitempty"`
}

// bearerIn is the bearer preset's inbound spec — also how the broker's own
// endpoints (/connector, /context) take the identity.
var bearerIn = InboundSpec{Header: "Authorization", Prefixes: []string{"Bearer ", "token "}} // "token <x>": gh to a GHE host (GH_HOST=<jam>)

// inboundPresets / outboundPresets are the presets' expansions; keys are
// exactly Presets. Read-only.
var (
	inboundPresets = map[ApplyMethod]InboundSpec{
		ApplyBearer:        bearerIn,
		ApplyBasicPassword: {Header: "Authorization", Encoding: EncodingBasic},
		ApplyXAPIKey:       {Header: "X-Api-Key"},
		ApplyRaw:           {Header: "Authorization"},
	}
	outboundPresets = map[ApplyMethod]OutboundSpec{
		ApplyBearer: {Header: "Authorization", Template: "Bearer {cred}"},
		// git smart-HTTP: any username, PAT as password.
		ApplyBasicPassword: {Header: "Authorization", Template: "{cred}", Encoding: EncodingBasic, BasicUser: "x-access-token"},
		ApplyXAPIKey:       {Header: "X-Api-Key", Template: "{cred}"},
		ApplyRaw:           {Header: "Authorization", Template: "{cred}"},
	}
)

// inboundSpec resolves a method: the preset's expansion, or custom's spec.
// false = none (requests get 401).
func inboundSpec(m ApplyMethod, custom *InboundSpec) (InboundSpec, bool) {
	if m == ApplyCustom {
		if custom == nil {
			return InboundSpec{}, false
		}
		return *custom, true
	}
	in, ok := inboundPresets[m]
	return in, ok
}

// outboundSpec resolves a method: the preset's expansion, or custom's spec.
// false = none (no credential is set).
func outboundSpec(m ApplyMethod, custom *OutboundSpec) (OutboundSpec, bool) {
	if m == ApplyCustom {
		if custom == nil {
			return OutboundSpec{}, false
		}
		return *custom, true
	}
	out, ok := outboundPresets[m]
	return out, ok
}

// InboundSpec is the effective identity-in spec (IdentityIn / IdentityInSpec).
func (d Destination) InboundSpec() (InboundSpec, bool) {
	return inboundSpec(d.IdentityIn, d.IdentityInSpec)
}

// OutboundSpec is the effective apply spec (Apply / ApplySpec).
func (d Destination) OutboundSpec() (OutboundSpec, bool) { return outboundSpec(d.Apply, d.ApplySpec) }

// extract returns the identity token the spec finds on h.
func (s InboundSpec) extract(h http.Header) (string, bool) {
	v := h.Get(s.Header)
	if v == "" {
		return "", false
	}
	if s.Encoding == EncodingBasic {
		_, pass, ok := decodeBasic(v)
		return pass, ok && pass != ""
	}
	if len(s.Prefixes) == 0 {
		return v, true
	}
	for _, p := range s.Prefixes {
		if t, ok := strings.CutPrefix(v, p); ok && t != "" {
			return t, true
		}
	}
	return "", false
}

// apply sets cred on h per the spec.
func (s OutboundSpec) apply(h http.Header, cred string) {
	v := strings.Replace(s.Template, "{cred}", cred, 1)
	if s.Encoding == EncodingBasic {
		v = "Basic " + base64.StdEncoding.EncodeToString([]byte(s.BasicUser+":"+v))
	}
	h.Set(s.Header, v)
}

// decodeBasic parses a basic-auth header value, as net/http does.
func decodeBasic(v string) (user, pass string, ok bool) {
	const prefix = "Basic "
	if len(v) < len(prefix) || !strings.EqualFold(v[:len(prefix)], prefix) {
		return "", "", false
	}
	b, err := base64.StdEncoding.DecodeString(v[len(prefix):])
	if err != nil {
		return "", "", false
	}
	return strings.Cut(string(b), ":")
}

// unhonorableHeaders can't carry an identity or credential through the
// broker: ReverseProxy strips the hop-by-hop ones after the Director, and it
// owns Host. Canonical keys.
var unhonorableHeaders = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
	"Te", "Trailer", "Transfer-Encoding", "Upgrade", "Host",
}

// validateHeaderName checks that header is a valid HTTP header name the proxy
// can honor (not hop-by-hop, not Host).
func validateHeaderName(header string) error {
	if !httpguts.ValidHeaderFieldName(header) {
		return fmt.Errorf("header %q is not a valid HTTP header name", header)
	}
	if slices.Contains(unhonorableHeaders, http.CanonicalHeaderKey(header)) {
		return fmt.Errorf("header %q is hop-by-hop or proxy-owned; the broker can't carry it", header)
	}
	return nil
}

// validateHeaderAndEncoding holds the checks shared by both spec directions:
// a valid header name the proxy can honor, a known encoding, and basic
// encoding only on Authorization.
func validateHeaderAndEncoding(header string, enc HeaderEncoding) error {
	if err := validateHeaderName(header); err != nil {
		return err
	}
	if enc != EncodingRaw && enc != "raw" && enc != EncodingBasic {
		return fmt.Errorf("encoding %q is not raw or basic", enc)
	}
	if enc == EncodingBasic && http.CanonicalHeaderKey(header) != "Authorization" {
		return errors.New("basic encoding requires header Authorization")
	}
	return nil
}

func validHeaderValuePart(s string) bool {
	return !strings.ContainsAny(s, "\r\n\x00")
}

// Validate checks an inbound spec. Errors never echo prefix values.
func (s InboundSpec) Validate() error {
	if err := validateHeaderAndEncoding(s.Header, s.Encoding); err != nil {
		return err
	}
	if s.Encoding == EncodingBasic && len(s.Prefixes) > 0 {
		return errors.New("prefixes don't apply to basic encoding")
	}
	for _, p := range s.Prefixes {
		if p == "" || !validHeaderValuePart(p) {
			return errors.New("prefixes must be non-empty and single-line")
		}
	}
	return nil
}

// Validate checks an outbound spec. Errors never echo the template.
func (s OutboundSpec) Validate() error {
	if err := validateHeaderAndEncoding(s.Header, s.Encoding); err != nil {
		return err
	}
	if strings.Count(s.Template, "{cred}") != 1 {
		return errors.New("template must contain {cred} exactly once")
	}
	if !validHeaderValuePart(s.Template) {
		return errors.New("template must be single-line")
	}
	if s.Encoding == EncodingBasic {
		if s.BasicUser == "" || strings.Contains(s.BasicUser, ":") || !validHeaderValuePart(s.BasicUser) {
			return errors.New("basic encoding requires a basic_user without ':'")
		}
	} else if s.BasicUser != "" {
		return errors.New("basic_user applies only to basic encoding")
	}
	return nil
}

// validateHeaderSpecs checks IdentityIn/Apply at write time: empty or a known
// preset, or custom with a valid spec; a spec is only allowed with custom.
func (d Destination) validateHeaderSpecs() error {
	if err := validateMethod("identity_in", d.IdentityIn, d.IdentityInSpec != nil, func() error { return d.IdentityInSpec.Validate() }); err != nil {
		return err
	}
	return validateMethod("apply", d.Apply, d.ApplySpec != nil, func() error { return d.ApplySpec.Validate() })
}

func validateMethod(field string, m ApplyMethod, hasSpec bool, validate func() error) error {
	if m == ApplyCustom {
		if !hasSpec {
			return fmt.Errorf("%s custom requires %s_spec", field, field)
		}
		if err := validate(); err != nil {
			return fmt.Errorf("%s_spec: %w", field, err)
		}
		return nil
	}
	if hasSpec {
		return fmt.Errorf("%s_spec is only used with %s custom", field, field)
	}
	if m != "" && !slices.Contains(Presets, m) {
		names := make([]string, 0, len(Presets)+1)
		for _, p := range Presets {
			names = append(names, string(p))
		}
		return fmt.Errorf("%s %q is not one of %s", field, m, strings.Join(append(names, string(ApplyCustom)), ", "))
	}
	return nil
}
