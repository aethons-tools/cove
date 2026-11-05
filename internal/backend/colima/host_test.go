package colima

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/runner"
)

const vmOut = `Filesystem     1024-blocks     Used Available Capacity Mounted on
/dev/vdb1        102687672 92418904  5000000      95% /var/lib/docker
MemTotal:        8131360 kB
MemFree:          401234 kB
MemAvailable:    5131360 kB
Buffers:           12345 kB
`

const dfOut = `{"Active":"3","Reclaimable":"1.2GB (40%)","Size":"3.1GB","TotalCount":"5","Type":"Images"}
{"Active":"0","Reclaimable":"0B","Size":"0B","TotalCount":"0","Type":"Containers"}
{"Active":"1","Reclaimable":"12.3MB (50%)","Size":"24.6MB","TotalCount":"2","Type":"Local Volumes"}
{"Active":"0","Reclaimable":"512kB","Size":"512kB","TotalCount":"10","Type":"Build Cache"}
`

func TestHostStats(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: vmOut}, {Stdout: dfOut}}}
	c := NewForProfile(f, "jam-b")
	c.now = func() time.Time { return time.Unix(42, 0) }
	got, err := c.HostStats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := jam.HostStats{
		At:       time.Unix(42, 0),
		MemTotal: 8131360 * 1024, MemUsed: (8131360 - 5131360) * 1024,
		// Total is what docker can use (used + available), as df's Capacity.
		DiskTotal: (92418904 + 5000000) * 1024, DiskUsed: 92418904 * 1024,
		Reclaimable: jam.Reclaimable{Images: 1_200_000_000, Volumes: 12_300_000, BuildCache: 512_000},
	}
	if got != want {
		t.Fatalf("HostStats =\n %+v\nwant\n %+v", got, want)
	}
	if len(f.Calls) != 2 {
		t.Fatalf("calls = %+v", f.Calls)
	}
	if a := strings.Join(append([]string{f.Calls[0].Name}, f.Calls[0].Args...), " "); a != "colima ssh --profile jam-b -- sh -c df -kP /var/lib/docker; cat /proc/meminfo" {
		t.Errorf("vm call = %q", a)
	}
	if a := strings.Join(append([]string{f.Calls[1].Name}, f.Calls[1].Args...), " "); a != "docker --context colima-jam-b system df --format {{json .}}" {
		t.Errorf("docker call = %q", a)
	}
}

// Without a profile the VM is the context's: colima-<p> is p, colima default.
// df -kP names the filesystem holding /var/lib/docker, whatever its mount
// point: often / when docker's data is not a separate disk.
func TestParseVMTakesDFRowWhateverTheMount(t *testing.T) {
	for _, mount := range []string{"/", "/var/lib/docker", "/mnt/my data"} {
		out := "Filesystem     1024-blocks     Used Available Capacity Mounted on\n" +
			"/dev/vda1        100 60 40      60% " + mount + "\n" +
			"MemTotal: 10 kB\nMemAvailable: 4 kB\n"
		var h jam.HostStats
		if err := parseVM(out, &h); err != nil {
			t.Fatalf("mount %q: %v", mount, err)
		}
		if h.DiskUsed != 60*1024 || h.DiskTotal != 100*1024 || h.MemUsed != 6*1024 {
			t.Errorf("mount %q: %+v", mount, h)
		}
	}
}

func TestHostStatsProfileFromContext(t *testing.T) {
	for ctx, want := range map[string]string{"": "default", "colima": "default", "colima-jam-b": "jam-b"} {
		if got := NewWithContext(&runner.Fake{}, ctx).profileName(); got != want {
			t.Errorf("context %q: profile %q, want %q", ctx, got, want)
		}
	}
	if got := NewForProfile(&runner.Fake{}, "default").contextName(); got != "colima" {
		t.Errorf("default profile context = %q", got)
	}
}

func TestHostStatsErrors(t *testing.T) {
	for name, outs := range map[string][]runner.FakeResult{
		"ssh fails":       {{Err: errors.New("exit 1")}},
		"no df line":      {{Stdout: "MemTotal: 1 kB\nMemAvailable: 1 kB\n"}},
		"df header only":  {{Stdout: "Filesystem 1024-blocks Used Available Capacity Mounted on\nMemTotal: 1 kB\nMemAvailable: 1 kB\n"}},
		"bad df row":      {{Stdout: "Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/vdb1 10 x 5 50% /\nMemTotal: 1 kB\nMemAvailable: 1 kB\n"}},
		"no meminfo":      {{Stdout: "Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/vdb1 10 5 5 50% /var/lib/docker\n"}},
		"docker df fails": {{Stdout: vmOut}, {Err: errors.New("exit 1")}},
		"bad size":        {{Stdout: vmOut}, {Stdout: `{"Type":"Images","Reclaimable":"lots"}` + "\n"}},
	} {
		f := &runner.Fake{Outputs: outs}
		if _, err := NewForProfile(f, "p").HostStats(context.Background()); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestParseDockerSize(t *testing.T) {
	for in, want := range map[string]uint64{
		"0B": 0, "512B": 512, "512kB": 512_000, "1.2GB (40%)": 1_200_000_000, "12.35MB": 12_350_000, "2TB": 2_000_000_000_000, "1.5PB": 1_500_000_000_000_000,
	} {
		got, err := parseDockerSize(in)
		if err != nil || got != want {
			t.Errorf("parseDockerSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "GB", "1.2XB", "-1B"} {
		if _, err := parseDockerSize(bad); err == nil {
			t.Errorf("parseDockerSize(%q) accepted", bad)
		}
	}
}
