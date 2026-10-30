// The studio's block in ~/.ssh/config. Ported from cmd/jam/ssh.go
// (sshConfigBlock, spliceSSHConfigBlock, stripLegacySSHBlocks, editSSHConfig)
// so the CLI and the extension treat each other's blocks alike. A user's
// existing file must never be damaged: every failure throws before a write.
import { randomBytes } from "node:crypto";
import { chmod, mkdir, readFile, realpath, rename, rm, stat, writeFile } from "node:fs/promises";
import * as path from "node:path";
import { proxyBinary, shellQuote, sshConfigEscape, sshConfigPath } from "./quote";

export const EXT_MARK = "# managed by the Jam VS Code extension (aethons.jam)";

const BEGIN_PREFIX = "# >>> jam ";
const END_PREFIX = "# <<< jam ";
const LEGACY_BEGIN = "# >>> at-jam ";
const LEGACY_END = "# <<< at-jam ";
const SSH_USER = "agent";

export type BlockOpts = { studio: string; jamPath: string; jamUrl: string; keyPath: string; certPath: string };
export type Owner = "none" | "extension" | "other";

const markers = (studio: string): [string, string] => [`${BEGIN_PREFIX}${studio} >>>`, `${END_PREFIX}${studio} <<<`];
const splitLines = (s: string): string[] => s.split(/(?<=\n)/);

export function renderBlock(o: BlockOpts): string {
  const [begin, end] = markers(o.studio);
  const proxy = sshConfigEscape(
    `${proxyBinary(o.jamPath)} ssh-proxy --admin-url ${shellQuote(o.jamUrl)} --participant ${o.studio}`,
  );
  return [
    begin,
    EXT_MARK,
    `Host jam-${o.studio}`,
    `  User ${SSH_USER}`,
    `  ProxyCommand ${proxy}`,
    `  IdentityFile ${sshConfigPath(o.keyPath)}`,
    `  CertificateFile ${sshConfigPath(o.certPath)}`,
    "  IdentitiesOnly yes",
    "  StrictHostKeyChecking accept-new",
    "  PreferredAuthentications publickey,gssapi-with-mic",
    "Match all",
    end,
  ].join("\n") + "\n";
}

// blockOwner says who wrote the config's entry for the studio. "other" is
// anything the extension must not overwrite: a marked block without EXT_MARK
// (jam ssh-config's), an unterminated or repeated one, or a `Host` line
// naming jam-<studio> outside a marked block.
export function blockOwner(config: string, studio: string): Owner {
  const [begin, end] = markers(studio);
  const lines = config.split(/\r?\n/);
  const alias = `jam-${studio}`;
  let begins = 0;
  let owner: Owner = "none";
  let inside = false;
  let ext = false;
  for (let i = 0; i < lines.length; i++) {
    const t = lines[i].trim();
    if (!inside && t === begin) {
      begins++;
      inside = true;
      ext = lines[i + 1]?.trim() === EXT_MARK;
      continue;
    }
    if (inside) {
      if (t === end) {
        inside = false;
        owner = ext ? "extension" : "other";
      }
      continue;
    }
    if (hostNames(t).includes(alias)) return "other";
  }
  if (inside || begins > 1) return "other";
  return owner;
}

// hostNames returns the patterns of a `Host` line ("Host a b", "host=a").
function hostNames(trimmed: string): string[] {
  const m = /^host(?:\s*=\s*|\s+)(.*)$/i.exec(trimmed);
  if (!m) return [];
  return m[1].split(/\s+/).map((p) => p.replace(/^"|"$/g, "")).filter(Boolean);
}

// spliceBlock puts block in place of the studio's marked block, else before
// the first line that is neither blank nor a comment (or opens another jam
// block), because OpenSSH takes the first value it finds.
export function spliceBlock(config: string, studio: string, block: string): string {
  const [begin, end] = markers(studio);
  const lines = splitLines(config);
  let bi = -1;
  let ei = -1;
  lines.forEach((l, i) => {
    const t = l.trim();
    if (t === begin) {
      if (bi >= 0) throw new Error(`two "${begin}" markers in ~/.ssh/config; fix the file by hand`);
      bi = i;
    } else if (t === end && bi >= 0 && ei < 0) {
      ei = i;
    }
  });
  if (bi >= 0) {
    if (ei < 0) throw new Error(`found "${begin}" without "${end}" in ~/.ssh/config; fix the file by hand`);
    return lines.slice(0, bi).join("") + block + lines.slice(ei + 1).join("");
  }
  let at = lines.length;
  for (let i = 0; i < lines.length; i++) {
    const t = lines[i].trim();
    if ((t !== "" && !t.startsWith("#")) || t.startsWith(BEGIN_PREFIX) || t.startsWith(LEGACY_BEGIN)) {
      at = i;
      break;
    }
  }
  let head = lines.slice(0, at).join("");
  if (head !== "" && !head.endsWith("\n")) head += "\n";
  const tail = lines.slice(at).join("");
  return head + block + (tail !== "" ? "\n" : "") + tail;
}

// stripLegacyBlock removes the studio's pre-rename ("at-jam") block and the
// blank line after it.
export function stripLegacyBlock(config: string, studio: string): string {
  const lines = splitLines(config);
  const want = `${LEGACY_END}${studio} <<<`;
  let out = "";
  for (let i = 0; i < lines.length; i++) {
    if (lines[i].trim() === `${LEGACY_BEGIN}${studio} >>>`) {
      let end = -1;
      for (let j = i + 1; j < lines.length; j++) {
        if (lines[j].trim() === want) {
          end = j;
          break;
        }
      }
      if (end >= 0) {
        i = end;
        if (i + 1 < lines.length && lines[i + 1].trim() === "" && lines[i + 1] !== "") i++;
        continue;
      }
    }
    out += lines[i];
  }
  return out;
}

// listJamStudios lists the studios with a complete marked block, in order.
export function listJamStudios(config: string): string[] {
  const out: string[] = [];
  let cur: string | undefined;
  for (const l of config.split(/\r?\n/)) {
    const t = l.trim();
    if (cur === undefined) {
      if (t.startsWith(BEGIN_PREFIX) && t.endsWith(" >>>")) {
        const s = t.slice(BEGIN_PREFIX.length, -4);
        if (s && !/\s/.test(s)) cur = s;
      }
    } else if (t === `${END_PREFIX}${cur} <<<`) {
      out.push(cur);
      cur = undefined;
    }
  }
  return out;
}

// editSshConfig rewrites the config (a symlink's target) through edit, which
// sees and returns LF endings; a CRLF file keeps its endings and every file
// its mode (new: 0600 in a 0700 directory). The replacement is atomic, and an
// edit that throws writes nothing.
export async function editSshConfig(file: string, edit: (s: string) => string): Promise<void> {
  await mkdir(path.dirname(file), { recursive: true, mode: 0o700 });
  let target = file;
  try {
    target = await realpath(file);
  } catch {
    // missing: create it at file
  }
  let mode = 0o600;
  let old = "";
  try {
    old = await readFile(target, "utf8");
    mode = (await stat(target)).mode & 0o777;
  } catch (e) {
    if ((e as NodeJS.ErrnoException).code !== "ENOENT") throw e;
  }
  const crlf = old.includes("\r\n");
  let s = edit(old.replaceAll("\r\n", "\n"));
  if (crlf) s = s.replaceAll("\n", "\r\n");
  const tmp = path.join(path.dirname(target), `.${path.basename(target)}.${randomBytes(6).toString("hex")}.tmp`);
  try {
    await writeFile(tmp, s, { mode });
    await chmod(tmp, mode);
    await rename(tmp, target);
  } catch (e) {
    await rm(tmp, { force: true });
    throw e;
  }
}
