package jam

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
)

// WithSetupInspect mounts the setup inspection routes (inside the operator
// authenticator, like every /admin/* route):
//
//	GET /admin/roles/{project}/{role}/setup   — what a raise now would get
//	GET /admin/instances/{id}/setup           — what a studio got (?diff=role)
//
// both taking ?sections=a,b; the role route also ?manager=<name> (the manager
// whose image is read). Each call is logged "setup inspected" with the
// operator, subject and sections — never a body.
func WithSetupInspect(d SetupDeps, log *slog.Logger) AdminOption {
	sectionsOf := func(w http.ResponseWriter, r *http.Request) ([]string, bool) {
		s, err := ParseSetupSections(r.URL.Query().Get("sections"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return nil, false
		}
		return s, true
	}
	audit := func(r *http.Request, subject string, sections []string) {
		log.Info("setup inspected", "operator", OperatorID(r), "subject", subject, "sections", strings.Join(sections, ","))
	}
	// fail maps a render error to its fixed-string response; never echoes a body.
	fail := func(w http.ResponseWriter, err error, subject, notFound string) {
		if code, ok := ManagerErrStatus(err); ok {
			http.Error(w, err.Error(), code)
			return
		}
		switch {
		case errors.Is(err, ErrNotFound):
			http.Error(w, notFound, http.StatusNotFound)
		case errors.Is(err, ErrUnknownSection):
			http.Error(w, err.Error(), http.StatusBadRequest)
		default:
			log.Warn("setup inspect failed", "subject", subject, "err", err.Error())
			http.Error(w, "setup unavailable", http.StatusInternalServerError)
		}
	}
	return func(mux *adminMux) {
		mux.HandleFunc("GET /admin/roles/{project}/{role}/setup", func(w http.ResponseWriter, r *http.Request) {
			sections, ok := sectionsOf(w, r)
			if !ok {
				return
			}
			project, role := r.PathValue("project"), r.PathValue("role")
			s, err := RoleSetup(r.Context(), d, project, role, sections, r.URL.Query().Get("manager"))
			if err != nil {
				fail(w, err, "role "+project+"/"+role, fmt.Sprintf("role %s/%s does not exist", project, role))
				return
			}
			audit(r, s.Subject, sections)
			writeJSON(w, http.StatusOK, s)
		})
		mux.HandleFunc("GET /admin/instances/{id}/setup", func(w http.ResponseWriter, r *http.Request) {
			sections, ok := sectionsOf(w, r)
			if !ok {
				return
			}
			diff := r.URL.Query().Get("diff")
			if diff != "" && diff != "role" {
				http.Error(w, `diff: want "role"`, http.StatusBadRequest)
				return
			}
			id := r.PathValue("id")
			s, err := InstanceSetup(r.Context(), d, id, sections, diff == "role")
			if err != nil {
				fail(w, err, "studio "+id, fmt.Sprintf("studio %s not found", id))
				return
			}
			audit(r, s.Subject, sections)
			writeJSON(w, http.StatusOK, s)
		})
	}
}
