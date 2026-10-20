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
// change (and after a raise clears them). It is called outside instMu.
type ExposureSink interface {
	ExposuresChanged(actorID string, ports []int32)
}

// SetExposureSink wires the sink (once, at wiring; nil → nobody is told).
func (s *Supervisor) SetExposureSink(sink ExposureSink) { s.exposureSink = sink }

// exposedPorts is the sorted distinct ports of exps.
func exposedPorts(exps []Exposure) []int32 {
	ports := make([]int32, 0, len(exps))
	for _, e := range exps {
		ports = append(ports, int32(e.Port))
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i] < ports[j] })
	return slices.Compact(ports)
}

func (s *Supervisor) notifyExposures(actorID string, exps []Exposure) {
	if s.exposureSink != nil {
		s.exposureSink.ExposuresChanged(actorID, exposedPorts(exps))
	}
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
	s.notifyExposures(actorID, exps)
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
	exps := inst.Exposures
	s.instMu.Unlock()
	if s.log != nil {
		s.log.Info("preview unexposed", "session", actorID, "name", name, "port", port)
	}
	s.notifyExposures(actorID, exps)
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
