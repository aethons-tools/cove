// The short-lived SSH certificate Jam signs for the key (the participant
// plane's POST /me/studios/<id>/ssh/cert, cmd/jam/ssh.go).
import { randomBytes } from "node:crypto";
import { chmod, mkdir, readFile, rename, rm, writeFile } from "node:fs/promises";
import * as path from "node:path";
import type { ApiError, JamApi } from "../api";

export const CERT_RENEW_BEFORE_MS = 3_600_000;
const CERT_TYPE = "ssh-ed25519-cert-v01@openssh.com";
const MAX_DATE_MS = 8.64e15;

class Reader {
  private o = 0;
  constructor(private b: Buffer) {}
  bytes(n: number): Buffer {
    if (n < 0 || this.o + n > this.b.length) throw new Error("short");
    const r = this.b.subarray(this.o, this.o + n);
    this.o += n;
    return r;
  }
  u32(): number {
    return this.bytes(4).readUInt32BE(0);
  }
  u64(): bigint {
    return this.bytes(8).readBigUInt64BE(0);
  }
  str(): Buffer {
    return this.bytes(this.u32());
  }
}

// ed25519Key extracts the 32 key bytes of an "ssh-ed25519 AAAA…" line.
function pubKeyBytes(pubLine: string): Buffer | null {
  try {
    const f = pubLine.trim().split(/\s+/);
    if (f[0] !== "ssh-ed25519" || !f[1]) return null;
    const r = new Reader(Buffer.from(f[1], "base64"));
    if (r.str().toString() !== "ssh-ed25519") return null;
    return r.str();
  } catch {
    return null;
  }
}

// certValidBefore is the expiry of certLine when it is an ed25519 certificate
// for pubLine's key, else null.
export function certValidBefore(certLine: string, pubLine: string): Date | null {
  const want = pubKeyBytes(pubLine);
  if (!want) return null;
  try {
    const f = certLine.trim().split(/\s+/);
    if (f[0] !== CERT_TYPE || !f[1]) return null;
    const r = new Reader(Buffer.from(f[1], "base64"));
    if (r.str().toString() !== CERT_TYPE) return null;
    r.str(); // nonce
    if (!r.str().equals(want)) return null;
    r.u64(); // serial
    r.u32(); // type
    r.str(); // key id
    r.str(); // principals
    r.u64(); // valid after
    const before = r.u64();
    return new Date(before > BigInt(MAX_DATE_MS) ? MAX_DATE_MS : Number(before) * 1000);
  } catch {
    return null;
  }
}

// ensureCert makes certPath hold a certificate for pubLine that stays valid
// for CERT_RENEW_BEFORE_MS. A SignedOutError from the API propagates.
export async function ensureCert(api: JamApi, studio: string, pubLine: string, certPath: string, now: Date): Promise<void> {
  const have = await readFile(certPath, "utf8").then(
    (s) => s,
    () => "",
  );
  const until = certValidBefore(have, pubLine);
  if (until && until.getTime() - now.getTime() >= CERT_RENEW_BEFORE_MS) return;
  const { certificate } = await api.issueCert(studio, pubLine);
  const line = certificate.trim();
  if (!certValidBefore(line, pubLine)) throw new Error("Jam returned a certificate that is not for this key");
  const dir = path.dirname(certPath);
  await mkdir(dir, { recursive: true, mode: 0o700 });
  const tmp = path.join(dir, `.${path.basename(certPath)}.${randomBytes(6).toString("hex")}.tmp`);
  try {
    await writeFile(tmp, line + "\n", { mode: 0o644 });
    await chmod(tmp, 0o644);
    await rename(tmp, certPath);
  } catch (e) {
    await rm(tmp, { force: true });
    throw e;
  }
}

// certErrorMessage mirrors cmd/jam/ssh.go:sshErrMessage for the participant plane.
export function certErrorMessage(e: ApiError, studio: string): string {
  switch (e.status) {
    case 403:
      return e.message.includes("not linked to a Jam user")
        ? "your login is not linked to a Jam user; ask an operator to link it"
        : "you don't have SSH access to this studio (ask an operator: jam studio share …)";
    case 404:
      return `no studio ${studio} on this Jam (or SSH is not enabled on it)`;
    case 409:
      return `studio ${studio} is not running`;
    case 429:
      return `too many SSH tunnels are open to studio ${studio}; close one and retry`;
    case 503:
      return `studio ${studio} is not connected to Jam right now; retry shortly`;
    case 504:
      return `studio ${studio} did not open the tunnel in time; retry (if it persists, the studio's image may predate SSH support)`;
  }
  return `Jam refused (${e.status}): ${e.message}`;
}
