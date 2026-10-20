package jam

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/aethons-tools/cove/internal/jam/preview"
)

// Exposure is a port a session has exposed as a named studio preview.
type Exposure struct {
	Name      string    `json:"name"`
	Port      int       `json:"port"`
	CreatedAt time.Time `json:"created_at"`
}

var (
	// ErrExposureLimit: the session already exposes preview.MaxExposures ports.
	ErrExposureLimit = fmt.Errorf("exposure limit reached (%d per session)", preview.MaxExposures)
	// ErrNoExposure: no exposure of that name.
	ErrNoExposure = errors.New("no such exposure")
)

// ExposureSink is told a session's full set of exposed ports after every
// change, after a raise clears them, and (as an empty set) after a teardown
// removes the instance. It is called outside instMu but while holding the
// supervisor's notify mutex, so implementations MUST be fast and non-blocking
// (store and signal only) and MUST NOT call back into the Supervisor. (The
// attach server's own source call takes Server.mu then instMu, a bounded wait:
// nothing holding instMu calls into the server.)
type ExposureSink interface {
	ExposuresChanged(actorID string, ports []int32)
}

// SetExposureSink wires the sink (once, at wiring; nil → nobody is told).
func (s *Supervisor) SetExposureSink(sink ExposureSink) { s.exposureSink = sink }

// SetPreviewsEnabled records that studio previews are on for this Jam, so the
// session context tells agents about expose (once, at wiring).
func (s *Supervisor) SetPreviewsEnabled(on bool) { s.previews = on }

// exposedPorts is the sorted distinct ports of exps.
func exposedPorts(exps []Exposure) []int32 {
	ports := make([]int32, 0, len(exps))
	for _, e := range exps {
		ports = append(ports, int32(e.Port))
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i] < ports[j] })
	return slices.Compact(ports)
}

// ExposedPorts is the sorted distinct ports the session currently exposes.
func (s *Supervisor) ExposedPorts(actorID string) []int32 {
	return exposedPorts(s.Exposures(actorID))
}

// notifyExposures tells the sink the actor's CURRENT exposed ports. notifyMu is
// held across the re-read and the call, so concurrent changes cannot deliver
// out of order: the last call always carries the latest stored set. It is taken
// before instMu (never while holding it), so the sink runs outside instMu.
func (s *Supervisor) notifyExposures(actorID string) {
	if s.exposureSink == nil {
		return
	}
	s.notifyMu.Lock()
	defer s.notifyMu.Unlock()
	s.instMu.Lock()
	inst, _ := s.store.GetInstance(actorID)
	exps := slices.Clone(inst.Exposures)
	s.instMu.Unlock()
	s.exposureSink.ExposuresChanged(actorID, exposedPorts(exps))
}

// Expose publishes port as the named preview (replacing one of the same name).
func (s *Supervisor) Expose(actorID, name string, port int) (Exposure, error) {
	if err := preview.ValidName(name); err != nil {
		return Exposure{}, err
	}
	if err := preview.ValidPort(port); err != nil {
		return Exposure{}, err
	}
	s.instMu.Lock()
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		s.instMu.Unlock()
		return Exposure{}, fmt.Errorf("no instance for actor %q", actorID)
	}
	if inst.Phase != PhaseLive && inst.Phase != PhaseIdled {
		s.instMu.Unlock()
		return Exposure{}, fmt.Errorf("session %q is %s: cannot expose a preview", actorID, inst.Phase)
	}
	e := Exposure{Name: name, Port: port, CreatedAt: s.now()}
	exps := slices.Clone(inst.Exposures)
	if i := slices.IndexFunc(exps, func(x Exposure) bool { return x.Name == name }); i >= 0 {
		exps[i] = e
	} else if len(exps) >= preview.MaxExposures {
		s.instMu.Unlock()
		return Exposure{}, ErrExposureLimit
	} else {
		exps = append(exps, e)
	}
	inst.Exposures = exps
	if err := s.store.PutInstance(inst); err != nil {
		s.instMu.Unlock()
		return Exposure{}, err
	}
	s.instMu.Unlock()
	if s.log != nil {
		s.log.Info("preview exposed", "session", actorID, "name", name, "port", port)
	}
	s.notifyExposures(actorID)
	return e, nil
}

// Unexpose removes the named preview (ErrNoExposure if absent).
func (s *Supervisor) Unexpose(actorID, name string) error {
	s.instMu.Lock()
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		s.instMu.Unlock()
		return fmt.Errorf("no instance for actor %q", actorID)
	}
	i := slices.IndexFunc(inst.Exposures, func(x Exposure) bool { return x.Name == name })
	if i < 0 {
		s.instMu.Unlock()
		return ErrNoExposure
	}
	port := inst.Exposures[i].Port
	inst.Exposures = slices.Delete(slices.Clone(inst.Exposures), i, i+1)
	if err := s.store.PutInstance(inst); err != nil {
		s.instMu.Unlock()
		return err
	}
	s.instMu.Unlock()
	if s.log != nil {
		s.log.Info("preview unexposed", "session", actorID, "name", name, "port", port)
	}
	s.notifyExposures(actorID)
	return nil
}

// Exposures lists the session's exposed previews (nil if none or unknown).
func (s *Supervisor) Exposures(actorID string) []Exposure {
	s.instMu.Lock()
	defer s.instMu.Unlock()
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		return nil
	}
	return slices.Clone(inst.Exposures)
}
