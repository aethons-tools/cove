package jam

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/aethons-tools/cove/internal/jam/linediff"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
	"github.com/aethons-tools/cove/internal/jam/snippet"
	"github.com/aethons-tools/cove/internal/studio"
)

// SetupDeps are what a setup render reads beyond the store.
type SetupDeps struct {
	Store        Store
	Sup          *Supervisor      // nil: context and kit resolution unavailable
	Images       ImageFactsReader // nil: image parts unavailable (placeholder launcher)
	SealedEgress []string         // the hardening layer's always-on egress list
}

// Fixed section reasons: never an error text from outside, never repo text.
const (
	reasonNoSnapshot        = "no snapshot (studio raised before setup recording); recomputed"
	reasonNoSupervisor      = "no supervisor on this Jam"
	reasonNoLauncher        = "no image reader (placeholder launcher)"
	reasonImageUnknown      = "image tag unknown (raised before it was recorded, or the launcher cannot name images)"
	reasonLauncherUntagged  = "the launcher cannot name images"
	reasonNoKit             = "the role raises no kit (the launcher's static image)"
	reasonKitUnresolvable   = "the role's kit does not resolve; a raise would fail (see at-jam kit show)"
	reasonModelSpec         = "the role's model-spec does not resolve; a raise would fail"
	reasonConnectorConflict = "the role's destinations conflict; a raise would fail"
	reasonRoleGone          = "the role no longer exists"
)

// Image-read reasons are the fixed error values' text.
var (
	reasonNoImage        = ErrImageNotPresent.Error()
	reasonImageError     = ErrImageUnreadable.Error()
	reasonSeedUnreadable = "seed unreadable: " + ErrImageUnreadable.Error()
)

// wouldBeActorID names the synthetic actor of a role render.
const wouldBeActorID = "would-be"

// wouldBe is what a raise of a role would deliver now.
type wouldBe struct {
	actor     Actor
	egress    *EgressPolicy
	def       KitDefinition
	haveKit   bool
	kitErr    error
	ceiling   []string
	ceilingOK bool
	bundle    sessionctx.Bundle
	imageTag  string
}

var errNoSupervisor = errors.New(reasonNoSupervisor)

// wouldBeRaise resolves a raise of role in project the way Raise does: the
// actor Enroll would make (one grant), the role's egress, its kit under its
// model-spec harness (raiseKit), and the session context compileContext
// would build for an ephemeral session with no unit and no owner.
func (s *Supervisor) wouldBeRaise(project string, role Role) (wouldBe, error) {
	if s == nil {
		return wouldBe{}, errNoSupervisor
	}
	pid := orDefaultProject(project)
	if id, ok := ProjectIDOf(s.store, pid); ok {
		pid = string(id)
	}
	wb := wouldBe{actor: Actor{ID: wouldBeActorID, Grants: []Grant{{Project: pid, Role: role.Name}}}}
	if role.Scope.Egress != nil {
		wb.egress = &EgressPolicy{Domains: slices.Clone(role.Scope.Egress.Domains)}
	}
	spec := RaiseSpec{ActorID: wouldBeActorID, Project: pid, Role: role.Name, Egress: wb.egress}
	var def *KitDefinition
	if d, have, err := s.raiseKit(pid, role); err != nil {
		wb.kitErr = err
	} else if have {
		wb.def, wb.haveKit, def = d, true, &d
		spec.Kit = d.Ref
		wb.ceiling, _ = studio.Ceiling(d.Kit.Egress)
		wb.ceilingOK = true
		wb.imageTag = s.imageTag(d.Ref)
	}
	wb.bundle = s.compileContext(spec, wb.actor, spec.Kit, def, nil)
	return wb, nil
}

// imageMemo reads an image at most once per render.
type imageMemo struct {
	done   bool
	facts  ImageFacts
	reason string
}

// readImage returns tag's facts, or the fixed reason they are unavailable;
// noKit, when set, is the reason (there is no image to read). The facts are
// shared with the reader's cache: callers only read them.
func (d SetupDeps) readImage(ctx context.Context, m *imageMemo, tag, noKit string) (ImageFacts, string) {
	if m.done {
		return m.facts, m.reason
	}
	m.done = true
	switch {
	case noKit != "":
		m.reason = noKit
	case d.Images == nil:
		m.reason = reasonNoLauncher
	case tag == "":
		m.reason = reasonImageUnknown
	default:
		f, err := d.Images.ImageFacts(ctx, tag)
		switch {
		case errors.Is(err, ErrImageNotPresent):
			m.reason = reasonNoImage
		case err != nil:
			m.reason = reasonImageError
		default:
			m.facts = f
		}
	}
	return m.facts, m.reason
}

func (d SetupDeps) imageEnvPart(ctx context.Context, m *imageMemo, tag, noKit string) string {
	f, reason := d.readImage(ctx, m, tag, noKit)
	if reason != "" {
		return "image env: unavailable (" + reason + ")\n"
	}
	return imageEnvBody(f)
}

func (d SetupDeps) imageLabelsPart(ctx context.Context, m *imageMemo, tag, noKit string) string {
	f, reason := d.readImage(ctx, m, tag, noKit)
	if reason != "" {
		return "image: unavailable (" + reason + ")\n"
	}
	return imageLabelsBody(f)
}

func (d SetupDeps) seedSection(ctx context.Context, m *imageMemo, tag, noKit string) SetupSection {
	f, reason := d.readImage(ctx, m, tag, noKit)
	switch {
	case reason != "":
		return unavailable(SectionSeed, reason)
	case f.SeedErr:
		return unavailable(SectionSeed, reasonSeedUnreadable)
	}
	return SetupSection{Name: SectionSeed, Source: SourceImage, Body: seedBody(f)}
}

// RoleSetup renders what a studio of project/role raised now would get.
// sections is validated (empty = all; an unknown name is ErrUnknownSection).
// ErrNotFound for an unknown role; every other failure is a section's.
func RoleSetup(ctx context.Context, d SetupDeps, project, roleName string, sections []string) (StudioSetup, error) {
	sections, err := setupSections(sections)
	if err != nil {
		return StudioSetup{}, err
	}
	r, ok := d.Store.GetRole(project, roleName)
	if !ok {
		return StudioSetup{}, fmt.Errorf("role %s/%s: %w", orDefaultProject(project), roleName, ErrNotFound)
	}
	out := StudioSetup{Subject: "role " + ProjectName(d.Store, orDefaultProject(project)) + "/" + r.Name}
	wb, wbErr := d.Sup.wouldBeRaise(project, r)
	actor := wb.actor
	if wbErr != nil {
		pid := orDefaultProject(project)
		if id, ok := ProjectIDOf(d.Store, pid); ok {
			pid = string(id)
		}
		actor = Actor{ID: wouldBeActorID, Grants: []Grant{{Project: pid, Role: r.Name}}}
	}
	noKit := ""
	switch {
	case wbErr != nil:
		noKit = reasonNoSupervisor
	case wb.kitErr != nil:
		noKit = reasonKitUnresolvable
	case !wb.haveKit:
		noKit = reasonNoKit
	case wb.imageTag == "":
		noKit = reasonLauncherUntagged // a role render has no recorded tag to be missing
	}
	var img imageMemo
	for _, name := range sections {
		sec := SetupSection{Name: name, Source: SourceConfig}
		switch name {
		case SectionIdentity:
			sec.Body = roleIdentityBody(d.Store, project, r.Name)
		case SectionContext:
			if wbErr != nil {
				sec = unavailable(name, reasonNoSupervisor)
			} else {
				sec.Body = contextSnapshotBody(wb.bundle) // redacted exactly as a studio's snapshot
			}
		case SectionEgress:
			egress := wb.egress
			if wbErr != nil && r.Scope.Egress != nil {
				egress = &EgressPolicy{Domains: slices.Clone(r.Scope.Egress.Domains)}
			}
			sec.Body = egressBody(egress, wb.ceiling, wb.ceilingOK) + sealedEgressBody(d.SealedEgress)
		case SectionDestinations:
			sec.Body = destinationsBody(d.Store, actor)
		case SectionEnv:
			if conn, err := ConnectorFor(d.Store, actor); err != nil {
				sec = unavailable(name, reasonConnectorConflict)
			} else {
				sec.Body = connectorBody(conn) + "\n"
			}
			sec.Body += d.imageEnvPart(ctx, &img, wb.imageTag, noKit)
		case SectionModel:
			if ms, err := ModelSpecFor(d.Store, actor); err != nil {
				sec = unavailable(name, reasonModelSpec)
			} else {
				sec.Body = modelBody(ms)
			}
		case SectionKit:
			switch {
			case wbErr != nil:
				sec = unavailable(name, reasonNoSupervisor)
			case wb.kitErr != nil:
				sec = unavailable(name, reasonKitUnresolvable)
			case !wb.haveKit:
				sec.Body = kitRefsBody(KitRef{}, nil, "") + d.imageLabelsPart(ctx, &img, wb.imageTag, noKit)
			default:
				chain := chainOrSelf(chainRefs(wb.def.Chain), wb.def.Ref)
				sec.Body = kitRefsBody(wb.def.Ref, chainRefs(wb.def.Chain), wb.imageTag) + kitDetailBody(d.Store, chain, wb.def.Kit.BuildArgs, nil) +
					d.imageLabelsPart(ctx, &img, wb.imageTag, noKit)
			}
		case SectionSeed:
			sec = d.seedSection(ctx, &img, wb.imageTag, noKit)
		case SectionMCP:
			sec.Body = mcpBody(wb.def.Kit.MCPServers, wb.haveKit)
		case SectionPolicy:
			sec.Body = rolePolicyBody(d.Store, project, r)
		}
		out.Sections = append(out.Sections, sec)
	}
	return out, nil
}

// InstanceSetup renders what studio ref (a session id or a live manual
// label) got: its snapshots, else a labelled recompute; config sections as
// of now; image facts for the image it runs. diffRole adds, per section, a
// line diff against RoleSetup of its role. sections is validated as
// RoleSetup's. ErrNotFound for an unknown or gone studio.
func InstanceSetup(ctx context.Context, d SetupDeps, ref string, sections []string, diffRole bool) (StudioSetup, error) {
	sections, err := setupSections(sections)
	if err != nil {
		return StudioSetup{}, err
	}
	id := ResolveSession(d.Store, ref)
	inst, ok := d.Store.GetInstance(id)
	if !ok || inst.Phase == PhaseGone {
		return StudioSetup{}, fmt.Errorf("studio %s: %w", ref, ErrNotFound)
	}
	snaps := map[string]SetupSnapshot{}
	for _, s := range d.Store.SetupSnapshots(id) {
		snaps[s.Part] = s
	}
	actor := actorOf(d.Store, inst)
	role, roleOK := d.Store.GetRole(inst.Project, inst.Role)
	noKit := ""
	if inst.Kit.ID == "" && inst.ImageTag == "" {
		noKit = reasonNoKit
		if len(snaps) == 0 {
			noKit = "" // raised before kits were recorded: the tag is simply unknown
		}
	}
	out := StudioSetup{Subject: "studio " + id}
	var img imageMemo
	for _, name := range sections {
		var sec SetupSection
		switch name {
		case SectionIdentity:
			sec = SetupSection{Name: name, Source: SourceConfig, Body: instanceIdentityBody(d.Store, inst)}
		case SectionContext:
			sec = d.instanceContext(actor, snaps)
		case SectionEgress:
			sec = d.instanceEgress(inst, role, roleOK, snaps)
		case SectionDestinations:
			sec = SetupSection{Name: name, Source: SourceConfig, Body: destinationsBody(d.Store, actor), Stale: connectorStale(d.Store, actor, inst.Connector)}
		case SectionEnv:
			sec = d.instanceEnv(inst, actor, snaps)
			sec.Body += d.imageEnvPart(ctx, &img, instanceImage(inst), noKit)
		case SectionModel:
			if ms, err := ModelSpecFor(d.Store, actor); err != nil {
				sec = unavailable(name, reasonModelSpec)
			} else {
				sec = SetupSection{Name: name, Source: SourceConfig, Body: modelBody(ms)}
			}
		case SectionKit:
			sec = d.instanceKitSection(ctx, &img, inst, snaps, noKit)
		case SectionSeed:
			sec = d.seedSection(ctx, &img, instanceImage(inst), noKit)
		case SectionMCP:
			sk, known := instanceKit(d.Store, inst)
			sec = SetupSection{Name: name, Source: SourceConfig, Body: mcpBody(sk.MCPServers, known)}
		case SectionPolicy:
			if roleOK {
				sec = SetupSection{Name: name, Source: SourceConfig, Body: rolePolicyBody(d.Store, inst.Project, role) + instancePolicyBody(inst)}
			} else {
				sec = SetupSection{Name: name, Source: SourceUnavailable, Reason: reasonRoleGone, Body: instancePolicyBody(inst)}
			}
		}
		out.Sections = append(out.Sections, sec)
	}
	if !diffRole {
		return out, nil
	}
	if !roleOK {
		out.Note = reasonRoleGone + ": no diff"
		return out, nil
	}
	rs, err := RoleSetup(ctx, d, inst.Project, inst.Role, sections)
	if err != nil {
		out.Note = "the role could not be rendered: no diff"
		return out, nil
	}
	// The role side is the true raise-now baseline: a fresh studio has no
	// idle override and no alarms.
	for i := range rs.Sections {
		if sec := &rs.Sections[i]; sec.Name == SectionPolicy && sec.Source != SourceUnavailable {
			sec.Body += instancePolicyBody(Instance{})
		}
	}
	applyDiff(&out, rs)
	return out, nil
}

// instanceContext is the context snapshot (stale when a refresh now differs),
// else a recompute redacted exactly like a snapshot (contextSnapshotBody).
func (d SetupDeps) instanceContext(actor Actor, snaps map[string]SetupSnapshot) SetupSection {
	var cur sessionctx.Bundle
	curOK := false
	if d.Sup != nil {
		if b, err := d.Sup.ContextForActor(actor); err == nil {
			cur, curOK = b, true
		}
	}
	if s, ok := snaps[SetupPartContext]; ok {
		return SetupSection{Name: SectionContext, Source: SourceSnapshot, At: s.At, Body: s.Body, Stale: curOK && cur.Fingerprint != s.Fingerprint}
	}
	if !curOK {
		return unavailable(SectionContext, reasonNoSupervisor)
	}
	return SetupSection{Name: SectionContext, Source: SourceRecomputed, Reason: reasonNoSnapshot, Body: contextSnapshotBody(cur)}
}

func (d SetupDeps) instanceEgress(inst Instance, role Role, roleOK bool, snaps map[string]SetupSnapshot) SetupSection {
	var want *EgressPolicy
	if roleOK && role.Scope.Egress != nil {
		want = &EgressPolicy{Domains: slices.Clone(role.Scope.Egress.Domains)}
	}
	sec := SetupSection{Name: SectionEgress}
	if s, ok := snaps[SetupPartEgress]; ok {
		sec.Source, sec.At, sec.Body = SourceSnapshot, s.At, s.Body
		sec.Stale = roleOK && EgressFingerprint(want) != s.Fingerprint
		if inst.Egress != "" && inst.Egress != s.Fingerprint {
			sec.Body += "note: the supervisor's recorded policy differs from this snapshot (a re-apply is pending)\n"
		}
	} else {
		sk, known := instanceKit(d.Store, inst)
		var ceiling []string
		if known {
			ceiling, _ = studio.Ceiling(sk.Egress)
		}
		sec.Source, sec.Reason, sec.Body = SourceRecomputed, reasonNoSnapshot, egressBody(want, ceiling, known)
	}
	sec.Body += sealedEgressBody(d.SealedEgress)
	return sec
}

func (d SetupDeps) instanceEnv(inst Instance, actor Actor, snaps map[string]SetupSnapshot) SetupSection {
	cur, curErr := ConnectorFor(d.Store, actor)
	sec := SetupSection{Name: SectionEnv}
	switch s, ok := snaps[SetupPartConnector]; {
	case ok:
		sec.Source, sec.At, sec.Body = SourceSnapshot, s.At, s.Body
		sec.Stale = curErr == nil && snippet.Fingerprint(cur) != s.Fingerprint
		if inst.Connector != "" && inst.Connector != s.Fingerprint {
			sec.Body += "note: the studio last reported applying a different connector\n"
		}
	case curErr != nil:
		sec.Source, sec.Reason = SourceUnavailable, reasonConnectorConflict
	default:
		sec.Source, sec.Reason, sec.Body = SourceRecomputed, reasonNoSnapshot, connectorBody(cur)
	}
	sec.Body += "\n"
	return sec
}

func (d SetupDeps) instanceKitSection(ctx context.Context, m *imageMemo, inst Instance, snaps map[string]SetupSnapshot, noKit string) SetupSection {
	sec := SetupSection{Name: SectionKit, Source: SourceRecomputed, Reason: reasonNoSnapshot, Body: instanceKitRefsBody(inst)}
	if inst.KitDraft != nil {
		sec.Title = SectionKit + " (draft @" + inst.KitDraft.Tree[:min(7, len(inst.KitDraft.Tree))] + ")"
	}
	if s, ok := snaps[SetupPartKit]; ok {
		sec.Source, sec.Reason, sec.At, sec.Body = SourceSnapshot, "", s.At, s.Body
	}
	sec.Stale = d.Sup != nil && imageStatus(d.Sup, map[[3]string]currentImage{}, inst) == "stale"
	if inst.Kit.ID != "" {
		sk, _ := instanceKit(d.Store, inst)
		sec.Body += kitDetailBody(d.Store, chainOrSelf(inst.KitChain, inst.Kit), sk.BuildArgs, inst.KitDraft)
	}
	sec.Body += d.imageLabelsPart(ctx, m, instanceImage(inst), noKit)
	return sec
}

// instanceImage is the image an instance's image facts are read from: a
// draft studio's raised image id (its tag moves with later builds), else
// its tag. The tag stays what the kit section shows.
func instanceImage(inst Instance) string {
	if inst.KitDraft != nil && inst.KitDraftImage != "" {
		return inst.KitDraftImage
	}
	return inst.ImageTag
}

// actorOf is the instance's identity, or (revoked) the one grant it was raised with.
func actorOf(store Store, inst Instance) Actor {
	for _, a := range store.ListActors() {
		if a.ID == inst.ActorID {
			return a
		}
	}
	return Actor{ID: inst.ActorID, Grants: []Grant{{Project: inst.Project, Role: inst.Role}}}
}

// connectorStale: the connector the cove reported applying is not its current one.
func connectorStale(store Store, actor Actor, reported string) bool {
	want, err := ConnectorFor(store, actor)
	return err == nil && reported != "" && snippet.Fingerprint(want) != reported
}

// applyDiff sets each section's diff against the role render's same section
// (this studio −, a raise now +); identity is never diffed.
func applyDiff(out *StudioSetup, role StudioSetup) {
	byName := map[string]SetupSection{}
	for _, s := range role.Sections {
		byName[s.Name] = s
	}
	for i := range out.Sections {
		s := &out.Sections[i]
		r, ok := byName[s.Name]
		if !ok || s.Name == SectionIdentity {
			continue
		}
		s.Diff, s.DiffTooLarge = linediff.Diff(s.Body, r.Body)
		s.Diffed = true
	}
}
