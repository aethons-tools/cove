// The SSH key shared with the jam CLI (cmd/jam/ssh.go:ensureSSHKey). The key
// is made by ssh-keygen: node:crypto can't write OpenSSH's private-key format.
import { chmod, link, mkdir, mkdtemp, rm, stat } from "node:fs/promises";
import * as path from "node:path";

export type ExecFile = (file: string, args: string[]) => Promise<{ stdout: string; stderr: string }>;

// ensureKey returns the public key line ("ssh-ed25519 AAAA… jam") of the key
// at keyPath, creating the key when it is absent.
export async function ensureKey(keyPath: string, sshKeygen: string, run: ExecFile): Promise<string> {
  const dir = path.dirname(keyPath);
  await mkdir(dir, { recursive: true, mode: 0o700 });
  let st = await stat(keyPath).catch((e: NodeJS.ErrnoException) => {
    if (e.code !== "ENOENT") throw e;
    return undefined;
  });
  if (!st) {
    const tmp = await mkdtemp(path.join(dir, ".tmp-key-"));
    try {
      const made = path.join(tmp, "id_ed25519");
      await run(sshKeygen, ["-t", "ed25519", "-N", "", "-C", "jam", "-q", "-f", made]);
      try {
        await link(made, keyPath); // EEXIST: another run won, which is fine
      } catch (e) {
        if ((e as NodeJS.ErrnoException).code !== "EEXIST") throw e;
      }
    } finally {
      await rm(tmp, { recursive: true, force: true });
    }
    st = await stat(keyPath);
  }
  if ((st.mode & 0o077) !== 0) {
    try {
      await chmod(keyPath, 0o600);
    } catch (e) {
      throw new Error(`${keyPath} is readable by other users and could not be made private: ${(e as Error).message}`);
    }
  }
  const { stdout } = await run(sshKeygen, ["-y", "-f", keyPath]);
  const pub = stdout.trim();
  if (!pub.startsWith("ssh-ed25519 ")) {
    throw new Error(`${keyPath} is not an ed25519 key (remove it to generate a new one)`);
  }
  return pub;
}
