// shot.mjs end to end: a page asking for a route with no mock fails the shot
// and writes no PNG. It needs headless Chrome (as `just manual-shot` does)
// and skips without one.
import { execFile } from "node:child_process";
import { existsSync, mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

const browser = process.env.JAM_BROWSER ?? "/opt/chrome-headless-shell/chrome-headless-shell";

describe.skipIf(!existsSync(browser))("a manual screenshot", () => {
  it("fails, writing nothing, when the page asks for an unmocked route", async () => {
    const dir = mkdtempSync(join(tmpdir(), "shot-test-"));
    const out = join(dir, "zeta.png");
    try {
      const run = await new Promise((ok) => {
        // No project zeta in src/shots/mocks.ts.
        execFile("node", ["scripts/shot.mjs", "admin/config/projects/zeta", "--out", out], (err, stdout, stderr) => ok({ code: err?.code ?? 0, stderr }));
      });
      expect(run.code).not.toBe(0);
      expect(run.stderr).toContain("no mock for GET /app/admin/api/projects/zeta");
      expect(existsSync(out)).toBe(false);
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  }, 60_000);
});
