package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
)

func TestManagerGauges(t *testing.T) {
	host := &jam.HostStats{MemTotal: 8, MemUsed: 3, DiskTotal: 100, DiskUsed: 91, Reclaimable: jam.Reclaimable{Images: 1, BuildCache: 2, Volumes: 4}}
	got := managerGauges([]jam.ManagerStatus{
		{Name: "a", Connected: true, Host: host},
		{Name: "b", Connected: true},              // no host report
		{Name: "c", Connected: false, Host: host}, // stale: not exported
		{Name: "d", Connected: true, Host: host},
	})
	var lines []string
	for _, g := range got {
		var ls []string
		for _, l := range g.Labels {
			ls = append(ls, l.Name+"="+l.Value)
		}
		lines = append(lines, fmt.Sprintf("%s{%s} %g", g.Name, strings.Join(ls, ","), g.Value))
	}
	want := []string{
		"jam_managers{} 4", "jam_managers_connected{} 3",
		"jam_manager_memory_bytes{manager=a,kind=used} 3", "jam_manager_memory_bytes{manager=a,kind=total} 8",
		"jam_manager_memory_bytes{manager=d,kind=used} 3", "jam_manager_memory_bytes{manager=d,kind=total} 8",
		"jam_manager_disk_bytes{manager=a,kind=used} 91", "jam_manager_disk_bytes{manager=a,kind=total} 100",
		"jam_manager_disk_bytes{manager=d,kind=used} 91", "jam_manager_disk_bytes{manager=d,kind=total} 100",
		"jam_manager_reclaimable_bytes{manager=a,kind=images} 1", "jam_manager_reclaimable_bytes{manager=a,kind=build_cache} 2", "jam_manager_reclaimable_bytes{manager=a,kind=volumes} 4",
		"jam_manager_reclaimable_bytes{manager=d,kind=images} 1", "jam_manager_reclaimable_bytes{manager=d,kind=build_cache} 2", "jam_manager_reclaimable_bytes{manager=d,kind=volumes} 4",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("gauges:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}
