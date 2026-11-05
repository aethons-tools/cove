package colima

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/runner"
)

var _ jam.HostReporter = (*Colima)(nil)

// dockerDataDir is where the VM's docker daemon keeps images, containers and
// volumes: the disk that fills.
const dockerDataDir = "/var/lib/docker"

// reclaimEvery is how often HostStats re-runs `docker system df`: it walks
// every image and container layer, too slow to repeat on each sample.
const reclaimEvery = 10 * time.Minute

// reclaimCache is the last good `docker system df` figure and when the next
// is due. A Colima and its WithBuildOutput copies share one.
type reclaimCache struct {
	mu   sync.Mutex
	r    jam.Reclaimable
	next time.Time
}

// HostStats measures the colima VM: memory from /proc/meminfo (used =
// MemTotal - MemAvailable) and docker's disk from df on its data dir (total
// = used + available, so the percentage matches df's Capacity), both over
// one `colima ssh`; and what docker could reclaim from `docker system df`.
//
// The reclaimable figure is best effort: refreshed at most every
// reclaimEvery, and a failed or unreadable `docker system df` keeps the last
// figure (docker fails it while containers write: it lstats a snapshot file
// that just went away) and is retried on the next sample. Only the VM
// measurement can fail HostStats.
func (c *Colima) HostStats(ctx context.Context) (jam.HostStats, error) {
	var h jam.HostStats
	vm, err := c.output(ctx, "colima", "ssh", "--profile", c.profileName(), "--",
		"sh", "-c", "df -kP "+dockerDataDir+"; cat /proc/meminfo")
	if err != nil {
		return h, fmt.Errorf("measure colima VM %s: %w", c.profileName(), err)
	}
	if err := parseVM(vm, &h); err != nil {
		return h, fmt.Errorf("measure colima VM %s: %w", c.profileName(), err)
	}
	h.At = time.Now()
	if c.now != nil {
		h.At = c.now()
	}
	h.Reclaimable = c.reclaimable(ctx, h.At)
	return h, nil
}

// reclaimable is the `docker system df` figure, measured when due.
func (c *Colima) reclaimable(ctx context.Context, now time.Time) jam.Reclaimable {
	rc := c.recl
	if rc == nil {
		rc = &reclaimCache{} // no shared cache: measure every time
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if now.Before(rc.next) {
		return rc.r
	}
	df, err := c.output(ctx, "docker", c.dargs("system", "df", "--format", "{{json .}}")...)
	if err != nil {
		return rc.r
	}
	r, err := parseSystemDF(df)
	if err != nil {
		return rc.r
	}
	rc.r, rc.next = r, now.Add(reclaimEvery)
	return r
}

// output runs a command under ctx with an empty stdin (never the manager's
// terminal: colima ssh would take it) and returns its stdout; a failure
// carries the last line of its stderr.
func (c *Colima) output(ctx context.Context, name string, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	err := runner.Scoped(ctx, c.r, nil).RunIO(strings.NewReader(""), &stdout, &stderr, name, args...)
	out := stdout.String()
	if err != nil {
		if line := lastLine(stderr.String()); line != "" {
			return "", fmt.Errorf("%w: %s", err, line)
		}
		return "", err
	}
	return out, nil
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimSpace(s)
}

// parseVM reads `df -kP <dir>` then /proc/meminfo into h. df's data row is
// the one right after its header: it names the filesystem holding dir, whose
// mount point is often not dir itself (/ when docker's data is not a separate
// disk).
func parseVM(out string, h *jam.HostStats) error {
	var gotDisk, dfRow bool
	meminfo := map[string]uint64{}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		switch {
		case dfRow:
			dfRow = false
			if len(f) < 6 {
				return fmt.Errorf("bad df line %q", sc.Text())
			}
			used, uerr := strconv.ParseUint(f[2], 10, 64)
			avail, aerr := strconv.ParseUint(f[3], 10, 64)
			if uerr != nil || aerr != nil {
				return fmt.Errorf("bad df line %q", sc.Text())
			}
			h.DiskUsed, h.DiskTotal, gotDisk = used*1024, (used+avail)*1024, true
		case len(f) > 0 && f[0] == "Filesystem" && !gotDisk:
			dfRow = true
		case len(f) >= 2 && strings.HasSuffix(f[0], ":"):
			if n, err := strconv.ParseUint(f[1], 10, 64); err == nil {
				meminfo[strings.TrimSuffix(f[0], ":")] = n * 1024 // kB
			}
		}
	}
	if !gotDisk {
		return fmt.Errorf("no df line for %s", dockerDataDir)
	}
	total, ok1 := meminfo["MemTotal"]
	avail, ok2 := meminfo["MemAvailable"]
	if !ok1 || !ok2 || avail > total {
		return errors.New("no MemTotal/MemAvailable in /proc/meminfo")
	}
	h.MemTotal, h.MemUsed = total, total-avail
	return nil
}

// parseSystemDF reads `docker system df --format '{{json .}}'`: one object per
// type, each with a human Reclaimable size.
func parseSystemDF(out string) (jam.Reclaimable, error) {
	var r jam.Reclaimable
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var row struct{ Type, Reclaimable string }
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return r, fmt.Errorf("bad line %q: %w", line, err)
		}
		var dst *uint64
		switch row.Type {
		case "Images":
			dst = &r.Images
		case "Build Cache":
			dst = &r.BuildCache
		case "Local Volumes":
			dst = &r.Volumes
		default:
			continue // Containers: removed with their studios, not reclaimed
		}
		n, err := parseDockerSize(row.Reclaimable)
		if err != nil {
			return r, fmt.Errorf("%s: %w", row.Type, err)
		}
		*dst = n
	}
	return r, nil
}

// dockerUnits are docker's (go-units HumanSize) decimal size suffixes.
var dockerUnits = map[string]float64{"B": 1, "kB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12, "PB": 1e15, "EB": 1e18}

// parseDockerSize parses a docker human size, "1.2GB" or "1.2GB (40%)", into
// bytes.
func parseDockerSize(s string) (uint64, error) {
	s, _, _ = strings.Cut(strings.TrimSpace(s), " ")
	// The unit is the trailing letters; the rest is the number, which docker
	// may print with an exponent (1.162e+08B) and, buggily, negative (counted
	// as 0).
	i := len(s)
	for i > 0 && (s[i-1] >= 'a' && s[i-1] <= 'z' || s[i-1] >= 'A' && s[i-1] <= 'Z') {
		i--
	}
	if i <= 0 {
		return 0, fmt.Errorf("bad size %q", s)
	}
	mult, ok := dockerUnits[s[i:]]
	v, err := strconv.ParseFloat(s[:i], 64)
	if !ok || err != nil {
		return 0, fmt.Errorf("bad size %q", s)
	}
	if v <= 0 {
		return 0, nil
	}
	return uint64(v*mult + 0.5), nil
}
