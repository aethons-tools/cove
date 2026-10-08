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

// contextFile is the authored-context YAML shared by `at-jam context set` and
// the admin UI's Session context card.
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
		return ContextBody{}, errors.New("the context sets nothing; to remove a layer, clear it")
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

// MarshalContextYAML renders b in the ParseContextYAML format (bodies inline).
func MarshalContextYAML(b ContextBody) ([]byte, error) { return yaml.Marshal(b) }

// SetRoleContextChecked validates and stores a role's authored context (400 on
// bad input, 404 for a missing role).
func SetRoleContextChecked(store Store, project, role string, b ContextBody) error {
	if len(b.Resources) > 0 {
		return writeErr(http.StatusBadRequest, "resources apply to projects only")
	}
	return SetRoleContext(store, project, role, b.layer())
}

// SetProjectContextChecked validates and stores a project's authored context
// and resources; the delivered core (with the resources pointer) must fit the
// budget, and resources.md is reserved. An empty body clears.
func SetProjectContextChecked(store Store, project string, b ContextBody) error {
	if err := validateProjectContext(b.layer(), b.Resources); err != nil {
		return writeErr(http.StatusBadRequest, "%s", err.Error())
	}
	if err := store.SetProjectContext(project, b.layer(), b.Resources); err != nil {
		return writeErr(projectErrStatus(err, http.StatusInternalServerError), "%s", err.Error())
	}
	return nil
}

// SetJamContextChecked validates and stores the Jam-wide authored context; an
// empty body clears it.
func SetJamContextChecked(store Store, b ContextBody) error {
	if len(b.Resources) > 0 {
		return writeErr(http.StatusBadRequest, "resources apply to projects only")
	}
	if err := sessionctx.ValidateLayer(b.layer(), sessionctx.BudgetJam); err != nil {
		return writeErr(http.StatusBadRequest, "jam context: %s", err.Error())
	}
	return store.SetJamContext(b.layer())
}

// validateProjectContext is the project-layer rule set, shared by the admin
// API and config import: budgets (counting the generated resources pointer),
// leaf rules, resources, and the reserved resources.md name.
func validateProjectContext(l sessionctx.Layer, rs []sessionctx.Resource) error {
	if err := sessionctx.ValidateLayer(l, sessionctx.BudgetProject); err != nil {
		return fmt.Errorf("project context: %w", err)
	}
	if err := sessionctx.ValidateResources(rs); err != nil {
		return fmt.Errorf("project resources: %w", err)
	}
	for _, lf := range l.Leaves {
		if lf.Name == sessionctx.ResourcesLeaf {
			return fmt.Errorf("project context: leaf name %s is reserved for the generated resource list", sessionctx.ResourcesLeaf)
		}
	}
	if n := len(sessionctx.ProjectLayer(l, rs).Core); n > sessionctx.BudgetProject {
		return fmt.Errorf("project context: core plus the resources pointer is %d bytes; the budget is %d", n, sessionctx.BudgetProject)
	}
	return nil
}
