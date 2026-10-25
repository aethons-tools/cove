package jam

import (
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aethons-tools/cove/internal/harnessinstall"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
	"github.com/aethons-tools/cove/internal/jam/snippet"
	"github.com/aethons-tools/cove/internal/studio"
)

// RecordSetupPart records that part was delivered to actorID — only when its
// fingerprint differs from the stored one, so a cove refreshing every wake
// neither builds the body nor writes. body renders the already-masked text
// and is called only on a change. A failure is logged and never returned: a
// snapshot must not fail a delivery.
func RecordSetupPart(store Store, log *slog.Logger, actorID, part, fingerprint string, body func() string, at time.Time) {
	for _, cur := range store.SetupSnapshots(actorID) {
		if cur.Part == part && cur.Fingerprint == fingerprint {
			return
		}
	}
	err := store.PutSetupSnapshot(SetupSnapshot{ActorID: actorID, Part: part, At: at.UTC(), Fingerprint: fingerprint, Body: capSetupBody(body())})
	if err != nil && log != nil {
		log.Warn("setup snapshot write failed", "actor", actorID, "part", part, "err", err.Error())
	}
}

// capSetupBody truncates s to MaxSetupBody bytes (at a rune boundary) with a
// marker naming the full size.
func capSetupBody(s string) string {
	if len(s) <= MaxSetupBody {
		return s
	}
	marker := fmt.Sprintf("\n… truncated: %d bytes in all (cap %d)\n", len(s), MaxSetupBody)
	cut := s[:MaxSetupBody-len(marker)]
	for !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + marker
}

// contextSnapshotBody is the context part's body: contextBody of the bundle
// redacted for display (redactContextBundle). The delivered bundle itself is
// unchanged.
func contextSnapshotBody(b sessionctx.Bundle) string {
	return contextBody(redactContextBundle(b))
}

// redactContextBundle is a copy of b with every build-arg row of the kit's
// tools.md passed through RedactEnv under its tool name (a secret-named build
// arg reads «masked»), then every line of the core, the files and the
// warnings passed through redactProse (token shapes, secret assignments, URL
// userinfo, queries and fragments). File sizes in the rendered body are then
// the shown sizes.
func redactContextBundle(b sessionctx.Bundle) sessionctx.Bundle {
	out := b
	out.Core = redactProseLines(b.Core)
	out.Files = make(map[string]string, len(b.Files))
	maps.Copy(out.Files, b.Files)
	for p, body := range out.Files {
		if p == sessionctx.LayerKit+"/"+sessionctx.ToolsLeaf {
			body = redactToolsRows(body)
		}
		out.Files[p] = redactProseLines(body)
	}
	out.Warnings = make([]string, len(b.Warnings))
	for i, w := range b.Warnings {
		out.Warnings[i] = redactProseLines(w)
	}
	return out
}

// redactToolsRows masks each "| tool | value |" row's value through
// RedactEnv(tool, value) (KitLayer's table; the header row is unchanged).
func redactToolsRows(body string) string {
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		inner, ok := strings.CutPrefix(l, "| ")
		if !ok {
			continue
		}
		if inner, ok = strings.CutSuffix(inner, " |"); !ok {
			continue
		}
		tool, value, ok := strings.Cut(inner, " | ")
		if !ok {
			continue
		}
		lines[i] = "| " + tool + " | " + RedactEnv(tool, value) + " |"
	}
	return strings.Join(lines, "\n")
}

// instanceKit is the merged kit an instance's image was built from: its
// recorded chain at those exact versions, else its kit ref. ok=false without
// a kit or when the registry no longer has it.
func instanceKit(store Store, inst Instance) (studio.StudioKit, bool) {
	if inst.KitDraft != nil {
		sk, err := draftStudioKit(store, inst)
		return sk, err == nil
	}
	if len(inst.KitChain) > 0 {
		sk, err := recordedChainKit(store, inst.KitChain)
		return sk, err == nil
	}
	if inst.Kit.ID == "" {
		return studio.StudioKit{}, false
	}
	def, ok, err := ResolveKitDefinition(store, inst.Kit, harnessinstall.Default())
	return def.Kit, ok && err == nil
}

// recordRaiseSetup records the four parts a raise delivered.
func (s *Supervisor) recordRaiseSetup(inst Instance, spec RaiseSpec, def *KitDefinition) {
	now := s.now()
	if spec.Context != nil {
		b := *spec.Context
		RecordSetupPart(s.store, s.log, inst.ActorID, SetupPartContext, b.Fingerprint, func() string { return contextSnapshotBody(b) }, now)
	}
	RecordSetupPart(s.store, s.log, inst.ActorID, SetupPartEgress, EgressFingerprint(spec.Egress), func() string {
		var ceiling []string
		known := def != nil && def.Ref == spec.Kit
		if known {
			ceiling, _ = studio.Ceiling(def.Kit.Egress)
		}
		return egressBody(spec.Egress, ceiling, known)
	}, now)
	if spec.Connector != nil {
		c := *spec.Connector
		RecordSetupPart(s.store, s.log, inst.ActorID, SetupPartConnector, snippet.Fingerprint(c), func() string { return connectorBody(c) }, now)
	}
	RecordSetupPart(s.store, s.log, inst.ActorID, SetupPartKit, kitFingerprint(inst.Kit, inst.ImageTag), func() string {
		return kitRefsBody(inst.Kit, inst.KitChain, inst.ImageTag)
	}, now)
}

// recordEgress records a landed egress apply of p to inst.
func (s *Supervisor) recordEgress(inst Instance, p *EgressPolicy) {
	RecordSetupPart(s.store, s.log, inst.ActorID, SetupPartEgress, EgressFingerprint(p), func() string {
		sk, known := instanceKit(s.store, inst)
		var ceiling []string
		if known {
			ceiling, _ = studio.Ceiling(sk.Egress)
		}
		return egressBody(p, ceiling, known)
	}, s.now())
}
