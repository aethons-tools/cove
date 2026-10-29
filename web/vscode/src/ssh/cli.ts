// Finding the jam CLI and ssh-keygen. No shell: PATH is searched directly.
import { access, constants, stat } from "node:fs/promises";
import * as path from "node:path";

// executable is true for a regular file we may execute (access(X_OK) alone
// accepts a directory).
const executable = async (p: string): Promise<boolean> => {
  try {
    await access(p, constants.X_OK);
    return (await stat(p)).isFile();
  } catch {
    return false;
  }
};

async function onPath(name: string, envPath: string): Promise<string | undefined> {
  for (const d of envPath.split(path.delimiter)) {
    if (!d || !path.isAbsolute(d)) continue;
    const p = path.join(d, name);
    if (await executable(p)) return p;
  }
  return undefined;
}

export async function findJam(cliPath: string, envPath: string): Promise<string | Error> {
  if (cliPath !== "") {
    if (!path.isAbsolute(cliPath)) return new Error("jam.cliPath must be an absolute path");
    if (!(await executable(cliPath))) return new Error(`jam.cliPath (${cliPath}) is not an executable file`);
    return cliPath;
  }
  return (await onPath("jam", envPath)) ?? new Error("The jam CLI was not found. Install it (see the Jam docs) or set jam.cliPath.");
}

export async function findSshKeygen(envPath: string): Promise<string | Error> {
  return (await onPath("ssh-keygen", envPath)) ?? new Error("ssh-keygen was not found; install an OpenSSH client.");
}
