import { describe, expect, it } from "vitest";
import { oldHref } from "./legacy";

describe("oldHref", () => {
  it.each([
    ["", "/app/admin/"],
    ["config", "/app/admin/config"],
    ["config/tracking", "/app/admin/config"],
    ["config/projects", "/app/admin/projects"],
    ["config/projects/acme", "/app/admin/projects/acme"],
    ["config/projects/acme/members", "/app/admin/projects/acme/members"],
    ["config/projects/acme/members/usr_1", "/app/admin/users/usr_1"],
    ["config/projects/acme/agents", "/app/admin/projects/acme/agents"],
    ["config/projects/a%20b/agents/coder", "/app/admin/projects/a%20b/roles/coder"],
    ["config/projects/acme/intercom", "/app/admin/projects/acme/intercom"],
    ["config/projects/acme/escalation", "/app/admin/projects/acme/escalation"],
    ["config/users", "/app/admin/users"],
    ["config/users/usr_1", "/app/admin/users/usr_1"],
    ["config/kits/base", "/app/admin/kits/base"],
    ["config/destinations/gh", "/app/admin/destinations/gh"],
    ["config/model-specs/m1", "/app/admin/model-specs/m1"],
    ["agents", "/app/admin/agents"],
    ["agents/ses_1/session", "/app/admin/agents/ses_1/session"],
    ["health", "/app/admin/health"],
  ])("%s → %s", (path, want) => {
    expect(oldHref(path)).toBe(want);
  });
});
