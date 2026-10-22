package jam

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam/gitsource"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// ConfigFileVersion is the `version:` every config file carries.
const ConfigFileVersion = 1

// The config object kinds: the `kind:` of each file, and the kind of an
// ownership mark.
const (
	ConfigKindDestination = "destination"
	ConfigKindModelSpec   = "model-spec"
	ConfigKindUser        = "user"
	ConfigKindConnection  = "connection"
	ConfigKindKit         = "kit"
	ConfigKindProject     = "project"
	ConfigKindRole        = "role"
	ConfigKindJamContext  = "jam-context"
)

// ConfigKey names one config object.
type ConfigKey struct {
	Kind string `json:"kind"`
	Name string `json:"name"` // a role is "<project>/<role>"; the Jam context is "jam"
}

// String is "destination anthropic", "role app/ic".
func (k ConfigKey) String() string { return k.Kind + " " + k.Name }

// Path is the object's repo file (a project: its project.yml).
func (k ConfigKey) Path() string {
	switch k.Kind {
	case ConfigKindDestination:
		return "destinations/" + k.Name + ".yml"
	case ConfigKindModelSpec:
		return "model-specs/" + k.Name + ".yml"
	case ConfigKindUser:
		return "users/" + k.Name + ".yml"
	case ConfigKindConnection:
		return "connections/" + k.Name + ".yml"
	case ConfigKindKit:
		return "kits/" + k.Name + "/kit.yml"
	case ConfigKindProject:
		return "projects/" + k.Name + "/project.yml"
	case ConfigKindRole:
		p, r, _ := strings.Cut(k.Name, "/")
		return "projects/" + p + "/roles/" + r + ".yml"
	case ConfigKindJamContext:
		return "context.md"
	}
	return k.Name
}

var (
	unknownFieldRE = regexp.MustCompile(`^line \d+: field (\S+) not found in type`)
	typeLineRE     = regexp.MustCompile(`^line (\d+):`)
)

// decodeStrict decodes one YAML document into out with unknown fields
// refused. Its errors are path-prefixed and never carry a value from the file
// (only an unknown key's name, clipped).
func decodeStrict(path string, data []byte, out any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	err := dec.Decode(out)
	switch {
	case errors.Is(err, io.EOF):
		return fmt.Errorf("%s: empty file", path)
	case err == nil:
		return nil
	}
	var te *yaml.TypeError
	if errors.As(err, &te) && len(te.Errors) > 0 {
		if m := unknownFieldRE.FindStringSubmatch(te.Errors[0]); m != nil {
			return fmt.Errorf("%s: unknown field %q", path, clipName(m[1]))
		}
		if m := typeLineRE.FindStringSubmatch(te.Errors[0]); m != nil {
			return fmt.Errorf("%s: line %s: a value has the wrong type", path, m[1])
		}
		return fmt.Errorf("%s: a value has the wrong type", path)
	}
	return fmt.Errorf("%s: %s", path, strings.TrimPrefix(err.Error(), "yaml: ")) // syntax errors carry no content
}

func clipName(s string) string {
	if len(s) > 64 {
		return s[:64] + "…"
	}
	return s
}

// checkHeader enforces kind:, version: and name: (defaulting an empty name to the file's).
func checkHeader(path, wantKind, kind string, version int, wantName string, name *string) error {
	if kind != wantKind {
		return fmt.Errorf("%s: kind must be %s", path, wantKind)
	}
	if version != ConfigFileVersion {
		return fmt.Errorf("%s: version must be %d", path, ConfigFileVersion)
	}
	if *name == "" {
		*name = wantName
	} else if *name != wantName {
		return fmt.Errorf("%s: name %q does not match the file name %q", path, clipName(*name), wantName)
	}
	return nil
}

// renderYAML is the one renderer config write, digests and the diff use:
// yaml.v3, 2-space indent (maps sorted by yaml.v3). The file types hold only
// strings, numbers, bools, slices, maps and structs, so encoding cannot fail.
func renderYAML(v any) []byte {
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		panic("config: render: " + err.Error()) // unreachable for the file types (tested per type)
	}
	if err := enc.Close(); err != nil {
		panic("config: render: " + err.Error())
	}
	return b.Bytes()
}

// canonFiles is an object's canonical bytes: its files in path order, each
// introduced by its path.
func canonFiles(files map[string][]byte) []byte {
	var b bytes.Buffer
	for _, p := range slices.Sorted(maps.Keys(files)) {
		b.WriteString("# " + p + "\n")
		b.Write(files[p])
	}
	return b.Bytes()
}

func formatConfigDuration(d time.Duration) string {
	if d == 0 {
		return ""
	}
	s := d.String() // "1h30m0s", "5m0s", "30s", "1h0m5s"
	if strings.HasSuffix(s, "m0s") {
		s = s[:len(s)-2] // "1h30m", "5m", "1h0m"
	}
	if strings.HasSuffix(s, "h0m") {
		s = s[:len(s)-2] // "1h"
	}
	return s
}

// parseConfigDuration reads a file duration ("" = 0); field names the key for errors.
func parseConfigDuration(field, s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("%s is not a duration (e.g. 90s, 30m, 4h)", field)
	}
	return d, nil
}

// configFile is any non-kit object in file form.
type configFile interface {
	configKey() ConfigKey
	configFiles() map[string][]byte // repo path → rendered content
}

// configObject is one object in canonical form; Canon is canonFiles(Files),
// except a kit's, which is its stored config JSON.
type configObject struct {
	Key    ConfigKey
	Files  map[string][]byte
	Binary map[string]ConfigFileBlob // a kit's context-dir files, by repo path
	Canon  []byte
}

func newConfigObject(f configFile) configObject {
	files := f.configFiles()
	return configObject{Key: f.configKey(), Files: files, Canon: canonFiles(files)}
}

// Digest is the hex sha256 of the canonical bytes.
func (o configObject) Digest() string {
	sum := sha256.Sum256(o.Canon)
	return hex.EncodeToString(sum[:])
}

// ---- destination ----

// DestinationFile is destinations/<name>.yml.
type DestinationFile struct {
	Kind        string `yaml:"kind"`
	Version     int    `yaml:"version"`
	Destination `yaml:",inline"`
}

func (f DestinationFile) configKey() ConfigKey {
	return ConfigKey{ConfigKindDestination, f.Destination.Name}
}

func (f DestinationFile) configFiles() map[string][]byte {
	var n yaml.Node
	if err := n.Encode(f); err != nil {
		panic("config: render: " + err.Error())
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == "name" { // the file name carries it
			n.Content = slices.Delete(n.Content, i, i+2)
			break
		}
	}
	return map[string][]byte{f.configKey().Path(): renderYAML(&n)}
}

// ParseDestinationFile parses destinations/<name>.yml.
func ParseDestinationFile(path, name string, data []byte) (DestinationFile, error) {
	var f DestinationFile
	if err := decodeStrict(path, data, &f); err != nil {
		return f, err
	}
	err := checkHeader(path, ConfigKindDestination, f.Kind, f.Version, name, &f.Destination.Name)
	return f, err
}

// ---- model spec ----

// ModelSpecFile is model-specs/<name>.yml. The spec's own version (the
// harness release) is written as harness-version, since version: is the
// file's.
type ModelSpecFile struct {
	Kind              string         `yaml:"kind"`
	Version           int            `yaml:"version"`
	Name              string         `yaml:"name,omitempty"`
	Type              HarnessType    `yaml:"type"`
	HarnessVersion    string         `yaml:"harness-version,omitempty"`
	VersionConstraint string         `yaml:"version-constraint,omitempty"`
	Principal         ModelPrincipal `yaml:"principal,omitempty"`
	Model             ModelChoice    `yaml:"model,omitempty"`
	Policy            ModelPolicy    `yaml:"policy,omitempty"`
	Note              string         `yaml:"note,omitempty"`
	Claude            *ClaudeSpec    `yaml:"claude,omitempty"`
}

func (f ModelSpecFile) configKey() ConfigKey { return ConfigKey{ConfigKindModelSpec, f.Name} }

func (f ModelSpecFile) configFiles() map[string][]byte {
	out := f
	out.Name = ""
	return map[string][]byte{f.configKey().Path(): renderYAML(out)}
}

func (f ModelSpecFile) spec() ModelSpec {
	return ModelSpec{
		Name: f.Name, Type: f.Type, Version: f.HarnessVersion, VersionConstraint: f.VersionConstraint,
		Principal: f.Principal, Model: f.Model, Policy: f.Policy, Note: f.Note, Claude: f.Claude,
	}
}

func modelSpecFileOf(ms ModelSpec) ModelSpecFile {
	return ModelSpecFile{
		Kind: ConfigKindModelSpec, Version: ConfigFileVersion, Name: ms.Name, Type: ms.Type,
		HarnessVersion: ms.Version, VersionConstraint: ms.VersionConstraint,
		Principal: ms.Principal, Model: ms.Model, Policy: ms.Policy, Note: ms.Note, Claude: ms.Claude,
	}
}

// ParseModelSpecFile parses model-specs/<name>.yml.
func ParseModelSpecFile(path, name string, data []byte) (ModelSpecFile, error) {
	var f ModelSpecFile
	if err := decodeStrict(path, data, &f); err != nil {
		return f, err
	}
	err := checkHeader(path, ConfigKindModelSpec, f.Kind, f.Version, name, &f.Name)
	return f, err
}

// ---- user ----

// UserFile is users/<name>.yml: a user and the logins that resolve to it.
type UserFile struct {
	Kind    string   `yaml:"kind"`
	Version int      `yaml:"version"`
	Name    string   `yaml:"name,omitempty"`
	Logins  []string `yaml:"logins,omitempty"`
}

func (f UserFile) configKey() ConfigKey { return ConfigKey{ConfigKindUser, f.Name} }

func (f UserFile) configFiles() map[string][]byte {
	out := f
	out.Name = ""
	return map[string][]byte{f.configKey().Path(): renderYAML(out)}
}

// ParseUserFile parses users/<name>.yml.
func ParseUserFile(path, name string, data []byte) (UserFile, error) {
	var f UserFile
	if err := decodeStrict(path, data, &f); err != nil {
		return f, err
	}
	err := checkHeader(path, ConfigKindUser, f.Kind, f.Version, name, &f.Name)
	return f, err
}

// ---- connection ----

// ConnectionFile is connections/<name>.yml. The connection's service kind is
// service: (kind: is the file's); cred: names the credential, never its value.
type ConnectionFile struct {
	Kind    string `yaml:"kind"`
	Version int    `yaml:"version"`
	Name    string `yaml:"name,omitempty"`
	Service string `yaml:"service"`
	Cred    string `yaml:"cred,omitempty"`
}

func (f ConnectionFile) configKey() ConfigKey { return ConfigKey{ConfigKindConnection, f.Name} }

func (f ConnectionFile) configFiles() map[string][]byte {
	out := f
	out.Name = ""
	return map[string][]byte{f.configKey().Path(): renderYAML(out)}
}

// ParseConnectionFile parses connections/<name>.yml.
func ParseConnectionFile(path, name string, data []byte) (ConnectionFile, error) {
	var f ConnectionFile
	if err := decodeStrict(path, data, &f); err != nil {
		return f, err
	}
	err := checkHeader(path, ConfigKindConnection, f.Kind, f.Version, name, &f.Name)
	return f, err
}

// ---- project ----

// EscalationTierFile is one escalation tier.
type EscalationTierFile struct {
	Targets []string `yaml:"targets"`
	Timeout string   `yaml:"timeout,omitempty"`
}

// DeliveryFile is one delivery address of a member.
type DeliveryFile struct {
	Service string `yaml:"service"`
	Address string `yaml:"address"`
}

// MemberFile is a project member, by user name.
type MemberFile struct {
	User     string         `yaml:"user"`
	Delivery []DeliveryFile `yaml:"delivery,omitempty"`
}

// RoomBindingFile binds a room to a connection's external ref.
type RoomBindingFile struct {
	Connection string `yaml:"connection"`
	Ref        string `yaml:"ref"`
	Mode       string `yaml:"mode,omitempty"` // "" = both
}

// RoomFile is a project room.
type RoomFile struct {
	Key      string            `yaml:"key"`
	Label    string            `yaml:"label,omitempty"` // "" = the key
	Bindings []RoomBindingFile `yaml:"bindings,omitempty"`
}

// ProjectFile is projects/<name>/project.yml; Context is that project's
// context.md, loaded alongside it.
type ProjectFile struct {
	Kind                 string                          `yaml:"kind"`
	Version              int                             `yaml:"version"`
	Name                 string                          `yaml:"name,omitempty"`
	ChatService          string                          `yaml:"chat-service,omitempty"`
	Escalation           []EscalationTierFile            `yaml:"escalation,omitempty"`
	EscalationByCategory map[string][]EscalationTierFile `yaml:"escalation-by-category,omitempty"`
	Resources            []sessionctx.Resource           `yaml:"resources,omitempty"`
	Members              []MemberFile                    `yaml:"members,omitempty"`
	Rooms                []RoomFile                      `yaml:"rooms,omitempty"`
	Context              sessionctx.Layer                `yaml:"-"`
}

func (f ProjectFile) configKey() ConfigKey { return ConfigKey{ConfigKindProject, f.Name} }

func (f ProjectFile) configFiles() map[string][]byte {
	out := f
	out.Name = ""
	files := map[string][]byte{f.configKey().Path(): renderYAML(out)}
	if !f.Context.Empty() {
		files["projects/"+f.Name+"/context.md"] = RenderContextMarkdown(f.Context)
	}
	return files
}

func normalizeTiers(field string, tiers []EscalationTierFile) error {
	for i := range tiers {
		d, err := parseConfigDuration(fmt.Sprintf("%s[%d].timeout", field, i), tiers[i].Timeout)
		if err != nil {
			return err
		}
		tiers[i].Timeout = formatConfigDuration(d)
	}
	return nil
}

// normalize puts the file in canonical form: members, rooms and each room's
// bindings sorted, a room label equal to its key and a binding mode of both
// dropped, durations canonical.
func (f *ProjectFile) normalize() error {
	slices.SortStableFunc(f.Members, func(a, b MemberFile) int { return strings.Compare(a.User, b.User) })
	slices.SortStableFunc(f.Rooms, func(a, b RoomFile) int { return strings.Compare(a.Key, b.Key) })
	for i := range f.Rooms {
		slices.SortStableFunc(f.Rooms[i].Bindings, func(a, b RoomBindingFile) int {
			return cmp.Or(strings.Compare(a.Connection, b.Connection), strings.Compare(a.Ref, b.Ref))
		})
		if f.Rooms[i].Label == f.Rooms[i].Key {
			f.Rooms[i].Label = ""
		}
		for j := range f.Rooms[i].Bindings {
			if f.Rooms[i].Bindings[j].Mode == "both" {
				f.Rooms[i].Bindings[j].Mode = ""
			}
		}
	}
	if err := normalizeTiers("escalation", f.Escalation); err != nil {
		return err
	}
	for _, c := range slices.Sorted(maps.Keys(f.EscalationByCategory)) {
		if err := normalizeTiers("escalation-by-category."+c, f.EscalationByCategory[c]); err != nil {
			return err
		}
	}
	return nil
}

// ParseProjectFile parses projects/<name>/project.yml (normalized). Its
// context.md is the loader's to attach.
func ParseProjectFile(path, name string, data []byte) (ProjectFile, error) {
	var f ProjectFile
	if err := decodeStrict(path, data, &f); err != nil {
		return f, err
	}
	if err := checkHeader(path, ConfigKindProject, f.Kind, f.Version, name, &f.Name); err != nil {
		return f, err
	}
	if err := f.normalize(); err != nil {
		return f, fmt.Errorf("%s: %w", path, err)
	}
	return f, nil
}

// ---- role ----

// RoleScopeFile is a role's scope.
type RoleScopeFile struct {
	Destinations []string          `yaml:"destinations,omitempty"`
	Credentials  map[string]string `yaml:"credentials,omitempty"`
	Addressing   []string          `yaml:"addressing,omitempty"`
	TTL          string            `yaml:"ttl,omitempty"`
	Egress       *[]string         `yaml:"egress,omitempty"` // nil = the kit's default list
}

// StandingFile is one standing session.
type StandingFile struct {
	Name   string `yaml:"name"`
	Prompt string `yaml:"prompt,omitempty"`
}

// AllocationFile is a role's allocation policy.
type AllocationFile struct {
	MaxEphemeral        int            `yaml:"max-ephemeral,omitempty"`
	MaxPersonal         int            `yaml:"max-personal,omitempty"`
	MaxPersonalPerOwner int            `yaml:"max-personal-per-owner,omitempty"`
	IdleAfter           string         `yaml:"idle-after,omitempty"`
	NagEvery            string         `yaml:"nag-every,omitempty"`
	ReclaimAfter        string         `yaml:"reclaim-after,omitempty"`
	Standing            []StandingFile `yaml:"standing,omitempty"`
}

// TurnEndFile is a role's turn-end policy.
type TurnEndFile struct {
	IdleTimeout string `yaml:"idle-timeout,omitempty"`
	OnIdle      string `yaml:"on-idle,omitempty"`
	TimeZone    string `yaml:"time-zone,omitempty"`
}

// CollaborateFile is a role's collaboration policy.
type CollaborateFile struct {
	Preview string `yaml:"preview,omitempty"`
	SSH     string `yaml:"ssh,omitempty"`
}

// ContextFile is a role's inline session-context layer.
type ContextFile struct {
	Core   string            `yaml:"core,omitempty"`
	Leaves []sessionctx.Leaf `yaml:"leaves,omitempty"`
}

// RoleFile is projects/<project>/roles/<name>.yml. Project is the directory's,
// not a file key.
type RoleFile struct {
	Kind        string          `yaml:"kind"`
	Version     int             `yaml:"version"`
	Name        string          `yaml:"name,omitempty"`
	Project     string          `yaml:"-"`
	Scope       RoleScopeFile   `yaml:"scope,omitempty"`
	Kit         string          `yaml:"kit,omitempty"`
	ModelSpec   string          `yaml:"model-spec,omitempty"`
	Allocation  AllocationFile  `yaml:"allocation,omitempty"`
	TurnEnd     TurnEndFile     `yaml:"turn-end,omitempty"`
	Collaborate CollaborateFile `yaml:"collaborate,omitempty"`
	Context     *ContextFile    `yaml:"context,omitempty"`
}

func (f RoleFile) configKey() ConfigKey {
	return ConfigKey{ConfigKindRole, f.Project + "/" + f.Name}
}

func (f RoleFile) configFiles() map[string][]byte {
	out := f
	out.Name = ""
	return map[string][]byte{f.configKey().Path(): renderYAML(out)}
}

func negCheck(field string, n int) error {
	if n < 0 {
		return fmt.Errorf("allocation.%s must be >= 0", field)
	}
	return nil
}

// role converts the file to a Role: durations parsed, negatives refused,
// egress normalized. Its errors carry no path.
func (f RoleFile) role() (Role, error) {
	r := Role{Name: f.Name, Kit: f.Kit, ModelSpec: f.ModelSpec}
	r.Scope = Scope{
		Destinations: slices.Clone(f.Scope.Destinations), Credentials: maps.Clone(f.Scope.Credentials),
		Addressing: slices.Clone(f.Scope.Addressing),
	}
	var err error
	if r.Scope.TTL, err = parseConfigDuration("scope.ttl", f.Scope.TTL); err != nil {
		return r, err
	}
	if f.Scope.Egress != nil {
		d, err := NormalizeEgress(*f.Scope.Egress)
		if err != nil {
			return r, fmt.Errorf("scope.egress: %w", err)
		}
		r.Scope.Egress = &EgressPolicy{Domains: d}
	}
	a := f.Allocation
	for _, c := range []struct {
		k string
		n int
	}{{"max-ephemeral", a.MaxEphemeral}, {"max-personal", a.MaxPersonal}, {"max-personal-per-owner", a.MaxPersonalPerOwner}} {
		if err := negCheck(c.k, c.n); err != nil {
			return r, err
		}
	}
	r.Allocation = RoleAllocation{MaxEphemeral: a.MaxEphemeral, MaxPersonal: a.MaxPersonal, MaxPersonalPerOwner: a.MaxPersonalPerOwner}
	if r.Allocation.IdleAfter, err = parseConfigDuration("allocation.idle-after", a.IdleAfter); err != nil {
		return r, err
	}
	if r.Allocation.NagEvery, err = parseConfigDuration("allocation.nag-every", a.NagEvery); err != nil {
		return r, err
	}
	if r.Allocation.ReclaimAfter, err = parseConfigDuration("allocation.reclaim-after", a.ReclaimAfter); err != nil {
		return r, err
	}
	for _, s := range a.Standing {
		r.Allocation.Standing = append(r.Allocation.Standing, StandingSession{Name: s.Name, Prompt: s.Prompt})
	}
	t := TurnEndPolicy{OnIdle: f.TurnEnd.OnIdle, TimeZone: f.TurnEnd.TimeZone}
	if t.IdleTimeout, err = parseConfigDuration("turn-end.idle-timeout", f.TurnEnd.IdleTimeout); err != nil {
		return r, err
	}
	r.TurnEnd = t
	r.Collaborate = CollaboratePolicy{Preview: f.Collaborate.Preview, SSH: f.Collaborate.SSH}
	if f.Context != nil {
		r.Context = sessionctx.Layer{Core: f.Context.Core, Leaves: slices.Clone(f.Context.Leaves)}
	}
	return r, nil
}

// roleFileOf is a role in canonical file form. userName resolves a user id in
// an addressing entry to the user's name; unresolved entries stay verbatim.
func roleFileOf(project, name string, r Role, userName func(ident.ID) (string, bool)) RoleFile {
	f := RoleFile{Kind: ConfigKindRole, Version: ConfigFileVersion, Name: name, Project: project, Kit: r.Kit, ModelSpec: r.ModelSpec}
	f.Scope = RoleScopeFile{
		Destinations: slices.Clone(r.Scope.Destinations), Credentials: maps.Clone(r.Scope.Credentials),
		TTL: formatConfigDuration(r.Scope.TTL),
	}
	for _, a := range r.Scope.Addressing {
		if rest, ok := strings.CutPrefix(a, "user:"); ok {
			if id, err := ident.Parse(rest); err == nil && id.Kind() == ident.User {
				if n, ok := userName(id); ok {
					a = "user:" + n
				}
			}
		}
		f.Scope.Addressing = append(f.Scope.Addressing, a)
	}
	if r.Scope.Egress != nil {
		d := slices.Clone(r.Scope.Egress.Domains)
		if d == nil {
			d = []string{}
		}
		f.Scope.Egress = &d
	}
	a := r.Allocation
	f.Allocation = AllocationFile{
		MaxEphemeral: a.MaxEphemeral, MaxPersonal: a.MaxPersonal, MaxPersonalPerOwner: a.MaxPersonalPerOwner,
		IdleAfter: formatConfigDuration(a.IdleAfter), NagEvery: formatConfigDuration(a.NagEvery),
		ReclaimAfter: formatConfigDuration(a.ReclaimAfter),
	}
	for _, s := range a.Standing {
		f.Allocation.Standing = append(f.Allocation.Standing, StandingFile{Name: s.Name, Prompt: s.Prompt})
	}
	f.TurnEnd = TurnEndFile{IdleTimeout: formatConfigDuration(r.TurnEnd.IdleTimeout), OnIdle: r.TurnEnd.OnIdle, TimeZone: r.TurnEnd.TimeZone}
	f.Collaborate = CollaborateFile{Preview: r.Collaborate.Preview, SSH: r.Collaborate.SSH}
	if !r.Context.Empty() {
		f.Context = &ContextFile{Core: r.Context.Core, Leaves: slices.Clone(r.Context.Leaves)}
	}
	return f
}

var rolePathRE = regexp.MustCompile(`(?:^|/)projects/([^/]+)/roles/[^/]+$`)

// ParseRoleFile parses projects/<project>/roles/<name>.yml (normalized). The
// project is read from the path.
func ParseRoleFile(path, name string, data []byte) (RoleFile, error) {
	var f RoleFile
	if err := decodeStrict(path, data, &f); err != nil {
		return f, err
	}
	if err := checkHeader(path, ConfigKindRole, f.Kind, f.Version, name, &f.Name); err != nil {
		return f, err
	}
	if m := rolePathRE.FindStringSubmatch(path); m != nil {
		f.Project = m[1]
	}
	r, err := f.role()
	if err != nil {
		return f, fmt.Errorf("%s: %w", path, err)
	}
	return roleFileOf(f.Project, name, r, func(ident.ID) (string, bool) { return "", false }), nil
}

// ---- Jam context and context.md ----

// JamContextFile is the Jam-wide context.md.
type JamContextFile struct{ Layer sessionctx.Layer }

func (f JamContextFile) configKey() ConfigKey { return ConfigKey{ConfigKindJamContext, "jam"} }

func (f JamContextFile) configFiles() map[string][]byte {
	if f.Layer.Empty() {
		return map[string][]byte{}
	}
	return map[string][]byte{"context.md": RenderContextMarkdown(f.Layer)}
}

type contextFront struct {
	Leaves []sessionctx.Leaf `yaml:"leaves"`
}

// ParseContextMarkdown reads a context.md: optional YAML front matter holding
// the leaves, then the core as the body.
func ParseContextMarkdown(path string, data []byte) (sessionctx.Layer, error) {
	s := strings.ReplaceAll(string(data), "\r\n", "\n")
	var l sessionctx.Layer
	body := s
	if rest, ok := strings.CutPrefix(s, "---\n"); ok {
		var fm string
		switch {
		case strings.HasPrefix(rest, "---\n"):
			body = rest[4:]
		case rest == "---":
			body = ""
		default:
			i := strings.Index(rest, "\n---\n")
			switch {
			case i >= 0:
				fm, body = rest[:i+1], rest[i+5:]
			case strings.HasSuffix(rest, "\n---"):
				fm, body = rest[:len(rest)-3], ""
			default:
				return l, fmt.Errorf("%s: front matter is not closed with ---", path)
			}
		}
		if strings.TrimSpace(fm) != "" {
			var front contextFront
			if err := decodeStrict(path, []byte(fm), &front); err != nil {
				return l, err
			}
			l.Leaves = front.Leaves
		}
	}
	l.Core = strings.TrimRight(body, "\n")
	return l, nil
}

// RenderContextMarkdown is ParseContextMarkdown's inverse.
// A core that opens with --- and has no leaves gets empty front matter, so
// it is not read back as front matter.
func RenderContextMarkdown(l sessionctx.Layer) []byte {
	var b bytes.Buffer
	core := strings.TrimRight(l.Core, "\n")
	switch {
	case len(l.Leaves) > 0:
		b.WriteString("---\n")
		b.Write(renderYAML(contextFront{Leaves: l.Leaves}))
		b.WriteString("---\n")
	case strings.HasPrefix(core, "---"):
		b.WriteString("---\n---\n")
	}
	if strings.TrimSpace(core) != "" {
		b.WriteString(core + "\n")
	}
	return b.Bytes()
}

// ConfigKindKitSource is the file kind of kits/<name>.source.yml; the object
// it names is a kit (ConfigKindKit).
const ConfigKindKitSource = "kit-source"

// KitSourceFile is kits/<name>.source.yml: a kit kept in its own git repo,
// fetched and tracked by the config apply.
type KitSourceFile struct {
	Kind    string `yaml:"kind"`
	Version int    `yaml:"version"`
	Name    string `yaml:"name,omitempty"`
	Git     string `yaml:"git"`
}

// ParseKitSourceFile parses kits/<name>.source.yml and its git: source. A
// bad source is refused with gitsource.Parse's fixed text, never the value.
func ParseKitSourceFile(path, name string, data []byte) (KitSourceFile, gitsource.Source, error) {
	var f KitSourceFile
	if err := decodeStrict(path, data, &f); err != nil {
		return f, gitsource.Source{}, err
	}
	if err := checkHeader(path, ConfigKindKitSource, f.Kind, f.Version, name, &f.Name); err != nil {
		return f, gitsource.Source{}, err
	}
	if f.Git == "" {
		return f, gitsource.Source{}, fmt.Errorf("%s: git is required (<repo>[//<dir>]@<ref>)", path)
	}
	src, err := gitsource.Parse(f.Git)
	if err != nil {
		return f, gitsource.Source{}, fmt.Errorf("%s: git: %s", path, err)
	}
	return f, src, nil
}

// sourceKitPath is the repo file of a kit kept by reference.
func sourceKitPath(name string) string { return "kits/" + name + ".source.yml" }

// kitContextDir is the base.context-dir a written kit's context is rendered
// to, relative to its kit.yml.
const kitContextDir = "context"

// kitContextPath is the repo dir of kit name's rendered context.
func kitContextPath(name string) string { return "kits/" + name + "/" + kitContextDir }

// renderKitSourceFile renders a tracked kit's reference (its commit is Jam's,
// not the repo's).
func renderKitSourceFile(src KitSource) []byte {
	return renderYAML(KitSourceFile{Kind: ConfigKindKitSource, Version: ConfigFileVersion,
		Git: gitsource.Source{Repo: src.Repo, Dir: src.Dir, Ref: src.Ref}.String()})
}
