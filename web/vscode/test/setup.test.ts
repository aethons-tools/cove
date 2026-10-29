import { mkdir, mkdtemp, readFile, stat, writeFile } from "node:fs/promises";
import { readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import * as path from "node:path";
import { describe, expect, it } from "vitest";
import type { JamApi } from "../src/api";
import { renderBlock } from "../src/ssh/config";
import { ensureSsh } from "../src/ssh/setup";
import type { ExecFile } from "../src/ssh/keys";

const fx = (n: string) => readFileSync(path.join(__dirname, "fixtures", n), "utf8").trim();
const pubA = fx("keyA.pub");
const cert = fx("keyA-cert.pub");
const S = "ses_0abcdefghjkmnpqrstvwxyz012";
const URL = "https://jam.example.com";

async function env() {
  const home = await mkdtemp(path.join(tmpdir(), "jamsetup-"));
  const xdg = path.join(home, "xdg");
  const jam = path.join(home, "jam");
  await writeFile(jam, "#!/bin/sh\n", { mode: 0o755 });
  await writeFile(path.join(home, "ssh-keygen"), "#!/bin/sh\n", { mode: 0o755 });
  const issued: string[] = [];
  const api = { issueCert: async (s: string) => (issued.push(s), { certificate: cert, expiresAt: "x" }) } as unknown as JamApi;
  const run: ExecFile = async (_f, args) => {
    if (args.includes("-y")) return { stdout: pubA + "\n", stderr: "" };
    await writeFile(args[args.length - 1], "PRIVATE");
    return { stdout: "", stderr: "" };
  };
  // now is 30 min before expiry so certs always count as due, unless fresh
  const mkDeps = (now: Date, platform: NodeJS.Platform = "linux") => ({
    api, jamUrl: URL, cliPath: jam, env: { XDG_CONFIG_HOME: xdg, PATH: home } as NodeJS.ProcessEnv, home, platform, run, now: () => now,
  });
  return { home, xdg, jam, issued, mkDeps };
}
const DUE = new Date("2026-01-01T23:30:00Z");
const FRESH = new Date("2026-01-01T12:00:00Z");

describe("ensureSsh", () => {
  it("writes key, cert and block, then the block equals renderBlock", async () => {
    const e = await env();
    const r = await ensureSsh(e.mkDeps(DUE), S);
    expect(r).toEqual({ alias: `jam-${S}`, owner: "extension" });
    const key = path.join(e.xdg, "jam", "ssh", "id_ed25519");
    const certFile = path.join(e.xdg, "jam", "ssh", `${S}-cert.pub`);
    expect((await stat(key)).mode & 0o777).toBe(0o600);
    expect(await readFile(certFile, "utf8")).toBe(cert + "\n");
    const cfg = await readFile(path.join(e.home, ".ssh", "config"), "utf8");
    expect(cfg).toBe(renderBlock({ studio: S, jamPath: e.jam, jamUrl: URL, keyPath: key, certPath: certFile }));
  });

  it("a jam ssh-config block is left byte for byte and no cert is fetched", async () => {
    const e = await env();
    const orig = `# >>> jam ${S} >>>\nHost jam-${S}\n  User agent\nMatch all\n# <<< jam ${S} <<<\n`;
    await mkdir(path.join(e.home, ".ssh"));
    await writeFile(path.join(e.home, ".ssh", "config"), orig);
    const r = await ensureSsh(e.mkDeps(DUE), S);
    expect(r.owner).toBe("other");
    expect(await readFile(path.join(e.home, ".ssh", "config"), "utf8")).toBe(orig);
    expect(e.issued).toEqual([]);
    await expect(stat(path.join(e.xdg, "jam"))).rejects.toThrow();
  });

  it("win32 errors before touching files", async () => {
    const e = await env();
    await expect(ensureSsh(e.mkDeps(DUE, "win32"), S)).rejects.toThrow(
      "Jam's SSH needs the jam CLI, which runs on macOS and Linux",
    );
    await expect(stat(path.join(e.home, ".ssh"))).rejects.toThrow();
    await expect(stat(path.join(e.xdg))).rejects.toThrow();
  });

  it("a second run with a fresh cert makes no API call", async () => {
    const e = await env();
    await ensureSsh(e.mkDeps(FRESH), S);
    expect(e.issued).toHaveLength(1);
    const cfg1 = await readFile(path.join(e.home, ".ssh", "config"), "utf8");
    await ensureSsh(e.mkDeps(FRESH), S);
    expect(e.issued).toHaveLength(1);
    expect(await readFile(path.join(e.home, ".ssh", "config"), "utf8")).toBe(cfg1);
  });
});
