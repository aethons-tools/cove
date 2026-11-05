package managers

import (
	"encoding/json"
	"fmt"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/condition"
	"github.com/aethons-tools/cove/internal/jam/managers/wire"
	"github.com/aethons-tools/cove/internal/numfmt"
)

// DiskConditionKind is the attention condition for a manager's disk filling
// up: manager.disk:<name>.
const DiskConditionKind = "manager.disk"

// Disk thresholds (percent used): warning at DiskWarnPercent, critical at
// DiskCriticalPercent; resolved below DiskWarnPercent.
const (
	DiskWarnPercent     = 90
	DiskCriticalPercent = 95
)

// DiskConditionKey is name's manager.disk condition key.
func DiskConditionKey(name string) string { return DiskConditionKind + ":" + name }

const diskFix = "free disk in the manager's VM (jam-colima cleans up automatically above its high mark), " +
	"or grow the VM disk (colima stop; colima start --disk <GiB>)"

// report records an OpStatus payload from c, if it is still name's current
// conn, and re-evaluates name's disk condition. A malformed report is logged
// and ignored.
func (h *Hub) report(name string, c *conn, data []byte) {
	var r wire.StatusReport
	if err := json.Unmarshal(data, &r); err != nil {
		h.log.Warn("manager: bad status report ignored", "manager", name, "err", err)
		return
	}
	h.mu.Lock()
	st := h.status[name]
	if h.live[name] != c || st == nil {
		h.mu.Unlock()
		return
	}
	st.Host, st.Activities = r.Host, r.Activities
	// Under h.mu, so a concurrent Revoke's clear cannot be undone. Raise and
	// Clear never block.
	h.diskCondition(name, r.Host)
	h.mu.Unlock()
}

// diskCondition opens, updates or resolves name's disk condition from host;
// with no measurement (nil, or no disk total) it leaves it as it is.
func (h *Hub) diskCondition(name string, host *jam.HostStats) {
	pct, ok := host.DiskPercent()
	if !ok {
		return
	}
	key := DiskConditionKey(name)
	if pct < DiskWarnPercent {
		h.cfg.Conditions.Clear(key)
		return
	}
	sev := condition.Warning
	if pct >= DiskCriticalPercent {
		sev = condition.Critical
	}
	rc := host.Reclaimable
	h.cfg.Conditions.Raise(condition.Condition{
		Key: key, Severity: sev,
		Summary: fmt.Sprintf("manager %s disk %d%% full (%s of %s used)", name, pct, numfmt.Bytes(host.DiskUsed), numfmt.Bytes(host.DiskTotal)),
		Detail: fmt.Sprintf("reclaimable: images %s, build cache %s, volumes %s",
			numfmt.Bytes(rc.Images), numfmt.Bytes(rc.BuildCache), numfmt.Bytes(rc.Volumes)),
		Fix: diskFix,
	})
}

// clearUnknownDiskConditions resolves disk conditions naming a manager the
// hub has not registered.
func (h *Hub) clearUnknownDiskConditions() {
	for _, key := range h.cfg.Conditions.OpenKeys(DiskConditionKind) {
		name := key[len(DiskConditionKind)+1:]
		h.mu.Lock()
		_, known := h.remotes[name]
		h.mu.Unlock()
		if !known {
			h.cfg.Conditions.Clear(key)
		}
	}
}
