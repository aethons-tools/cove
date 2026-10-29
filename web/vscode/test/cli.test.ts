import { chmod, mkdir, mkdtemp, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import * as path from "node:path";
import { describe, expect, it } from "vitest";
import { findJam, findSshKeygen } from "../src/ssh/cli";

const tmp = () => mkdtemp(path.join(tmpdir(), "jamcli-"));
const NOT_FOUND = "The jam CLI was not found. Install it (see the Jam docs) or set jam.cliPath.";

describe("findJam", () => {
  it("cliPath relative is an error", async () => {
    const r = await findJam("bin/jam", "");
    expect(r).toBeInstanceOf(Error);
    expect((r as Error).message).toContain("absolute");
  });
  it("absolute executable cliPath is used; non-executable is an error", async () => {
    const d = await tmp();
    const j = path.join(d, "jam");
    await writeFile(j, "#!/bin/sh\n", { mode: 0o755 });
    expect(await findJam(j, "")).toBe(j);
    await chmod(j, 0o644);
    expect(await findJam(j, "")).toBeInstanceOf(Error);
  });
  it("PATH search finds an executable, skips a non-executable", async () => {
    const a = await tmp();
    const b = await tmp();
    await writeFile(path.join(a, "jam"), "x", { mode: 0o644 });
    await writeFile(path.join(b, "jam"), "x", { mode: 0o755 });
    expect(await findJam("", [a, b].join(path.delimiter))).toBe(path.join(b, "jam"));
  });
  it("not found message", async () => {
    const r = await findJam("", await tmp());
    expect((r as Error).message).toBe(NOT_FOUND);
  });
  it("ignores relative PATH entries", async () => {
    const d = await tmp();
    await mkdir(path.join(d, "rel"));
    expect(await findJam("", "rel")).toBeInstanceOf(Error);
  });
});

describe("findSshKeygen", () => {
  it("finds it on PATH or errors", async () => {
    const d = await tmp();
    await writeFile(path.join(d, "ssh-keygen"), "x", { mode: 0o755 });
    expect(await findSshKeygen(d)).toBe(path.join(d, "ssh-keygen"));
    expect(((await findSshKeygen(await tmp())) as Error).message).toContain("ssh-keygen");
  });
});
