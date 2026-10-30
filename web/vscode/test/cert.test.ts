import { mkdtemp, readFile, stat } from "node:fs/promises";
import { readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import * as path from "node:path";
import { describe, expect, it } from "vitest";
import { ApiError, SignedOutError } from "../src/api";
import type { JamApi } from "../src/api";
import { CERT_RENEW_BEFORE_MS, certErrorMessage, certValidBefore, ensureCert } from "../src/ssh/cert";

const fx = (n: string) => readFileSync(path.join(__dirname, "fixtures", n), "utf8").trim();
const pubA = fx("keyA.pub");
const pubB = fx("keyB.pub");
const cert = fx("keyA-cert.pub");
const VALID_BEFORE = new Date("2026-01-02T00:00:00Z");
const S = "ses_0abcdefghjkmnpqrstvwxyz012";

const apiReturning = (certificate: string, calls: string[] = []) =>
  ({
    issueCert: async (studio: string, key: string) => {
      calls.push(studio + " " + key);
      return { certificate, expiresAt: "x" };
    },
  }) as unknown as JamApi;
const dest = async () => path.join(await mkdtemp(path.join(tmpdir(), "jamcert-")), "s-cert.pub");

describe("certValidBefore", () => {
  it("reads valid_before", () => {
    expect(certValidBefore(cert, pubA)).toEqual(VALID_BEFORE);
  });
  it("cert for another key is null", () => {
    expect(certValidBefore(cert, pubB)).toBeNull();
    expect(certValidBefore("garbage", pubA)).toBeNull();
    expect(certValidBefore(pubA, pubA)).toBeNull();
  });
});

describe("ensureCert", () => {
  const at = (msLeft: number) => new Date(VALID_BEFORE.getTime() - msLeft);
  it("renews with 59 min left, keeps with 61 min", async () => {
    const f = await dest();
    const calls: string[] = [];
    await ensureCert(apiReturning(cert, calls), S, pubA, f, at(59 * 60_000));
    expect(calls).toEqual([`${S} ${pubA}`]);
    expect(await readFile(f, "utf8")).toBe(cert + "\n");
    expect((await stat(f)).mode & 0o777).toBe(0o644);
    await ensureCert(apiReturning(cert, calls), S, pubA, f, at(61 * 60_000));
    expect(calls).toHaveLength(1);
    expect(CERT_RENEW_BEFORE_MS).toBe(3_600_000);
  });
  it("returned cert for another key errors", async () => {
    const f = await dest();
    await expect(ensureCert(apiReturning(cert), S, pubB, f, new Date())).rejects.toThrow(
      "Jam returned a certificate that is not for this key",
    );
    await expect(stat(f)).rejects.toThrow();
  });
  it("401 propagates SignedOutError", async () => {
    const api = { issueCert: async () => { throw new SignedOutError(); } } as unknown as JamApi;
    await expect(ensureCert(api, S, pubA, await dest(), new Date())).rejects.toBeInstanceOf(SignedOutError);
  });
});

describe("certErrorMessage", () => {
  it("409 message", () => {
    expect(certErrorMessage(new ApiError("x", 409, ""), S)).toBe(`studio ${S} is not running`);
  });
  it("others", () => {
    expect(certErrorMessage(new ApiError("x", 403, ""), S)).toContain("don't have SSH access");
    expect(certErrorMessage(new ApiError("x", 404, ""), S)).toContain(`no studio ${S}`);
    expect(certErrorMessage(new ApiError("boom", 500, ""), S)).toBe("Jam refused (500): boom");
  });
});
