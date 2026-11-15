import type { DestDetail, DestEndpoint, DestFields, DestInboundSpec, DestOutboundSpec } from "../api/types";
import type { Problems } from "../ui/contextDraft";

// The Destination card's editing model: the draft, its problems, mirroring
// the server's rules (internal/jam/destedit.go, headerspec.go, policy.go) so
// a problem shows before saving, and the save body. Problems are keyed by the
// admin API's field paths (credName, env, endpoints[1].route,
// endpoints[0].applySpec.template), so the server's field errors land on the
// same fields.

// EXPRESSION is how the card names the API's custom method.
export const EXPRESSION = "expression";
export const MAX_ENDPOINTS = 10;
export const MAX_NOTE = 300;
export const LOG_IN = ["none", "examined", "all"] as const;
export const LOG_OUT = ["none", "changed", "all"] as const;
export const ENCODINGS = ["raw", "basic"] as const;

export type InDraft = { header: string; prefixes: string; encoding: string };
export type OutDraft = { header: string; template: string; encoding: string; basicUser: string };

// EndpointRow is one endpoint as the grid edits it: its columns as text
// (allowPaths space-separated, git yes or no), the rest beside them.
export type EndpointRow = {
  name: string; subdomain: string; route: string; upstream: string; identityIn: string; apply: string; allowPaths: string; git: string;
  inSpec: InDraft | null; outSpec: OutDraft | null; logHeadersIn: string; logHeadersOut: string; open: boolean;
};
export type EnvDraft = { key: string; template: string };
export type Draft = { credName: string; note: string; endpoints: EndpointRow[]; env: EnvDraft[] };

// The presets' expansions (headerspec.go's inboundPresets / outboundPresets),
// shown read-only in an expanded row.
const IN_PRESETS: Record<string, InDraft> = {
  bearer: { header: "Authorization", prefixes: "Bearer , token ", encoding: "raw" },
  "basic-password": { header: "Authorization", prefixes: "", encoding: "basic" },
  "x-api-key": { header: "X-Api-Key", prefixes: "", encoding: "raw" },
  raw: { header: "Authorization", prefixes: "", encoding: "raw" },
};
const OUT_PRESETS: Record<string, OutDraft> = {
  bearer: { header: "Authorization", template: "Bearer {cred}", encoding: "raw", basicUser: "" },
  "basic-password": { header: "Authorization", template: "{cred}", encoding: "basic", basicUser: "{user}" },
  "x-api-key": { header: "X-Api-Key", template: "{cred}", encoding: "raw", basicUser: "" },
  raw: { header: "Authorization", template: "{cred}", encoding: "raw", basicUser: "" },
};

// inSpecOf / outSpecOf are what a row's method means: its expression, or its
// preset's expansion (null for none, or an unknown preset).
export const inSpecOf = (r: EndpointRow): InDraft | null => (r.identityIn === EXPRESSION ? r.inSpec : IN_PRESETS[r.identityIn] ?? null);
export const outSpecOf = (r: EndpointRow): OutDraft | null => (r.apply === EXPRESSION ? r.outSpec : OUT_PRESETS[r.apply] ?? null);

const method = (m: string) => (m === "custom" ? EXPRESSION : m);
const inDraft = (s: DestInboundSpec): InDraft => ({ header: s.header, prefixes: s.prefixes.join(", "), encoding: s.encoding || "raw" });
const outDraft = (s: DestOutboundSpec): OutDraft => ({ header: s.header, template: s.template, encoding: s.encoding || "raw", basicUser: s.basicUser });

export function rowOf(e: DestEndpoint): EndpointRow {
  return {
    name: e.name, subdomain: e.subdomain, route: e.route, upstream: e.upstream,
    identityIn: method(e.identityIn), apply: method(e.apply), allowPaths: e.allowPaths.join(" "), git: e.git ? "yes" : "no",
    inSpec: e.identityInSpec ? inDraft(e.identityInSpec) : null, outSpec: e.applySpec ? outDraft(e.applySpec) : null,
    logHeadersIn: e.logHeadersIn || "none", logHeadersOut: e.logHeadersOut || "none", open: false,
  };
}

export const blankRow = (): EndpointRow => ({
  name: "", subdomain: "", route: "", upstream: "", identityIn: "bearer", apply: "bearer", allowPaths: "", git: "no",
  inSpec: null, outSpec: null, logHeadersIn: "none", logHeadersOut: "none", open: false,
});

export function draftOf(d: DestDetail): Draft {
  return { credName: d.fields.credName, note: d.fields.note, endpoints: d.fields.endpoints.map(rowOf), env: d.fields.env.map((e) => ({ ...e })) };
}

// withRows is next with each row just switched to expression opened, its
// expression seeded from the preset it had (as the prototype does).
export function withRows(prev: EndpointRow[], next: EndpointRow[]): EndpointRow[] {
  return next.map((r, i) => {
    const p = prev[i];
    if (!p) return r;
    const out = { ...r };
    if (r.identityIn === EXPRESSION && p.identityIn !== EXPRESSION) {
      out.inSpec ??= { ...(IN_PRESETS[p.identityIn] ?? IN_PRESETS.bearer!) };
      out.open = true;
    }
    if (r.apply === EXPRESSION && p.apply !== EXPRESSION) {
      out.outSpec ??= { ...(OUT_PRESETS[p.apply] ?? OUT_PRESETS.bearer!) };
      out.open = true;
    }
    return out;
  });
}

// logs is whether an endpoint logs headers (its row's logging tag).
export const logs = (r: EndpointRow) => r.logHeadersIn !== "none" || r.logHeadersOut !== "none";

const enc = new TextEncoder();
const words = (s: string) => s.split(/\s+/).filter(Boolean);
// prefixes are comma-separated; a prefix keeps its trailing space ("Bearer ").
const prefixList = (s: string) => s.split(",").map((p) => p.trimStart()).filter(Boolean);
const ENV_KEY = /^[A-Z_][A-Z0-9_]*$/;
const PLACEHOLDER = /\{[^}]*\}/g;
const ENDPOINT_PLACEHOLDER = /^\{(host|url):([^}]*)\}$/;
const HEADER = /^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/;

function headerProblem(h: string): string | undefined {
  if (h.trim() === "") return "Required";
  if (!HEADER.test(h.trim())) return "Not a header name";
  return undefined;
}

// problemsOf is every problem the card can see before saving.
export function problemsOf(d: Draft): Problems {
  const out: Problems = {};
  if (enc.encode(d.note).length > MAX_NOTE) out.note = `Over ${MAX_NOTE} bytes`;
  if (d.endpoints.length === 0) out.endpoints = "A destination needs an endpoint";
  else if (d.endpoints.length > MAX_ENDPOINTS) out.endpoints = `At most ${MAX_ENDPOINTS}`;
  const names = new Set<string>();
  const addrs = new Set<string>();
  d.endpoints.forEach((r, i) => {
    const at = `endpoints[${i}].`;
    if (r.name.trim() === "") out[at + "name"] = "Required";
    else if (names.has(r.name)) out[at + "name"] = "Name already used";
    names.add(r.name);
    const addr = `${r.subdomain}\n${r.route}`;
    if (r.subdomain === "" && r.route === "") out[at + "route"] = "Give a subdomain, a route or both";
    else if (addrs.has(addr)) out[at + (r.route ? "route" : "subdomain")] = "Another endpoint has this subdomain and route";
    addrs.add(addr);
    if (r.upstream.trim() === "") out[at + "upstream"] = "Required";
    if (words(r.allowPaths).some((p) => !p.startsWith("/"))) out[at + "allowPaths"] = "Each path starts with /";
    if (r.identityIn === EXPRESSION && r.inSpec) {
      const s = r.inSpec;
      const p = at + "identityInSpec.";
      const h = headerProblem(s.header);
      if (h) out[p + "header"] = h;
      if (s.encoding === "basic" && s.header.trim().toLowerCase() !== "authorization") out[p + "encoding"] = "Basic needs header Authorization";
      if (s.encoding === "basic" && prefixList(s.prefixes).length > 0) out[p + "prefixes"] = "Prefixes don't apply to basic encoding";
    }
    if (r.apply === EXPRESSION && r.outSpec) {
      const s = r.outSpec;
      const p = at + "applySpec.";
      const h = headerProblem(s.header);
      if (h) out[p + "header"] = h;
      if (s.template.split("{cred}").length !== 2) out[p + "template"] = "The template needs {cred} exactly once";
      if (s.encoding === "basic" && s.header.trim().toLowerCase() !== "authorization") out[p + "encoding"] = "Basic needs header Authorization";
      if (s.encoding === "basic" && (s.basicUser.trim() === "" || s.basicUser.includes(":"))) out[p + "basicUser"] = "Basic needs a user without :";
      else if (s.encoding !== "basic" && s.basicUser.trim() !== "") out[p + "basicUser"] = "Only for basic encoding";
    }
  });
  const keys = new Set<string>();
  d.env.forEach((e, i) => {
    const at = `env[${i}].`;
    if (!ENV_KEY.test(e.key)) out[at + "key"] = e.key ? "Uppercase A–Z, 0–9 or _" : "Required";
    else if (keys.has(e.key)) out[at + "key"] = "Variable already set";
    keys.add(e.key);
    for (const ph of e.template.match(PLACEHOLDER) ?? []) {
      if (["{url}", "{base}", "{host}", "{token}"].includes(ph)) continue;
      const m = ENDPOINT_PLACEHOLDER.exec(ph);
      if (!m) out[at + "template"] = `Unknown placeholder ${ph}`;
      else if (!names.has(m[2]!)) out[at + "template"] = `${ph} names no endpoint`;
    }
  });
  return out;
}

// saveOf is the update body: every field (a save replaces them all).
export function saveOf(d: Draft): DestFields {
  return {
    credName: d.credName.trim(), note: d.note,
    env: d.env.map((e) => ({ key: e.key.trim(), template: e.template.trim() })),
    endpoints: d.endpoints.map((r) => ({
      name: r.name.trim(), subdomain: r.subdomain.trim(), route: r.route.trim(), upstream: r.upstream.trim(),
      identityIn: r.identityIn === EXPRESSION ? "custom" : r.identityIn,
      identityInSpec: r.identityIn === EXPRESSION && r.inSpec ? { header: r.inSpec.header.trim(), prefixes: prefixList(r.inSpec.prefixes), encoding: r.inSpec.encoding } : null,
      apply: r.apply === EXPRESSION ? "custom" : r.apply,
      applySpec: r.apply === EXPRESSION && r.outSpec ? { header: r.outSpec.header.trim(), template: r.outSpec.template, encoding: r.outSpec.encoding, basicUser: r.outSpec.basicUser.trim() } : null,
      allowPaths: words(r.allowPaths), git: r.git === "yes", logHeadersIn: r.logHeadersIn, logHeadersOut: r.logHeadersOut,
    })),
  };
}
