import type { DestDetail, DestFields, SpecDetail, SpecFields } from "../api/types";

export const destFields = (f: Partial<DestFields> = {}): DestFields => ({
  route: "/gh/", upstream: "https://api.github.com", identityIn: "bearer", apply: "bearer", credName: "gh-pat", env: "GH_HOST={host}",
  allowPaths: "", note: "", logHeadersIn: "none", logHeadersOut: "none", git: false, ...f,
});

export const dest = (d: Partial<DestDetail> = {}): DestDetail => ({
  name: "gh", managed: null, fields: destFields(), methods: ["bearer", "x-api-key", "basic"], identityInSpec: "", applySpec: "",
  allowPaths: [], env: [{ key: "GH_HOST", template: "api.github.com" }], envImplied: false, gitRouted: false, gitImplied: false,
  uses: [{ project: "acme", role: "coder", cred: "", credIsRole: false }, { project: "acme", role: "rev", cred: "rev-pat", credIsRole: true }],
  conflicts: [], conflictRoles: [], ...d,
});

export const specFields = (f: Partial<SpecFields> = {}): SpecFields => ({
  type: "claude", version: "2.1.3", versionConstraint: "", principal: "anthropic", modelId: "", effort: "", mode: "", provider: "anthropic",
  headers: "", allow: "", deny: "", providerEnv: "", plugins: "", settings: "", note: "", ...f,
});

export const spec = (s: Partial<SpecDetail> = {}): SpecDetail => ({
  name: "fast", managed: null, fields: specFields(), choices: { types: ["claude"], principals: ["anthropic", "other"], modes: ["default", "plan"], providers: ["anthropic", "vertex"] },
  headerRules: [], allow: [], deny: [], hasClaude: true, providerEnvKeys: [], plugins: [], settingsKeys: [],
  uses: [{ project: "acme", role: "coder", implicit: false }, { project: "acme", role: "rev", implicit: true }], ...s,
});
