package main

import (
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/condition"
)

// managerGauges are the /metrics series for Studio Managers: the registered
// and connected counts, and each connected manager's last host report
// (memory, disk, reclaimable), grouped by metric.
func managerGauges(ms []jam.ManagerStatus) []condition.Gauge {
	var registered, connected int
	var mem, disk, recl []condition.Gauge
	for _, m := range ms {
		registered++
		if !m.Connected {
			continue
		}
		connected++
		h := m.Host
		if h == nil {
			continue
		}
		g := func(name, help, kind string, v uint64) condition.Gauge {
			return condition.Gauge{Name: name, Help: help, Labels: []condition.Label{{Name: "manager", Value: m.Name}, {Name: "kind", Value: kind}}, Value: float64(v)}
		}
		const (
			memHelp  = "Studio Manager host memory in bytes (kind: used, total)."
			diskHelp = "Studio Manager disk for studios in bytes (kind: used, total)."
			reclHelp = "Studio Manager disk its container runtime could reclaim, in bytes (kind: images, build_cache, volumes)."
		)
		mem = append(mem, g("jam_manager_memory_bytes", memHelp, "used", h.MemUsed), g("jam_manager_memory_bytes", memHelp, "total", h.MemTotal))
		disk = append(disk, g("jam_manager_disk_bytes", diskHelp, "used", h.DiskUsed), g("jam_manager_disk_bytes", diskHelp, "total", h.DiskTotal))
		recl = append(recl,
			g("jam_manager_reclaimable_bytes", reclHelp, "images", h.Reclaimable.Images),
			g("jam_manager_reclaimable_bytes", reclHelp, "build_cache", h.Reclaimable.BuildCache),
			g("jam_manager_reclaimable_bytes", reclHelp, "volumes", h.Reclaimable.Volumes))
	}
	out := []condition.Gauge{
		{Name: "jam_managers", Help: "Studio Managers registered.", Value: float64(registered)},
		{Name: "jam_managers_connected", Help: "Studio Managers connected.", Value: float64(connected)},
	}
	out = append(out, mem...)
	out = append(out, disk...)
	return append(out, recl...)
}
