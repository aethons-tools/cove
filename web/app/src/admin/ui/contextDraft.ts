import type { ContextPanel, ContextSave } from "../api/types";

// The Agent context card's editing model: the draft, its byte counts and its
// problems, mirroring the server's rules (internal/jam/sessionctx/authored.go,
// internal/jam/contextyaml.go) so a problem shows before saving. Problems are
// keyed by the admin API's field paths (core, leaves[2].readWhen,
// resources[0].kind, or a list: leaves, resources), so the server's field
// errors land on the same fields.

export type LeafDraft = { key: number; name: string; readWhen: string; body: string; open: boolean };
export type ResourceRow = { name: string; kind: string; ref: string; note: string };
export type Draft = { core: string; leaves: LeafDraft[]; resources: ResourceRow[] };
export type Problems = Record<string, string>;

export const KINDS = ["repo", "doc", "tracker", "url"] as const;
const RESOURCES_LEAF = "resources.md";
const LEAF_NAME = /^[a-z0-9][a-z0-9._-]*\.md$/;

const enc = new TextEncoder();
// bytes is s's UTF-8 length, as the server counts it.
export const bytes = (s: string) => enc.encode(s).length;

let nextKey = 0;
export const leafKey = () => ++nextKey;

export function draftOf(p: ContextPanel): Draft {
  return {
    core: p.core,
    leaves: p.leaves.map((l) => ({ key: leafKey(), name: l.name, readWhen: l.readWhen, body: l.body, open: false })),
    resources: p.showResources ? p.resources.map((r) => ({ name: r.name, kind: r.kind, ref: r.ref, note: r.note })) : [],
  };
}

// coreBytes is what counts against the core budget: the trimmed core plus,
// with resources, the pointer line the server adds (sessionctx.ProjectLayer).
export function coreBytes(core: string, resources: number): number {
  const c = resources > 0 ? `${core}\n${resources} project resource(s) (repos, docs, trackers) are listed in project/resources.md.` : core;
  return bytes(c.trim());
}

export const leafBytes = (d: Draft) => d.leaves.reduce((n, l) => n + bytes(l.body), 0);

// isEmpty is a draft that sets nothing (saving it clears the context).
export const isEmpty = (d: Draft) => d.core.trim() === "" && d.leaves.length === 0 && d.resources.length === 0;

// problemsOf is every problem the card can see before saving.
export function problemsOf(d: Draft, p: ContextPanel): Problems {
  const out: Problems = {};
  const lim = p.limits;
  if (coreBytes(d.core, d.resources.length) > p.budget) out.core = "Over budget: move detail into progressive instructions";
  const seen = new Set<string>();
  d.leaves.forEach((l, i) => {
    const at = `leaves[${i}].`;
    if (!LEAF_NAME.test(l.name)) out[at + "name"] = "Lowercase a–z, 0–9, . _ or -, ending .md";
    else if (seen.has(l.name)) out[at + "name"] = "Name already used";
    else if (p.showResources && l.name === RESOURCES_LEAF) out[at + "name"] = "Reserved for the resources list";
    seen.add(l.name);
    if (l.readWhen.trim() === "") out[at + "readWhen"] = "Required";
    else if (bytes(l.readWhen) > lim.maxReadWhen) out[at + "readWhen"] = `Over ${lim.maxReadWhen} bytes`;
  });
  if (d.leaves.length > lim.maxLeaves) out.leaves = `At most ${lim.maxLeaves}`;
  else if (leafBytes(d) > lim.maxLeafBytes) out.leaves = `Over ${lim.maxLeafBytes / 1024} KiB`;
  if (p.showResources) {
    const names = new Set<string>();
    d.resources.forEach((r, i) => {
      const at = `resources[${i}].`;
      if (r.name.trim() === "") out[at + "name"] = "Required";
      else if (names.has(r.name)) out[at + "name"] = "Name already used";
      names.add(r.name);
      if (!(KINDS as readonly string[]).includes(r.kind)) out[at + "kind"] = "Choose a kind";
      if (r.ref.trim() === "") out[at + "ref"] = "Required";
    });
    if (d.resources.length > lim.maxResources) out.resources = `At most ${lim.maxResources}`;
  }
  return out;
}

// saveOf is the structured save: the whole context, every part sent (a save
// replaces it all).
export function saveOf(d: Draft, showResources: boolean): ContextSave {
  const s: ContextSave = { core: d.core, leaves: d.leaves.map((l) => ({ name: l.name, readWhen: l.readWhen, body: l.body })) };
  if (showResources) s.resources = d.resources.map((r) => ({ ...r }));
  return s;
}

// dropProblems is ps without field (and, for a list, its items' fields).
export function dropProblems(ps: Problems, field: string): Problems {
  const out: Problems = {};
  for (const [k, v] of Object.entries(ps)) if (k !== field && !k.startsWith(field + "[")) out[k] = v;
  return out;
}

// kib is n bytes as KiB to one decimal, rounded up (so any overflow shows).
export const kib = (n: number) => Math.ceil(n / 102.4) / 10;
