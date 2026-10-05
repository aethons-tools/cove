package jam

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
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

// inboundPreset expands a preset name; false for custom/unknown/empty.
func inboundPreset(m ApplyMethod) (InboundSpec, bool) {
	switch m {
	case ApplyBearer:
		// "token <x>" is what gh sends to a GitHub Enterprise host (GH_HOST=<jam>).
		return InboundSpec{Header: "Authorization", Prefixes: []string{"Bearer ", "token "}}, true
	case ApplyBasicPassword:
		return InboundSpec{Header: "Authorization", Encoding: EncodingBasic}, true
	case ApplyXAPIKey:
		return InboundSpec{Header: "X-Api-Key"}, true
	case ApplyRaw:
		return InboundSpec{Header: "Authorization"}, true
	}
	return InboundSpec{}, false
}

// outboundPreset expands a preset name; false for custom/unknown/empty.
func outboundPreset(m ApplyMethod) (OutboundSpec, bool) {
	switch m {
	case ApplyBearer:
		return OutboundSpec{Header: "Authorization", Template: "Bearer {cred}"}, true
	case ApplyBasicPassword:
		// git smart-HTTP: any username, PAT as password.
		return OutboundSpec{Header: "Authorization", Template: "{cred}", Encoding: EncodingBasic, BasicUser: "x-access-token"}, true
	case ApplyXAPIKey:
		return OutboundSpec{Header: "X-Api-Key", Template: "{cred}"}, true
	case ApplyRaw:
		return OutboundSpec{Header: "Authorization", Template: "{cred}"}, true
	}
	return OutboundSpec{}, false
}

// InboundSpec is the effective identity-in spec: the preset's expansion, or
// IdentityInSpec when IdentityIn is custom. false = none (requests get 401).
func (d Destination) InboundSpec() (InboundSpec, bool) {
	if d.IdentityIn == ApplyCustom {
		if d.IdentityInSpec == nil {
			return InboundSpec{}, false
		}
		return *d.IdentityInSpec, true
	}
	return inboundPreset(d.IdentityIn)
}

// OutboundSpec is the effective apply spec: the preset's expansion, or
// ApplySpec when Apply is custom. false = none (no credential is set).
func (d Destination) OutboundSpec() (OutboundSpec, bool) {
	if d.Apply == ApplyCustom {
		if d.ApplySpec == nil {
			return OutboundSpec{}, false
		}
		return *d.ApplySpec, true
	}
	return outboundPreset(d.Apply)
}

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

func validEncoding(e HeaderEncoding) bool {
	return e == EncodingRaw || e == "raw" || e == EncodingBasic
}

func validHeaderValuePart(s string) bool {
	return !strings.ContainsAny(s, "\r\n\x00")
}

// Validate checks an inbound spec. Errors never echo prefix values.
func (s InboundSpec) Validate() error {
	if !httpguts.ValidHeaderFieldName(s.Header) {
		return fmt.Errorf("header %q is not a valid HTTP header name", s.Header)
	}
	if !validEncoding(s.Encoding) {
		return fmt.Errorf("encoding %q is not raw or basic", s.Encoding)
	}
	if s.Encoding == EncodingBasic {
		if http.CanonicalHeaderKey(s.Header) != "Authorization" {
			return errors.New("basic encoding requires header Authorization")
		}
		if len(s.Prefixes) > 0 {
			return errors.New("prefixes don't apply to basic encoding")
		}
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
	if !httpguts.ValidHeaderFieldName(s.Header) {
		return fmt.Errorf("header %q is not a valid HTTP header name", s.Header)
	}
	if !validEncoding(s.Encoding) {
		return fmt.Errorf("encoding %q is not raw or basic", s.Encoding)
	}
	if strings.Count(s.Template, "{cred}") != 1 {
		return errors.New("template must contain {cred} exactly once")
	}
	if !validHeaderValuePart(s.Template) {
		return errors.New("template must be single-line")
	}
	if s.Encoding == EncodingBasic {
		if http.CanonicalHeaderKey(s.Header) != "Authorization" {
			return errors.New("basic encoding requires header Authorization")
		}
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
	if _, ok := inboundPreset(m); m != "" && !ok {
		return fmt.Errorf("%s %q is not one of bearer, basic-password, x-api-key, raw, custom", field, m)
	}
	return nil
}
