import { mkdtemp, readFile, readdir, stat } from "node:fs/promises";
import { tmpdir } from "node:os";
import * as path from "node:path";
import { describe, expect, it } from "vitest";
import { removeAppTokenFile, writeAppTokenFile } from "../src/tokenfile";

const tmp = () => mkdtemp(path.join(tmpdir(), "jamtok-"));

describe("tokenfile", () => {
  it("writes 0600 in a 0700 dir", async () => {
    const f = path.join(await tmp(), "jam", "default-app-token.json");
    await writeAppTokenFile(f, { token: "t", jamUrl: "https://j" });
    expect((await stat(f)).mode & 0o777).toBe(0o600);
    expect((await stat(path.dirname(f))).mode & 0o777).toBe(0o700);
    expect(JSON.parse(await readFile(f, "utf8"))).toEqual({ token: "t", jam_url: "https://j" });
  });
  it("replaces via rename (no partial file visible)", async () => {
    const f = path.join(await tmp(), "t.json");
    await writeAppTokenFile(f, { token: "a", jamUrl: "u" });
    await writeAppTokenFile(f, { token: "b", jamUrl: "u" });
    expect(JSON.parse(await readFile(f, "utf8")).token).toBe("b");
    expect(await readdir(path.dirname(f))).toEqual(["t.json"]);
  });
  it("remove of a missing file resolves", async () => {
    await expect(removeAppTokenFile(path.join(await tmp(), "none"))).resolves.toBeUndefined();
  });
});
