// The app-token file the jam CLI reads: {"token","jam_url"}, 0600 in a 0700
// directory, replaced atomically.
import { randomBytes } from "node:crypto";
import { chmod, mkdir, rename, rm, writeFile } from "node:fs/promises";
import * as path from "node:path";

export async function writeAppTokenFile(file: string, f: { token: string; jamUrl: string }): Promise<void> {
  const dir = path.dirname(file);
  await mkdir(dir, { recursive: true, mode: 0o700 });
  await chmod(dir, 0o700);
  const tmp = path.join(dir, `.${path.basename(file)}.${randomBytes(6).toString("hex")}.tmp`);
  try {
    await writeFile(tmp, JSON.stringify({ token: f.token, jam_url: f.jamUrl }) + "\n", { mode: 0o600 });
    await rename(tmp, file);
  } catch (e) {
    await rm(tmp, { force: true });
    throw e;
  }
}

export async function removeAppTokenFile(file: string): Promise<void> {
  await rm(file, { force: true });
}
