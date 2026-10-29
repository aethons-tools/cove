import { describe, expect, it, vi } from "vitest";
import { ApiError, SignedOutError } from "../src/api";
import {
  connectStudio,
  connectedSet,
  disconnectStudio,
  openSession,
  pickSession,
  remoteUri,
  statusBarItem,
  studioFromAuthority,
  webUrl,
  windowAuthority,
  windowKind,
} from "../src/open";
import type { OpenDeps, Ui } from "../src/open";

const ID = "ses_0abcdefghjkmnpqrstvwxyz012";
const URL_ = "https://jam.example.com";
const local = (id: string) => `file:///gs/sessions/${id}.code-workspace`;

function fake(over: { head?: unknown; headErr?: Error; sshErr?: Error; remote?: boolean; global?: unknown; rail?: unknown; conv?: unknown; connected?: string[] } = {}) {
  const mem = new Set<string>();
  const connected = new Set<string>(over.connected ?? []);
  const state = { global: over.global as unknown };
  const titles: string[] = [];
  const steps: string[] = [];
  const logs: string[] = [];
  const order: string[] = [];
  const answers: Record<string, string | undefined> = {};
  const ui = {
    error: vi.fn(async (_m: string, ..._a: string[]) => answers.error),
    info: vi.fn(async (_m: string, ..._a: string[]) => answers.info),
    pick: vi.fn(async (items: { label: string }[], _p: string) => items[0]),
    openExternal: vi.fn(async () => {}),
    openFolder: vi.fn(async (u: string, o?: unknown) => void order.push(`open ${u}${o ? " reuse" : ""}`)),
    localWorkspace: vi.fn(async (id: string) => local(id)),
    isConnected: vi.fn((id: string) => connected.has(id)),
    setConnected: vi.fn(async (id: string, on: boolean) => {
      order.push(`${on ? "mark" : "unmark"} ${id}`);
      if (on) connected.add(id);
      else connected.delete(id);
    }),
    hasRemoteSsh: vi.fn(() => over.remote ?? true),
    installRemoteSsh: vi.fn(async () => {}),
    getGlobal: vi.fn(() => state.global),
    setGlobal: vi.fn(async (_s: string, _k: string, v: unknown) => { state.global = v; }),
    remembered: vi.fn((k: string) => mem.has(k)),
    remember: vi.fn(async (k: string) => { mem.add(k); }),
    windowAuthority: vi.fn(() => undefined),
    progress: vi.fn(async (title: string, work: (step: (m: string) => void) => Promise<unknown>) => {
      titles.push(title);
      return work((m) => steps.push(m));
    }),
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
    order.push("ssh");
    if (over.sshErr) throw over.sshErr;
    return { alias: `jam-${s}`, owner: "extension" as const };
  });
  const d: OpenDeps = { ui, signedIn, ssh: ssh as never, log: (m) => void logs.push(m) };
  return { d, ui, api, signedIn, ssh, answers, titles, steps, logs, order, connected };
}

const BAD_IDS = ["ses_x", "ses_0abcdefghjkmnpqrstvwxyz012%0AHost", "../../x", "ses_0ABCDEFGHJKMNPQRSTVWXYZ012", ""];

describe("openSession", () => {
  it("checks the session, then opens its local workspace in a new window, with no SSH", async () => {
    const f = fake();
    await openSession(f.d, ID);
    expect(f.titles).toEqual([`Jam: opening ${ID}…`]);
    expect(f.steps).toEqual(["checking the session", "opening the window"]);
    expect(f.api.sessionHead).toHaveBeenCalledWith(ID);
    expect(f.ui.localWorkspace).toHaveBeenCalledWith(ID);
    expect(f.ui.openFolder).toHaveBeenCalledTimes(1);
    expect(f.ui.openFolder).toHaveBeenCalledWith(local(ID));
    expect(f.ssh).not.toHaveBeenCalled();
    expect(f.ui.hasRemoteSsh).not.toHaveBeenCalled();
    expect(f.logs).toEqual([
      `[${ID}] checking the session`,
      `[${ID}] session found: Ada`,
      `[${ID}] opening the window`,
      `[${ID}] openFolder ${local(ID)}`,
      `[${ID}] openFolder returned`,
    ]);
  });

  it("a connected session renews its SSH setup, then opens (focuses) its remote window", async () => {
    const f = fake({ connected: [ID], global: "always" });
    await openSession(f.d, ID);
    expect(f.order).toEqual(["ssh", `open ${remoteUri(ID)}`]);
    expect(f.ui.openFolder).toHaveBeenCalledWith(remoteUri(ID));
    expect(f.ui.localWorkspace).not.toHaveBeenCalled();
    expect(f.ssh).toHaveBeenCalledWith(f.api, ID);
    expect(f.steps).toEqual(["checking the session", "setting up SSH for Ada", "opening the window"]);
    expect(f.logs).toContain(`[${ID}] connected: opening its studio window`);
  });

  it.each([
    ["the SSH setup fails", { sshErr: new Error("boom") }],
    ["it can't be SSHed into", { head: { id: ID, label: "Ada", channel: "chn_1", ssh: "" } }],
    ["Remote-SSH is missing", { remote: false }],
  ])("a connected session: when %s, it clears the flag and opens the local window", async (_n, over) => {
    const f = fake({ connected: [ID], ...over });
    await openSession(f.d, ID);
    expect(f.ui.setConnected).toHaveBeenCalledWith(ID, false);
    expect(f.connected.has(ID)).toBe(false);
    expect(f.ui.openFolder).toHaveBeenCalledTimes(1);
    expect(f.ui.openFolder).toHaveBeenCalledWith(local(ID));
    expect(f.ui.error).not.toHaveBeenCalled();
    expect(f.logs.some((l) => l.startsWith(`[${ID}] can't reconnect: `))).toBe(true);
  });

  it("a SignedOutError from a connected session's SSH setup propagates", async () => {
    const f = fake({ connected: [ID], sshErr: new SignedOutError() });
    await expect(openSession(f.d, ID)).rejects.toBeInstanceOf(SignedOutError);
    expect(f.ui.openFolder).not.toHaveBeenCalled();
  });

  it("a session you can't SSH into still opens locally", async () => {
    const f = fake({ head: { id: ID, label: "Ada", channel: "chn_1", ssh: "" } });
    await openSession(f.d, ID);
    expect(f.ui.error).not.toHaveBeenCalled();
    expect(f.ui.openFolder).toHaveBeenCalledWith(local(ID));
  });

  it("not signed in runs sign-in first", async () => {
    const f = fake();
    await openSession(f.d, ID);
    expect(f.signedIn).toHaveBeenCalledTimes(1);
    expect(f.signedIn.mock.invocationCallOrder[0]!).toBeLessThan(f.ui.openFolder.mock.invocationCallOrder[0]!);
  });

  it.each(BAD_IDS)("bad id %j is refused before any call", async (id) => {
    const f = fake();
    await openSession(f.d, id);
    expect(f.ui.error).toHaveBeenCalledWith("That isn't a Jam session link.");
    expect(f.signedIn).not.toHaveBeenCalled();
    expect(f.api.sessionHead).not.toHaveBeenCalled();
    expect(f.ui.localWorkspace).not.toHaveBeenCalled();
    expect(f.ui.openFolder).not.toHaveBeenCalled();
  });

  it("404 offers the web app root and opens nothing", async () => {
    const f = fake({ headErr: new ApiError("nope", 404, "not_found") });
    f.answers.error = "Open in the web app";
    await openSession(f.d, ID);
    expect(f.ui.error).toHaveBeenCalledWith("No such session, or you can't see it.", "Open in the web app");
    expect(f.ui.openExternal).toHaveBeenCalledWith("https://jam.example.com/app/user/");
    expect(f.ui.localWorkspace).not.toHaveBeenCalled();
    expect(f.ui.openFolder).not.toHaveBeenCalled();
    expect(f.logs).toContain(`[${ID}] stopped: No such session, or you can't see it.`);
  });

  it("a 401 propagates as SignedOutError", async () => {
    const f = fake({ headErr: new SignedOutError() });
    await expect(openSession(f.d, ID)).rejects.toBeInstanceOf(SignedOutError);
    expect(f.ui.openFolder).not.toHaveBeenCalled();
  });

  it("logs and rethrows an unexpected error", async () => {
    const f = fake({ headErr: new Error("net down") });
    await expect(openSession(f.d, ID)).rejects.toThrow("net down");
    expect(f.logs).toContain(`[${ID}] error: net down`);
  });
});

describe("connectStudio", () => {
  it("runs the SSH steps, then reloads this window onto the remote (the remote window marks itself)", async () => {
    const f = fake({ global: "always" });
    await connectStudio(f.d, ID);
    expect(f.titles).toEqual([`Jam: connecting ${ID}…`]);
    expect(f.steps).toEqual(["checking the session", "setting up SSH for Ada", "connecting the window"]);
    expect(f.ssh).toHaveBeenCalledWith(f.api, ID);
    expect(f.order).toEqual(["ssh", `open ${remoteUri(ID)} reuse`]);
    expect(f.ui.setConnected).not.toHaveBeenCalled();
    expect(f.ui.openFolder).toHaveBeenCalledWith(remoteUri(ID), { reuse: true });
    expect(f.logs).toEqual([
      `[${ID}] checking the session`,
      `[${ID}] session found: Ada`,
      `[${ID}] setting up SSH for Ada`,
      `[${ID}] ssh ready: alias jam-${ID}`,
      `[${ID}] connecting the window`,
      `[${ID}] openFolder ${remoteUri(ID)} (this window)`,
      `[${ID}] openFolder returned`,
    ]);
  });

  it.each(BAD_IDS)("bad id %j is refused before any call", async (id) => {
    const f = fake();
    await connectStudio(f.d, id);
    expect(f.ui.error).toHaveBeenCalledWith("That isn't a Jam session link.");
    expect(f.signedIn).not.toHaveBeenCalled();
    expect(f.ssh).not.toHaveBeenCalled();
    expect(f.ui.openFolder).not.toHaveBeenCalled();
  });

  it("empty ssh shows the reason and Open in the web app", async () => {
    const f = fake({ head: { id: ID, label: "Ada", channel: "chn_1", ssh: "" } });
    f.answers.error = "Open in the web app";
    await connectStudio(f.d, ID);
    expect(f.ui.error).toHaveBeenCalledWith("You can't SSH into Ada's studio (ask an operator to share it).", "Open in the web app");
    expect(f.ui.openExternal).toHaveBeenCalledWith("https://jam.example.com/app/user/c/chn_1");
    expect(f.ssh).not.toHaveBeenCalled();
    expect(f.ui.openFolder).not.toHaveBeenCalled();
    expect(f.ui.setConnected).not.toHaveBeenCalled();
  });

  it("404 offers the web app root", async () => {
    const f = fake({ headErr: new ApiError("nope", 404, "not_found") });
    f.answers.error = "Open in the web app";
    await connectStudio(f.d, ID);
    expect(f.ui.error).toHaveBeenCalledWith("No such session, or you can't see it.", "Open in the web app");
    expect(f.ui.openFolder).not.toHaveBeenCalled();
  });

  it("a SignedOutError from ssh propagates and is not shown", async () => {
    const f = fake({ sshErr: new SignedOutError() });
    await expect(connectStudio(f.d, ID)).rejects.toBeInstanceOf(SignedOutError);
    expect(f.ui.error).not.toHaveBeenCalled();
    expect(f.ui.openFolder).not.toHaveBeenCalled();
  });

  it("missing Remote-SSH offers Install and does not connect", async () => {
    const f = fake({ remote: false });
    f.answers.error = "Install";
    await connectStudio(f.d, ID);
    expect(f.ui.error).toHaveBeenCalledWith("Connecting to a studio needs Microsoft's Remote - SSH extension.", "Install");
    expect(f.ui.installRemoteSsh).toHaveBeenCalled();
    expect(f.ui.openFolder).not.toHaveBeenCalled();
    expect(f.ssh).not.toHaveBeenCalled();
  });

  it("ssh error is shown and logged, and does not connect", async () => {
    const f = fake({ sshErr: new Error("The jam CLI was not found.") });
    await connectStudio(f.d, ID);
    expect(f.ui.error).toHaveBeenCalledWith("The jam CLI was not found.");
    expect(f.logs).toContain(`[${ID}] stopped: The jam CLI was not found.`);
    expect(f.ui.openFolder).not.toHaveBeenCalled();
    expect(f.ui.setConnected).not.toHaveBeenCalled();
  });

  it("a non-401 ApiError from ssh gets the cert message", async () => {
    const f = fake({ sshErr: new ApiError("x", 409, "") });
    await connectStudio(f.d, ID);
    expect(f.ui.error).toHaveBeenCalledWith(`studio ${ID} is not running`);
  });

  it("asks about localServerDownload once; Set it writes always globally", async () => {
    const f = fake({ global: "auto" });
    f.answers.info = "Set it";
    await connectStudio(f.d, ID);
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
    await connectStudio(f.d, ID);
    expect(f.ui.info).not.toHaveBeenCalled();
    expect(f.ui.openFolder).toHaveBeenCalledTimes(2);
  });

  it("Not now still connects and is remembered", async () => {
    const f = fake({ global: "auto" });
    f.answers.info = "Not now";
    await connectStudio(f.d, ID);
    expect(f.ui.setGlobal).not.toHaveBeenCalled();
    expect(f.ui.remember).toHaveBeenCalled();
    expect(f.ui.openFolder).toHaveBeenCalledTimes(1);
  });

  it("never asks when the value is already always", async () => {
    const f = fake({ global: "always" });
    await connectStudio(f.d, ID);
    expect(f.ui.info).not.toHaveBeenCalled();
  });
});

describe("disconnectStudio", () => {
  it("reloads this window onto its local workspace, with no API call (deactivate unmarks)", async () => {
    const f = fake({ connected: [ID] });
    await disconnectStudio(f.d, ID);
    expect(f.order).toEqual([`open ${local(ID)} reuse`]);
    expect(f.ui.openFolder).toHaveBeenCalledWith(local(ID), { reuse: true });
    expect(f.signedIn).not.toHaveBeenCalled();
    expect(f.ssh).not.toHaveBeenCalled();
    expect(f.logs).toEqual([`[${ID}] disconnecting: openFolder ${local(ID)} (this window)`]);
  });

  it.each(BAD_IDS)("bad id %j is refused", async (id) => {
    const f = fake();
    await disconnectStudio(f.d, id);
    expect(f.ui.openFolder).not.toHaveBeenCalled();
    expect(f.ui.setConnected).not.toHaveBeenCalled();
  });
});

describe("connectedSet", () => {
  it("keeps one key per session, jam.connected.<id>, ignoring junk", async () => {
    const m = new Map<string, unknown>();
    const st = { get: <T>(k: string) => m.get(k) as T | undefined, update: async (k: string, v: unknown) => void (v === undefined ? m.delete(k) : m.set(k, v)) };
    const c = connectedSet(st);
    expect(c.isConnected(ID)).toBe(false);
    await c.setConnected(ID, true);
    expect([...m]).toEqual([[`jam.connected.${ID}`, true]]);
    expect(c.isConnected(ID)).toBe(true);
    await c.setConnected(ID, false);
    expect([...m]).toEqual([]);
    m.set(`jam.connected.${ID}`, "nonsense");
    expect(c.isConnected(ID)).toBe(false);
    await c.setConnected("ses_x", true);
    expect(m.has("jam.connected.ses_x")).toBe(false);
  });
});

describe("windowKind", () => {
  const DIR = "/gs/sessions";
  const file = (path: string, scheme = "file") => ({ scheme, authority: "", path });
  it.each([
    ["a session workspace file", { workspaceFile: file(`${DIR}/${ID}.code-workspace`) }, { kind: "local", id: ID }],
    ["a jam remote folder", { workspaceFolders: [{ uri: { scheme: "vscode-remote", authority: `ssh-remote+jam-${ID}`, path: "/home/agent/workspace" } }] }, { kind: "remote", id: ID }],
    ["the same name elsewhere", { workspaceFile: file(`/elsewhere/sessions/${ID}.code-workspace`) }, undefined],
    ["a lookalike parent", { workspaceFile: file(`${DIR}/x/${ID}.code-workspace`) }, undefined],
    ["a lookalike dir prefix", { workspaceFile: file(`${DIR}-evil/${ID}.code-workspace`) }, undefined],
    ["a non-session workspace", { workspaceFile: file(`/home/u/proj.code-workspace`) }, undefined],
    ["a bad id", { workspaceFile: file(`${DIR}/ses_x.code-workspace`) }, undefined],
    ["a non-file scheme", { workspaceFile: file(`${DIR}/${ID}.code-workspace`, "untitled") }, undefined],
    ["a local folder", { workspaceFolders: [{ uri: file("/home/u/proj") }] }, undefined],
    ["an empty window", {}, undefined],
  ])("%s", (_n, w, want) => {
    expect(windowKind(w, DIR, "linux")).toEqual(want);
  });
  it("matches case-insensitively on darwin and win32 (drive letter too), but not on linux", () => {
    const w = { workspaceFile: file(`/Users/Ada/Library/Application Support/Code/User/globalStorage/aethons.jam/sessions/${ID}.code-workspace`) };
    const dir = "/users/ada/library/application support/code/user/globalstorage/aethons.jam/sessions/";
    expect(windowKind(w, dir, "darwin")).toEqual({ kind: "local", id: ID });
    expect(windowKind(w, dir, "linux")).toBeUndefined();
    const win = { workspaceFile: file(`/C:/Users/Ada/AppData/Roaming/Code/User/globalStorage/aethons.jam/sessions/${ID}.code-workspace`) };
    expect(windowKind(win, "/c:/users/ada/appdata/roaming/code/user/globalstorage/aethons.jam/sessions", "win32")).toEqual({ kind: "local", id: ID });
    // The id itself is never case-folded.
    const upper = { workspaceFile: file(`${dir}${ID.toUpperCase()}.code-workspace`) };
    expect(windowKind(upper, dir, "darwin")).toBeUndefined();
    // Lookalikes stay rejected.
    expect(windowKind({ workspaceFile: file(`/Users/Ada/sessions/${ID}.code-workspace`) }, dir, "darwin")).toBeUndefined();
    expect(windowKind({ workspaceFile: file(`${dir}x/${ID}.code-workspace`) }, dir, "darwin")).toBeUndefined();
  });
});

describe("statusBarItem", () => {
  it("Connect in a local session window, Disconnect in a remote one, nothing elsewhere", () => {
    expect(statusBarItem({ kind: "local", id: ID })).toEqual({ text: "$(plug) Connect studio", command: "jam.connectStudio", tooltip: "Jam: connect this window to the session's studio over Remote-SSH" });
    expect(statusBarItem({ kind: "remote", id: ID })).toEqual({ text: "$(debug-disconnect) Disconnect studio", command: "jam.disconnectStudio", tooltip: "Jam: disconnect from the studio so it can idle" });
    expect(statusBarItem(undefined)).toBeUndefined();
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
    expect(f.ui.openFolder).toHaveBeenCalledWith(local(ID));
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
