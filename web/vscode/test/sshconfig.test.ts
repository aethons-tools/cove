import { chmod, lstat, mkdir, mkdtemp, readFile, readdir, stat, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import * as path from "node:path";
import { describe, expect, it } from "vitest";
import { EXT_MARK, blockOwner, editSshConfig, listJamStudios, renderBlock, spliceBlock, stripLegacyBlock } from "../src/ssh/config";
import { proxyBinary, shellQuote, sshConfigEscape, sshConfigPath } from "../src/ssh/quote";

const S = "ses_0abcdefghjkmnpqrstvwxyz012";
const T = "ses_1abcdefghjkmnpqrstvwxyz012";
const opts = {
  studio: S,
  jamPath: "/usr/local/bin/jam",
  jamUrl: "https://jam.example.com",
  keyPath: "/home/u/.config/jam/ssh/id_ed25519",
  certPath: `/home/u/.config/jam/ssh/${S}-cert.pub`,
};
const mk = (studio: string, extra = "") =>
  `# >>> jam ${studio} >>>\n${extra}Host jam-${studio}\n  User agent\nMatch all\n# <<< jam ${studio} <<<\n`;

describe("renderBlock", () => {
  it("a non-default Jam's block names its token profile with --app", () => {
    const b = renderBlock({ ...opts, jamUrl: "https://b.example", app: "vscode-b.example" });
    expect(b).toContain(" --admin-url 'https://b.example' --app 'vscode-b.example' --participant ");
  });
  it("equals the Go golden fixture", async () => {
    expect(renderBlock(opts)).toBe(await readFile(path.join(__dirname, "fixtures", "ssh-block.txt"), "utf8"));
  });
  it("paths with spaces and % are quoted and escaped as ssh.go does", () => {
    const b = renderBlock({ ...opts, keyPath: "/Users/a b/.config/jam/ssh/id_ed25519", certPath: "/c/100%/x-cert.pub", jamPath: "/opt/my jam/jam" });
    expect(b).toContain(`IdentityFile "/Users/a b/.config/jam/ssh/id_ed25519"\n`);
    expect(b).toContain("CertificateFile /c/100%%/x-cert.pub\n");
    expect(b).toContain(`ProxyCommand '/opt/my jam/jam' ssh-proxy`);
  });
});

describe("quote", () => {
  it("helpers", () => {
    expect(shellQuote("a'b")).toBe(`'a'\\''b'`);
    expect(proxyBinary("/a/b-c_d.e+f")).toBe("/a/b-c_d.e+f");
    expect(proxyBinary("/a b/jam")).toBe("'/a b/jam'");
    expect(sshConfigEscape("100%")).toBe("100%%");
    expect(sshConfigPath("/x y/100%")).toBe('"/x y/100%%"');
  });
});

describe("blockOwner", () => {
  const cases: [string, string, string][] = [
    ["none", "Host *\n  User x\n", "none"],
    ["extension", renderBlock(opts), "extension"],
    ["jam ssh-config block", mk(S), "other"],
    ["hand-written host", `Host jam-${S}\n  User me\n`, "other"],
    ["multi-pattern host", `Host foo jam-${S}\n  User me\n`, "other"],
    ["another studio", mk(T), "none"],
  ];
  it.each(cases)("%s", (_n, cfg, want) => {
    expect(blockOwner(cfg, S)).toBe(want);
  });
  it("incomplete marked block is other", () => {
    expect(blockOwner(`# >>> jam ${S} >>>\nHost x\n`, S)).toBe("other");
  });
});

describe("spliceBlock", () => {
  const blk = renderBlock(opts);
  it("into an empty file", () => expect(spliceBlock("", S, blk)).toBe(blk));
  it("after leading comments, before Host *", () => {
    expect(spliceBlock("# hi\n\nHost *\n  A b\n", S, blk)).toBe("# hi\n\n" + blk + "\nHost *\n  A b\n");
  });
  it("before another studio's block", () => {
    const other = mk(T);
    expect(spliceBlock(other, S, blk)).toBe(blk + "\n" + other);
  });
  it("in place of its own block", () => {
    expect(spliceBlock(mk(S) + "\nHost a\n", S, blk)).toBe(blk + "\nHost a\n");
    expect(spliceBlock("# c\n" + mk(S) + "Host z\n", S, blk)).toBe("# c\n" + blk + "Host z\n");
  });
  it("two begins throws", () => {
    expect(() => spliceBlock(mk(S) + mk(S), S, blk)).toThrow();
  });
  it("begin without end throws", () => {
    expect(() => spliceBlock(`# >>> jam ${S} >>>\nHost x\n`, S, blk)).toThrow();
  });
});

describe("stripLegacyBlock / listJamStudios", () => {
  it("removes the studio's at-jam block and the blank line after it", () => {
    const leg = (s: string) => `# >>> at-jam ${s} >>>\nHost jam-${s}\n# <<< at-jam ${s} <<<\n`;
    expect(stripLegacyBlock(`# c\n${leg(S)}\n${leg(T)}Host z\n`, S)).toBe(`# c\n${leg(T)}Host z\n`);
  });
  it("lists complete marked blocks", () => {
    expect(listJamStudios(mk(S) + "\n" + mk(T) + `# >>> jam ses_x >>>\nHost y\n`)).toEqual([S, T]);
  });
});

describe("editSshConfig", () => {
  const home = () => mkdtemp(path.join(tmpdir(), "jamssh-"));
  it("keeps CRLF", async () => {
    const f = path.join(await home(), "config");
    await writeFile(f, "Host a\r\n  B c\r\n");
    await editSshConfig(f, (s) => {
      expect(s).toBe("Host a\n  B c\n");
      return s + "Host d\n";
    });
    expect(await readFile(f, "utf8")).toBe("Host a\r\n  B c\r\nHost d\r\n");
  });
  it("keeps mode 0644", async () => {
    const f = path.join(await home(), "config");
    await writeFile(f, "x\n");
    await chmod(f, 0o644);
    await editSshConfig(f, (s) => s + "y\n");
    expect((await stat(f)).mode & 0o777).toBe(0o644);
  });
  it("writes through a symlink to its target", async () => {
    const d = await home();
    const real = path.join(d, "dotfiles", "ssh_config");
    await mkdir(path.dirname(real));
    await writeFile(real, "a\n");
    const link = path.join(d, "config");
    await symlink(real, link);
    await editSshConfig(link, (s) => s + "b\n");
    expect((await lstat(link)).isSymbolicLink()).toBe(true);
    expect(await readFile(real, "utf8")).toBe("a\nb\n");
  });
  it("creates ~/.ssh 0700 and the file 0600", async () => {
    const f = path.join(await home(), ".ssh", "config");
    await editSshConfig(f, () => "x\n");
    expect((await stat(path.dirname(f))).mode & 0o777).toBe(0o700);
    expect((await stat(f)).mode & 0o777).toBe(0o600);
  });
  it("a throwing edit leaves the file untouched", async () => {
    const d = await home();
    const f = path.join(d, "config");
    await writeFile(f, "orig\r\n");
    await expect(editSshConfig(f, () => { throw new Error("boom"); })).rejects.toThrow("boom");
    expect(await readFile(f, "utf8")).toBe("orig\r\n");
    expect(await readdir(d)).toEqual(["config"]);
  });
  it("EXT_MARK is exact", () => {
    expect(EXT_MARK).toBe("# managed by the Jam VS Code extension (aethons.jam)");
  });
});
