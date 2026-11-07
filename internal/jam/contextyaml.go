package jam

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// contextFile is the authored-context YAML used by the admin UI's Session
// context card.
type contextFile struct {
	Core   string `yaml:"core"`
	Leaves []struct {
		Name     string `yaml:"name"`
		ReadWhen string `yaml:"read-when"`
		Body     string `yaml:"body"`
		File     string `yaml:"file"`
	} `yaml:"leaves"`
	Resources []sessionctx.Resource `yaml:"resources"`
}

// ParseContextYAML decodes an authored-context YAML strictly. A leaf sets body
// or file, not both; readFile resolves file (nil = file is refused, as in the
// UI). A file that sets nothing is refused: clearing is its own action.
func ParseContextYAML(data []byte, readFile func(name string) ([]byte, error)) (ContextBody, error) {
	var f contextFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return ContextBody{}, err
	}
	if strings.TrimSpace(f.Core) == "" && len(f.Leaves) == 0 && len(f.Resources) == 0 {
		return ContextBody{}, errSetsNothing
	}
	b := ContextBody{Core: f.Core, Resources: f.Resources}
	for _, lf := range f.Leaves {
		body := lf.Body
		switch {
		case lf.Body != "" && lf.File != "":
			return ContextBody{}, fmt.Errorf("leaf %q: set body or file, not both", lf.Name)
		case lf.File != "" && readFile == nil:
			return ContextBody{}, fmt.Errorf("leaf %q: file is not allowed here; inline the body", lf.Name)
		case lf.File != "":
			raw, err := readFile(lf.File)
			if err != nil {
				return ContextBody{}, fmt.Errorf("leaf %q: %w", lf.Name, err)
			}
			body = string(raw)
		}
		b.Leaves = append(b.Leaves, sessionctx.Leaf{Name: lf.Name, ReadWhen: lf.ReadWhen, Body: body})
	}
	return b, nil
}

var errSetsNothing = errors.New("the context sets nothing; to remove a layer, clear it")

// NewContextBody is a context save sent as fields (the admin UI's structured
// editor) rather than YAML; like ParseContextYAML, it refuses one that sets
// nothing. Validation is the Set*ContextChecked call's.
func NewContextBody(core string, leaves []sessionctx.Leaf, resources []sessionctx.Resource) (ContextBody, error) {
	if strings.TrimSpace(core) == "" && len(leaves) == 0 && len(resources) == 0 {
		return ContextBody{}, errSetsNothing
	}
	return ContextBody{Core: core, Leaves: leaves, Resources: resources}, nil
}

// MarshalContextYAML renders b in the ParseContextYAML format (bodies inline).
func MarshalContextYAML(b ContextBody) ([]byte, error) { return yaml.Marshal(b) }

// SetRoleContextChecked validates and stores a role's authored context (400 on
// bad input, carrying every problem's field; 404 for a missing role).
func SetRoleContextChecked(store Store, project, role string, b ContextBody) error {
	if fe := append(resourcesRefused(b), sessionctx.CheckLayer(b.layer(), sessionctx.BudgetRole)...); len(fe) > 0 {
		return contextFieldsErr("role", fe)
	}
	return SetRoleContext(store, project, role, b.layer())
}

// SetProjectContextChecked validates and stores a project's authored context
// and resources; the delivered core (with the resources pointer) must fit the
// budget, and resources.md is reserved. An empty body clears. A refusal is a
// 400 carrying every problem's field.
func SetProjectContextChecked(store Store, project string, b ContextBody) error {
	if err := contextFieldsErr("project", checkProjectContext(b.layer(), b.Resources)); err != nil {
		return err
	}
	if err := store.SetProjectContext(project, b.layer(), b.Resources); err != nil {
		return writeErr(projectErrStatus(err, http.StatusInternalServerError), "%s", err.Error())
	}
	return nil
}

// SetJamContextChecked validates and stores the Jam-wide authored context; an
// empty body clears it. A refusal is a 400 carrying every problem's field.
func SetJamContextChecked(store Store, b ContextBody) error {
	if err := contextFieldsErr("jam", append(resourcesRefused(b), sessionctx.CheckLayer(b.layer(), sessionctx.BudgetJam)...)); err != nil {
		return err
	}
	return store.SetJamContext(b.layer())
}

// resourcesRefused is the problem with resources on a layer other than a
// project's.
func resourcesRefused(b ContextBody) sessionctx.FieldErrors {
	if len(b.Resources) == 0 {
		return nil
	}
	return sessionctx.FieldErrors{{Field: "resources", Message: "resources apply to projects only"}}
}

// validateProjectContext is checkProjectContext as one error, for config
// import.
func validateProjectContext(l sessionctx.Layer, rs []sessionctx.Resource) error {
	if fe := checkProjectContext(l, rs); len(fe) > 0 {
		return errors.New(contextMessage("project", fe))
	}
	return nil
}

// checkProjectContext is the project-layer rule set, shared by the admin API
// and config import: budgets (counting the generated resources pointer), leaf
// rules, resources, and the reserved resources.md name. It returns every
// problem found.
func checkProjectContext(l sessionctx.Layer, rs []sessionctx.Resource) sessionctx.FieldErrors {
	fe := sessionctx.CheckLayer(l, sessionctx.BudgetProject)
	fe = append(fe, sessionctx.CheckResources(rs)...)
	for i, lf := range l.Leaves {
		if lf.Name == sessionctx.ResourcesLeaf {
			fe = append(fe, sessionctx.FieldError{Field: fmt.Sprintf("leaves[%d].name", i),
				Message: fmt.Sprintf("leaf name %s is reserved for the generated resource list", sessionctx.ResourcesLeaf)})
		}
	}
	// The pointer's own bytes only matter once the authored core fits.
	if n := len(sessionctx.ProjectLayer(l, rs).Core); n > sessionctx.BudgetProject && len(strings.TrimSpace(l.Core)) <= sessionctx.BudgetProject {
		fe = append(fe, sessionctx.FieldError{Field: "core",
			Message: fmt.Sprintf("core plus the resources pointer is %d bytes; the budget is %d", n, sessionctx.BudgetProject)})
	}
	return fe
}
