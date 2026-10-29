import { describe, expect, it, vi } from "vitest";
import { ApiError, SignedOutError } from "../src/api";
import { openSession, pickSession, remoteUri, studioFromAuthority, webUrl, windowAuthority } from "../src/open";
import type { OpenDeps, Ui } from "../src/open";

const ID = "ses_0abcdefghjkmnpqrstvwxyz012";
const URL_ = "https://jam.example.com";

function fake(over: { head?: unknown; headErr?: Error; sshErr?: Error; remote?: boolean; global?: unknown; rail?: unknown; conv?: unknown } = {}) {
  const mem = new Set<string>();
  const state = { global: over.global as unknown };
  const answers: Record<string, string | undefined> = {};
  const ui = {
    error: vi.fn(async (_m: string, ..._a: string[]) => answers.error),
    info: vi.fn(async (_m: string, ..._a: string[]) => answers.info),
    pick: vi.fn(async (items: { label: string }[], _p: string) => items[0]),
    openExternal: vi.fn(async () => {}),
    openFolder: vi.fn(async () => {}),
    hasRemoteSsh: vi.fn(() => over.remote ?? true),
    installRemoteSsh: vi.fn(async () => {}),
    getGlobal: vi.fn(() => state.global),
    setGlobal: vi.fn(async (_s: string, _k: string, v: unknown) => { state.global = v; }),
    remembered: vi.fn((k: string) => mem.has(k)),
    remember: vi.fn(async (k: string) => { mem.add(k); }),
    windowAuthority: vi.fn(() => undefined),
  } as unknown as Ui & { [K in keyof Ui]: ReturnType<typeof vi.fn> };
  const api = {
    sessionHead: vi.fn(async () => {
      if (over.headErr) throw over.headErr;
      return over.head ?? { id: ID, label: "Ada", channel: "chn_1", ssh: "jam ssh x" };
    }),
    rail: vi.fn(async () => over.rail),
    conversation: vi.fn(async () => over.conv),
  };
  const signedIn = vi.fn(async () => ({ api: api as never, url: URL_ }));
  const ssh = vi.fn(async (_a: unknown, s: string) => {
    if (over.sshErr) throw over.sshErr;
    return { alias: `jam-${s}`, owner: "extension" as const };
  });
  const d: OpenDeps = { ui, signedIn, ssh: ssh as never };
  return { d, ui, api, signedIn, ssh, answers };
}

describe("openSession", () => {
  it("opens the remote folder after sign-in and ssh", async () => {
    const f = fake({ global: "always" });
    await openSession(f.d, ID);
    expect(f.ui.openFolder).toHaveBeenCalledTimes(1);
    expect(f.ui.openFolder).toHaveBeenCalledWith(`vscode-remote://ssh-remote+jam-${ID}/home/agent/workspace`);
    expect(f.ssh).toHaveBeenCalledWith(f.api, ID);
  });

  it("not signed in runs sign-in first, then opens", async () => {
    const f = fake({ global: "always" });
    const order: string[] = [];
    f.signedIn.mockImplementation(async () => { order.push("signIn"); return { api: f.api as never, url: URL_ }; });
    f.ssh.mockImplementation(async (_a, s) => { order.push("ssh"); return { alias: `jam-${s}`, owner: "extension" as const }; });
    await openSession(f.d, ID);
    expect(order).toEqual(["signIn", "ssh"]);
    expect(f.ui.openFolder).toHaveBeenCalled();
  });

  it.each(["ses_x", "ses_0abcdefghjkmnpqrstvwxyz012%0AHost", "../../x", "ses_0ABCDEFGHJKMNPQRSTVWXYZ012", ""])(
    "bad id %j is refused before any call",
    async (id) => {
      const f = fake();
      await openSession(f.d, id);
      expect(f.ui.error).toHaveBeenCalledWith("That isn't a Jam session link.");
      expect(f.signedIn).not.toHaveBeenCalled();
      expect(f.ssh).not.toHaveBeenCalled();
      expect(f.api.sessionHead).not.toHaveBeenCalled();
      expect(f.ui.openFolder).not.toHaveBeenCalled();
    },
  );

  it("empty ssh shows the reason and Open in the web app", async () => {
    const f = fake({ head: { id: ID, label: "Ada", channel: "chn_1", ssh: "" } });
    f.answers.error = "Open in the web app";
    await openSession(f.d, ID);
    expect(f.ui.error).toHaveBeenCalledWith(
      "You can't SSH into Ada's studio (ask an operator to share it).",
      "Open in the web app",
    );
    expect(f.ui.openExternal).toHaveBeenCalledWith("https://jam.example.com/app/user/c/chn_1");
    expect(f.ssh).not.toHaveBeenCalled();
    expect(f.ui.openFolder).not.toHaveBeenCalled();
  });

  it("404 offers the web app root", async () => {
    const f = fake({ headErr: new ApiError("nope", 404, "not_found") });
    f.answers.error = "Open in the web app";
    await openSession(f.d, ID);
    expect(f.ui.error).toHaveBeenCalledWith("No such session, or you can't see it.", "Open in the web app");
    expect(f.ui.openExternal).toHaveBeenCalledWith("https://jam.example.com/app/user/");
    expect(f.ui.openFolder).not.toHaveBeenCalled();
  });

  it("a 401 propagates as SignedOutError", async () => {
    const f = fake({ headErr: new SignedOutError() });
    await expect(openSession(f.d, ID)).rejects.toBeInstanceOf(SignedOutError);
    expect(f.ui.openFolder).not.toHaveBeenCalled();
  });

  it("a SignedOutError from ssh propagates and is not shown", async () => {
    const f = fake({ sshErr: new SignedOutError() });
    await expect(openSession(f.d, ID)).rejects.toBeInstanceOf(SignedOutError);
    expect(f.ui.error).not.toHaveBeenCalled();
    expect(f.ui.openFolder).not.toHaveBeenCalled();
  });

  it("missing Remote-SSH offers Install and does not open", async () => {
    const f = fake({ remote: false });
    f.answers.error = "Install";
    await openSession(f.d, ID);
    expect(f.ui.error).toHaveBeenCalledWith("Opening a studio needs Microsoft's Remote - SSH extension.", "Install");
    expect(f.ui.installRemoteSsh).toHaveBeenCalled();
    expect(f.ui.openFolder).not.toHaveBeenCalled();
    expect(f.ssh).not.toHaveBeenCalled();
  });

  it("ssh error is shown and does not open", async () => {
    const f = fake({ sshErr: new Error("The jam CLI was not found.") });
    await openSession(f.d, ID);
    expect(f.ui.error).toHaveBeenCalledWith("The jam CLI was not found.");
    expect(f.ui.openFolder).not.toHaveBeenCalled();
  });

  it("a non-401 ApiError from ssh gets the cert message", async () => {
    const f = fake({ sshErr: new ApiError("x", 409, "") });
    await openSession(f.d, ID);
    expect(f.ui.error).toHaveBeenCalledWith(`studio ${ID} is not running`);
  });

  it("asks about localServerDownload once; Set it writes always globally", async () => {
    const f = fake({ global: "auto" });
    f.answers.info = "Set it";
    await openSession(f.d, ID);
    expect(f.ui.info).toHaveBeenCalledWith(
      "Jam studios can't download VS Code Server themselves. Set Remote-SSH's localServerDownload to 'always'?",
      "Set it",
      "Not now",
    );
    expect(f.ui.setGlobal).toHaveBeenCalledWith("remote.SSH", "localServerDownload", "always");
    expect(f.ui.remember).toHaveBeenCalledWith("jam.askedLocalServerDownload");
    expect(f.ui.openFolder).toHaveBeenCalledTimes(1);
    f.ui.getGlobal.mockReturnValue("auto");
    f.ui.info.mockClear();
    await openSession(f.d, ID);
    expect(f.ui.info).not.toHaveBeenCalled();
    expect(f.ui.openFolder).toHaveBeenCalledTimes(2);
  });

  it("Not now still opens and is remembered", async () => {
    const f = fake({ global: "auto" });
    f.answers.info = "Not now";
    await openSession(f.d, ID);
    expect(f.ui.setGlobal).not.toHaveBeenCalled();
    expect(f.ui.remember).toHaveBeenCalled();
    expect(f.ui.openFolder).toHaveBeenCalledTimes(1);
  });

  it("never asks when the value is already always", async () => {
    const f = fake({ global: "always" });
    await openSession(f.d, ID);
    expect(f.ui.info).not.toHaveBeenCalled();
  });
});

const row = (id: string, kind: string, over: Record<string, unknown> = {}) => ({
  id, label: id, project: "p", kind, needsYou: false, mission: "m", ...over,
});

describe("pickSession", () => {
  const rail = {
    groups: [
      { bucket: "needs-you", title: "Needs you", rows: [row("chn_a", "studio", { label: "A", needsYou: true }), row("chn_n", "named")] },
      { bucket: "active", title: "Sessions", rows: [row("chn_a", "studio"), row("chn_b", "studio", { label: "B" }), row("chn_d", "dm")] },
      { bucket: "history", title: "History", rows: [row("chn_h", "studio")] },
    ],
    unreadTotal: 0,
  };

  it("lists live studio rows only and opens the picked row's session", async () => {
    const f = fake({ rail, conv: { session: ID }, global: "always" });
    await pickSession(f.d);
    const items = f.ui.pick.mock.calls[0]![0] as { label: string; description: string; detail: string }[];
    expect(items.map((i) => i.label)).toEqual(["$(bell) A", "B"]);
    expect(items[0]).toMatchObject({ description: "p", detail: "m" });
    expect(f.api.conversation).toHaveBeenCalledWith("chn_a");
    expect(f.ui.openFolder).toHaveBeenCalledWith(remoteUri(ID));
  });

  it("with no session on the conversation shows the message", async () => {
    const f = fake({ rail, conv: { session: "" } });
    await pickSession(f.d);
    expect(f.ui.error).toHaveBeenCalledWith("That conversation has no session you can open");
    expect(f.ui.openFolder).not.toHaveBeenCalled();
  });

  it("a conversation session that is not a valid id is refused", async () => {
    const f = fake({ rail, conv: { session: "ses_x\nHost *" } });
    await pickSession(f.d);
    expect(f.ssh).not.toHaveBeenCalled();
    expect(f.ui.openFolder).not.toHaveBeenCalled();
  });
});

describe("helpers", () => {
  const hex = (o: unknown) => Buffer.from(typeof o === "string" ? o : JSON.stringify(o)).toString("hex");
  it.each([
    [`ssh-remote+jam-${ID}`, ID],
    [`ssh-remote+${hex({ hostName: `jam-${ID}` })}`, ID],
    [`ssh-remote+${hex({ hostName: `jam-${ID}`, user: "agent" })}`, ID],
    ["ssh-remote+other", undefined],
    ["wsl+Ubuntu", undefined],
    ["dev-container+abc", undefined],
    [`ssh-remote+${hex("{not json")}`, undefined],
    [`ssh-remote+${hex({ hostName: "other" })}`, undefined],
    ["ssh-remote+jam-ses_x", undefined],
    [undefined, undefined],
  ])("studioFromAuthority(%j)", (a, want) => {
    expect(studioFromAuthority(a)).toBe(want);
  });
  it("windowAuthority", () => {
    const r = (authority: string) => ({ scheme: "vscode-remote", authority });
    expect(windowAuthority({ workspaceFolders: [{ uri: r("ssh-remote+x") }] })).toBe("ssh-remote+x");
    expect(windowAuthority({ workspaceFolders: [{ uri: { scheme: "file", authority: "" } }] })).toBeUndefined();
    expect(windowAuthority({ workspaceFile: r("ssh-remote+y") })).toBe("ssh-remote+y");
    expect(windowAuthority({})).toBeUndefined();
  });
  it("webUrl", () => {
    expect(webUrl(URL_, "chn_1")).toBe(`${URL_}/app/user/c/chn_1`);
    expect(webUrl(URL_, "a b")).toBe(`${URL_}/app/user/c/a%20b`);
    expect(webUrl(URL_, "")).toBe(`${URL_}/app/user/`);
  });
});
