// Everything the SSH host needs before Remote-SSH connects: the key, a fresh
// cert and the studio's block in ~/.ssh/config.
import { readFile } from "node:fs/promises";
import type { JamApi } from "../api";
import { certPath, configDir, keyPath, sshConfigFile } from "../paths";
import { ensureCert } from "./cert";
import { findJam, findSshKeygen } from "./cli";
import { blockOwner, editSshConfig, renderBlock, spliceBlock, stripLegacyBlock } from "./config";
import { ensureKey } from "./keys";
import type { ExecFile } from "./keys";

export type SshResult = { alias: string; owner: "extension" | "other" };

export type SshDeps = {
  api: JamApi;
  jamUrl: string;
  // app is the jam CLI token profile for a Jam other than the default.
  app?: string;
  cliPath: string;
  env: NodeJS.ProcessEnv;
  home: string;
  platform: NodeJS.Platform;
  run: ExecFile;
  now: () => Date;
};

export async function ensureSsh(d: SshDeps, studio: string): Promise<SshResult> {
  if (d.platform === "win32") throw new Error("Jam's SSH needs the jam CLI, which runs on macOS and Linux");
  const alias = `jam-${studio}`;
  const cfgFile = sshConfigFile(d.home);
  const existing = await readFile(cfgFile, "utf8").catch((e: NodeJS.ErrnoException) => {
    if (e.code !== "ENOENT") throw e;
    return "";
  });
  // A jam ssh-config block or a hand-written Host is not ours to touch; the
  // CLI's ssh-proxy renews its cert.
  if (blockOwner(existing.replaceAll("\r\n", "\n"), studio) === "other") return { alias, owner: "other" };

  const jam = await findJam(d.cliPath, d.env.PATH ?? "");
  if (jam instanceof Error) throw jam;
  const keygen = await findSshKeygen(d.env.PATH ?? "");
  if (keygen instanceof Error) throw keygen;

  const cfg = configDir(d.env, d.home);
  const key = keyPath(cfg);
  const cert = certPath(cfg, studio);
  const pub = await ensureKey(key, keygen, d.run);
  await ensureCert(d.api, studio, pub, cert, d.now());
  const block = renderBlock({ studio, jamPath: jam, jamUrl: d.jamUrl, keyPath: key, certPath: cert, ...(d.app ? { app: d.app } : {}) });
  await editSshConfig(cfgFile, (s) => {
    if (blockOwner(s, studio) === "other") throw new Error(`~/.ssh/config changed: ${alias} is now managed elsewhere`);
    return spliceBlock(stripLegacyBlock(s, studio), studio, block);
  });
  return { alias, owner: "extension" };
}
