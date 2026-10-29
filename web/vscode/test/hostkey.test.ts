import { mkdtemp, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import * as path from "node:path";
import { describe, expect, it, vi } from "vitest";
import { forgetHostKey } from "../src/hostkey";

describe("forgetHostKey", () => {
  it("runs ssh-keygen -R jam-<studio> -f <known_hosts> via execFile", async () => {
    const dir = await mkdtemp(path.join(tmpdir(), "jamhk-"));
    const kh = path.join(dir, "known_hosts");
    await writeFile(kh, "x\n");
    const run = vi.fn(async () => ({ stdout: "", stderr: "" }));
    await forgetHostKey("jam-ses_1", kh, "/usr/bin/ssh-keygen", run);
    expect(run).toHaveBeenCalledWith("/usr/bin/ssh-keygen", ["-R", "jam-ses_1", "-f", kh]);
  });

  it("missing known_hosts is a no-op", async () => {
    const dir = await mkdtemp(path.join(tmpdir(), "jamhk-"));
    const run = vi.fn(async () => ({ stdout: "", stderr: "" }));
    await forgetHostKey("jam-ses_1", path.join(dir, "known_hosts"), "/usr/bin/ssh-keygen", run);
    expect(run).not.toHaveBeenCalled();
  });
});
