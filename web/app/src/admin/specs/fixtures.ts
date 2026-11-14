import type { DestDetail, DestEndpoint, DestFields, SpecDetail, SpecFields } from "../api/types";

export const endpoint = (e: Partial<DestEndpoint> = {}): DestEndpoint => ({
  name: "gh", subdomain: "", route: "/gh/", upstream: "https://api.github.com", identityIn: "bearer", identityInSpec: null, apply: "bearer", applySpec: null,
  allowPaths: [], git: false, logHeadersIn: "none", logHeadersOut: "none", ...e,
});

export const destFields = (f: Partial<DestFields> = {}): DestFields => ({
  credName: "gh-pat", note: "", env: [{ key: "GH_HOST", template: "{host}" }], endpoints: [endpoint()], ...f,
});

export const dest = (d: Partial<DestDetail> = {}): DestDetail => ({
  name: "gh", fields: destFields(), methods: ["bearer", "x-api-key", "basic"],
  env: [{ key: "GH_HOST", template: "api.github.com" }], envImplied: false, gitRouted: false, gitImplied: false,
  uses: [{ project: "acme", role: "coder", cred: "", credIsRole: false }, { project: "acme", role: "rev", cred: "rev-pat", credIsRole: true }],
  conflicts: [], conflictRoles: [], notes: { markdown: "", html: "" }, ...d,
});

export const specFields = (f: Partial<SpecFields> = {}): SpecFields => ({
  type: "claude", version: "2.1.3", versionConstraint: "", principal: "anthropic", modelId: "", effort: "", mode: "", provider: "anthropic",
  headers: "", allow: "", deny: "", providerEnv: "", plugins: "", settings: "", note: "", ...f,
});

export const spec = (s: Partial<SpecDetail> = {}): SpecDetail => ({
  name: "fast", fields: specFields(), choices: { types: ["claude"], principals: ["anthropic", "other"], modes: ["default", "plan"], providers: ["anthropic", "vertex"] },
  headerRules: [], allow: [], deny: [], hasClaude: true, providerEnvKeys: [], plugins: [], settingsKeys: [],
  uses: [{ project: "acme", role: "coder", implicit: false }, { project: "acme", role: "rev", implicit: true }], notes: { markdown: "", html: "" }, ...s,
});
