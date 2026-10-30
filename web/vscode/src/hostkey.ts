// Forgetting a studio's pinned host key, for after the studio was upgraded.
import { stat } from "node:fs/promises";
import type { ExecFile } from "./ssh/keys";

// forgetHostKey runs `ssh-keygen -R <alias> -f <knownHosts>` (execFile, no
// shell). A missing known_hosts has nothing to forget.
export async function forgetHostKey(alias: string, knownHosts: string, sshKeygen: string, run: ExecFile): Promise<void> {
  const st = await stat(knownHosts).catch((e: NodeJS.ErrnoException) => {
    if (e.code !== "ENOENT") throw e;
    return undefined;
  });
  if (!st) return;
  await run(sshKeygen, ["-R", alias, "-f", knownHosts]);
}
