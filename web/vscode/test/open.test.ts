import { describe, expect, it, vi } from "vitest";
import { ApiError, SignedOutError } from "../src/api";
import {
  CONNECTED_BEAT_MS,
  CONNECTED_STALE_MS,
  connectedSet,
  openSession,
  pickSession,
  goTo,
  HUB_FILE,
  PENDING_MAX_MS,
  readPending,
  remoteUri,
  studioFromAuthority,
  webUrl,
  windowAuthority,
  windowKind,
} from "../src/open";
import type { OpenDeps, Ui } from "../src/open";
import { editorFor } from "../src/editor";

const ID = "ses_0abcdefghjkmnpqrstvwxyz012";
const URL_ = "https://jam.example.com";

function fake(over: { head?: unknown; headErr?: Error; sshErr?: Error; remote?: boolean; global?: unknown; rail?: unknown; conv?: unknown; connected?: string[]; scheme?: string } = {}) {
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
    openFolder: vi.fn(async (u: string) => void order.push(`open ${u}`)),
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
  const d: OpenDeps = { ui, editor: editorFor(over.scheme ?? "vscode"), signedIn, ssh: ssh as never, log: (m) => void logs.push(m) };
  return { d, ui, api, signedIn, ssh, answers, titles, steps, logs, order, connected };
}

const BAD_IDS = ["ses_x", "ses_0abcdefghjkmnpqrstvwxyz012%0AHost", "../../x", "ses_0ABCDEFGHJKMNPQRSTVWXYZ012", ""];

describe("openSession", () => {
  it("checks the session, sets up SSH, then opens its studio window in a new window", async () => {
    const f = fake({ global: "always" });
    await openSession(f.d, ID);
    expect(f.titles).toEqual([`Jam: opening ${ID}…`]);
    expect(f.steps).toEqual(["checking the session", "setting up SSH for Ada", "opening the window"]);
    expect(f.order).toEqual(["ssh", `open ${remoteUri(ID)}`]);
    expect(f.ui.openFolder).toHaveBeenCalledTimes(1);
    expect(f.ui.openFolder).toHaveBeenCalledWith(remoteUri(ID));
    expect(f.ssh).toHaveBeenCalledWith(f.api, ID);
    expect(f.logs).toEqual([
      `[${ID}] checking the session`,
      `[${ID}] session found: Ada`,
      `[${ID}] setting up SSH for Ada`,
      `[${ID}] ssh ready: alias jam-${ID}`,
      `[${ID}] opening the window`,
      `[${ID}] openFolder ${remoteUri(ID)}`,
      `[${ID}] openFolder returned`,
    ]);
  });

  it("a session whose window is open: still one openFolder (VS Code focuses it), no connected-mark writes", async () => {
    const f = fake({ global: "always", connected: [ID] });
    await openSession(f.d, ID);
    expect(f.ui.openFolder).toHaveBeenCalledTimes(1);
    expect(f.ui.openFolder).toHaveBeenCalledWith(remoteUri(ID));
    expect(f.connected.has(ID)).toBe(true);
  });

  it("can't SSH: error with the web app, no ssh, no window", async () => {
    const f = fake({ head: { id: ID, label: "Ada", channel: "chn_1", ssh: "" } });
    f.answers.error = "Open in the web app";
    await openSession(f.d, ID);
    expect(f.ui.error).toHaveBeenCalledWith("You can't SSH into Ada's studio (ask an operator to share it).", "Open in the web app");
    expect(f.ui.openExternal).toHaveBeenCalledWith(`${URL_}/app/user/c/chn_1`);
    expect(f.ssh).not.toHaveBeenCalled();
    expect(f.ui.openFolder).not.toHaveBeenCalled();
  });

  it("a studio that isn't running (409) shows the cert message and opens nothing", async () => {
    const f = fake({ sshErr: new ApiError("x", 409, "") });
    await openSession(f.d, ID);
    expect(f.ui.error).toHaveBeenCalledWith(`studio ${ID} is not running`);
    expect(f.ui.openFolder).not.toHaveBeenCalled();
  });

  it("not signed in runs sign-in first", async () => {
    const f = fake({ global: "always" });
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
    expect(f.ssh).not.toHaveBeenCalled();
    expect(f.ui.openFolder).not.toHaveBeenCalled();
  });

  it("404 offers the web app root and opens nothing", async () => {
    const f = fake({ headErr: new ApiError("nope", 404, "not_found") });
    f.answers.error = "Open in the web app";
    await openSession(f.d, ID);
    expect(f.ui.error).toHaveBeenCalledWith("No such session, or you can't see it.", "Open in the web app");
    expect(f.ui.openExternal).toHaveBeenCalledWith("https://jam.example.com/app/user/");
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

  it("a SignedOutError from ssh propagates and is not shown", async () => {
    const f = fake({ sshErr: new SignedOutError() });
    await expect(openSession(f.d, ID)).rejects.toBeInstanceOf(SignedOutError);
    expect(f.ui.error).not.toHaveBeenCalled();
    expect(f.ui.openFolder).not.toHaveBeenCalled();
  });

  it("missing Remote-SSH offers Install and opens nothing", async () => {
    const f = fake({ remote: false });
    f.answers.error = "Install";
    await openSession(f.d, ID);
    expect(f.ui.error).toHaveBeenCalledWith("Connecting to a studio needs Microsoft's Remote - SSH extension.", "Install");
    expect(f.ui.installRemoteSsh).toHaveBeenCalled();
    expect(f.ui.openFolder).not.toHaveBeenCalled();
    expect(f.ssh).not.toHaveBeenCalled();
  });

  it("in VSCodium, missing Open Remote - SSH offers Install", async () => {
    const f = fake({ remote: false, scheme: "vscodium" });
    await openSession(f.d, ID);
    expect(f.ui.error).toHaveBeenCalledWith("Connecting to a studio needs jeanp413's Open Remote - SSH extension.", "Install");
  });

  it("ssh error is shown and logged, and opens nothing", async () => {
    const f = fake({ sshErr: new Error("The jam CLI was not found.") });
    await openSession(f.d, ID);
    expect(f.ui.error).toHaveBeenCalledWith("The jam CLI was not found.");
    expect(f.logs).toContain(`[${ID}] stopped: The jam CLI was not found.`);
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

  it("in VSCodium, never asks about localServerDownload", async () => {
    const f = fake({ global: "auto", scheme: "vscodium" });
    await openSession(f.d, ID);
    expect(f.ui.info).not.toHaveBeenCalled();
    expect(f.ui.setGlobal).not.toHaveBeenCalled();
    expect(f.ui.openFolder).toHaveBeenCalledWith(remoteUri(ID));
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

describe("connectedSet", () => {
  const memo = () => {
    const m = new Map<string, unknown>();
    return { m, st: { get: <T>(k: string) => m.get(k) as T | undefined, update: async (k: string, v: unknown) => void (v === undefined ? m.delete(k) : m.set(k, v)), keys: () => [...m.keys()] } };
  };

  it("list() is the sessions with a fresh mark only", async () => {
    const { m, st } = memo();
    const ID2 = "ses_1abcdefghjkmnpqrstvwxyz012";
    const now = 1_000_000;
    const c = connectedSet(st, () => now);
    await c.setConnected(ID, true);
    m.set(`jam.connected.${ID2}`, now - CONNECTED_STALE_MS - 1);
    m.set("jam.connected.ses_x", now);
    m.set("jam.other", now);
    expect(c.list()).toEqual([ID]);
  });

  it("keeps one key per session, jam.connected.<id>, holding the time it was marked, ignoring junk", async () => {
    const { m, st } = memo();
    const c = connectedSet(st, () => 1_000_000);
    expect(c.isConnected(ID)).toBe(false);
    await c.setConnected(ID, true);
    expect([...m]).toEqual([[`jam.connected.${ID}`, 1_000_000]]);
    expect(c.isConnected(ID)).toBe(true);
    await c.setConnected(ID, false);
    expect([...m]).toEqual([]);
    for (const junk of ["nonsense", true, null]) {
      m.set(`jam.connected.${ID}`, junk);
      expect(c.isConnected(ID)).toBe(false);
    }
    await c.setConnected("ses_x", true);
    expect(m.has("jam.connected.ses_x")).toBe(false);
  });

  it("a mark not refreshed within CONNECTED_STALE_MS is not connected (a window that went away without clearing it)", async () => {
    const { st } = memo();
    let t = 1_000_000;
    const c = connectedSet(st, () => t);
    await c.setConnected(ID, true);
    t += CONNECTED_STALE_MS - 1;
    expect(c.isConnected(ID)).toBe(true);
    t += 1;
    expect(c.isConnected(ID)).toBe(false);
    // A refresh (the studio window's beat) makes it fresh again.
    await c.setConnected(ID, true);
    expect(c.isConnected(ID)).toBe(true);
    // A mark from well in the future (a clock that jumped back) isn't trusted either.
    t -= CONNECTED_STALE_MS;
    expect(c.isConnected(ID)).toBe(false);
  });

  it("the beat refreshes well within the stale window", () => {
    expect(CONNECTED_BEAT_MS * 2).toBeLessThan(CONNECTED_STALE_MS);
  });
});

describe("goTo", () => {
  const CHN = "chn_0abcdefghjkmnpqrstvwxyz012";
  const deps = (o: { connected?: boolean; hub?: boolean; own?: string } = {}) => {
    const calls: string[] = [];
    return {
      calls,
      d: {
        isConnected: (id: string) => (calls.push(`isConnected ${id}`), o.connected ?? false),
        openSession: async (id: string) => void calls.push(`openSession ${id}`),
        isHub: o.hub ?? false,
        ownSession: o.own,
        openTab: (c: string) => void calls.push(`openTab ${c}`),
        writePending: async (p: { channel: string; at: number }) => void calls.push(`pending ${p.channel} ${p.at}`),
        openHub: async () => void calls.push("openHub"),
        now: () => 5,
      },
    };
  };
  it("a session with an open window: openSession only", async () => {
    const f = deps({ connected: true });
    await goTo(f.d, CHN, ID);
    expect(f.calls).toEqual([`isConnected ${ID}`, `openSession ${ID}`]);
  });
  it("in the hub: its tab", async () => {
    const f = deps({ hub: true });
    await goTo(f.d, CHN, ID);
    expect(f.calls).toEqual([`isConnected ${ID}`, `openTab ${CHN}`]);
    const g = deps({ hub: true });
    await goTo(g.d, CHN, "");
    expect(g.calls).toEqual([`openTab ${CHN}`]);
  });
  it("a studio window's own session: its own tab, not openSession", async () => {
    const f = deps({ connected: true, own: ID });
    await goTo(f.d, CHN, ID);
    expect(f.calls).toEqual([`openTab ${CHN}`]);
  });
  it("elsewhere: pending first, then the hub", async () => {
    const f = deps();
    await goTo(f.d, CHN, "");
    expect(f.calls).toEqual([`pending ${CHN} 5`, "openHub"]);
  });
});

describe("readPending", () => {
  const CHN = "chn_0abcdefghjkmnpqrstvwxyz012";
  it("a fresh, valid entry is its channel", () => {
    expect(readPending(JSON.stringify({ channel: CHN, at: 1000 }), 1000 + PENDING_MAX_MS)).toBe(CHN);
    expect(readPending(JSON.stringify({ channel: "legacy:dm:a", at: 1000 }), 1000)).toBe("legacy:dm:a");
  });
  it("stale, junk or bad entries are nothing", () => {
    expect(readPending(JSON.stringify({ channel: CHN, at: 1000 }), 1001 + PENDING_MAX_MS)).toBeUndefined();
    for (const raw of [undefined, "", "{", "[]", JSON.stringify({ channel: "chn_x", at: 1 }), JSON.stringify({ channel: CHN }), JSON.stringify({ channel: CHN, at: "1" })]) {
      expect(readPending(raw, 1)).toBeUndefined();
    }
  });
});

describe("windowKind", () => {
  const DIR = "/gs/hub";
  const HD = { scheme: "file", path: DIR };
  const file = (path: string, scheme = "file") => ({ scheme, authority: "", path });
  it.each([
    ["the hub file", { workspaceFile: file(`${DIR}/${HUB_FILE}`) }, { kind: "hub" }],
    ["a jam remote folder", { workspaceFolders: [{ uri: { scheme: "vscode-remote", authority: `ssh-remote+jam-${ID}`, path: "/home/agent/workspace" } }] }, { kind: "session", id: ID }],
    ["another workspace in the hub dir", { workspaceFile: file(`${DIR}/Other.code-workspace`) }, undefined],
    ["the hub name elsewhere", { workspaceFile: file(`/elsewhere/hub/${HUB_FILE}`) }, undefined],
    ["a lookalike dir prefix", { workspaceFile: file(`${DIR}-evil/${HUB_FILE}`) }, undefined],
    ["another scheme", { workspaceFile: file(`${DIR}/${HUB_FILE}`, "untitled") }, undefined],
    ["an empty window", {}, undefined],
  ])("%s", (_n, w, want) => {
    expect(windowKind(w, HD, "linux")).toEqual(want);
  });
  it("matches a case-folded hub dir on darwin and win32, not on linux", () => {
    const w = { workspaceFile: file(`/Users/Ada/Code/hub/${HUB_FILE}`) };
    expect(windowKind(w, { scheme: "file", path: "/users/ada/code/hub/" }, "darwin")).toEqual({ kind: "hub" });
    expect(windowKind(w, { scheme: "file", path: "/users/ada/code/hub/" }, "linux")).toBeUndefined();
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
