import { mkdtemp, readFile, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import * as path from "node:path";
import { describe, expect, it, vi } from "vitest";
import * as vscode from "vscode";
import { SignedOutError } from "../src/api";
import { activate, deactivate } from "../src/extension";
import { CONNECTED_BEAT_MS } from "../src/open";
import type { ExtToPane, PaneState } from "../../app/src/bridge/protocol.js" with { "resolution-mode": "import" };
import { fakeContext, fakeWebviewView, mock } from "./vscode";

const JAM = "https://jam.example.com";
const SES = "ses_0abcdefghjkmnpqrstvwxyz012";
const CHN = "chn_0abcdefghjkmnpqrstvwxyz012";
const remoteFolder = () => {
  (vscode.workspace as { workspaceFolders: unknown }).workspaceFolders = [{ uri: { scheme: "vscode-remote", authority: `ssh-remote+jam-${SES}` } }];
};
const openFolderCalls = () => vi.mocked(vscode.commands.executeCommand).mock.calls.filter((c) => c[0] === "vscode.openFolder");
const stored = (token: string) => JSON.stringify({ url: JAM, token, user: "u", expiresAt: "x" });
type View = ReturnType<typeof fakeWebviewView>;
const openView = (): View => {
  const v = fakeWebviewView();
  void mock.viewProviders.get("jam.status")!.resolveWebviewView(v as unknown as vscode.WebviewView, {} as vscode.WebviewViewResolveContext, {} as vscode.CancellationToken);
  return v;
};
const hubWindow = (ctx: vscode.ExtensionContext) => {
  (vscode.workspace as { workspaceFile: unknown }).workspaceFile = { scheme: "file", authority: "", path: `${ctx.globalStorageUri.path}/hub/Jam Hub (jam.example.com).code-workspace` };
};
const openRail = () => {
  const v = fakeWebviewView() as View & { badge?: unknown };
  void mock.viewProviders.get("jam.rail")!.resolveWebviewView(v as unknown as vscode.WebviewView, {} as vscode.WebviewViewResolveContext, {} as vscode.CancellationToken);
  return v;
};
const lastState = (v: View) =>
  (v.posted as ExtToPane[]).filter((m) => m.type === "state").map((m) => (m as { state: PaneState }).state).at(-1);
const settle = () => new Promise((r) => setTimeout(r, 20));
type Call = { method: string; path: string; auth: string; body: string };
// jamFetch answers like a Jam where the token "t" is revoked and "t2" is good.
function jamFetch(calls: Call[]) {
  return vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const u = new URL(String(input));
    const auth = ((init?.headers ?? {}) as Record<string, string>).Authorization ?? "";
    calls.push({ method: init?.method ?? "GET", path: u.pathname, auth, body: typeof init?.body === "string" ? init.body : "" });
    if (u.pathname === "/app/auth/app-token/exchange") return Response.json({ token: "t2", user: "u", expires_at: "x" });
    if (auth === "Bearer t") return new Response("", { status: 401 });
    if (u.pathname === "/app/user/events") return new Response(null, { status: 204 });
    if (u.pathname === `/app/user/api/sessions/${SES}`) return Response.json({ label: "alpha", channel: CHN });
    if (u.pathname === "/app/user/push/focus") return new Response(null, { status: 204 });
    if (u.pathname === "/app/user/api/rail") return Response.json({ groups: [] });
    if (u.pathname === `/app/user/api/conversations/${CHN}`) return Response.json({ session: SES, label: "alpha" });
    return Response.json({});
  });
}

describe("activate", () => {
  it("activate registers five view providers and seven commands, and no tree view", () => {
    mock.reset();
    const ctx = fakeContext();
    activate(ctx);
    expect(vscode.commands.registerCommand).toHaveBeenCalledTimes(7);
    expect(vscode.window.createTreeView).not.toHaveBeenCalled();
    expect(vscode.window.registerUriHandler).toHaveBeenCalledTimes(1);
    expect(vscode.window.registerWebviewViewProvider).toHaveBeenCalledTimes(5);
    const calls = vi.mocked(vscode.window.registerWebviewViewProvider).mock.calls as unknown as [string, unknown, { webviewOptions?: { retainContextWhenHidden?: boolean } } | undefined][];
    expect(calls.map((c) => c[0])).toEqual(["jam.rail", "jam.thinking", "jam.previews", "jam.status", "jam.artifacts"]);
    for (const [, , opts] of calls) expect(opts?.webviewOptions?.retainContextWhenHidden).toBeUndefined();
    deactivate();
  });

  it("the badge shows the needs-you count after activation and follows a changed event", async () => {
    mock.reset();
    const answers = [2, 1];
    let push: (chunk: string) => void = () => {};
    const fetchSpy = vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const u = new URL(String(input));
      if (u.pathname === "/app/user/events") {
        const body = new ReadableStream<Uint8Array>({
          start(c) {
            push = (chunk) => c.enqueue(new TextEncoder().encode(chunk));
          },
        });
        return new Response(body, { status: 200, headers: { "content-type": "text/event-stream" } });
      }
      if (u.pathname === "/app/user/api/rail") {
        const n = answers.length > 1 ? answers.shift()! : answers[0]!;
        const rows = Array.from({ length: n }, (_, i) => ({ id: `chn_${i}`, label: `l${i}`, project: "p", kind: "session", needsYou: true, mission: "" }));
        return Response.json({ groups: [{ bucket: "needs-you", title: "Needs you", rows }], unreadTotal: 0 });
      }
      return Response.json({});
    });
    try {
      mock.config["jam.url"] = JAM;
      const ctx = fakeContext();
      ctx.secretValues.set("jam.session", stored("t2"));
      activate(ctx);
      const railView = openRail();
      const badge = () => railView.badge;
      await vi.waitFor(() => expect(badge()).toEqual({ value: 2, tooltip: "2 conversations need you" }), { timeout: 6000 });
      push("event: changed\ndata: {}\n\n");
      await vi.waitFor(() => expect(badge()).toEqual({ value: 1, tooltip: "1 conversation needs you" }), { timeout: 6000 });
    } finally {
      fetchSpy.mockRestore();
      deactivate();
    }
  }, 20000);

  it("the badge is fetched at once on activation and cleared at once on sign-out", async () => {
    mock.reset();
    const fetchSpy = vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const u = new URL(String(input));
      if (u.pathname === "/app/user/events") return new Response(null, { status: 204 });
      if (u.pathname === "/app/user/api/rail") {
        const rows = [{ id: "chn_0", label: "l0", project: "p", kind: "session", needsYou: true, mission: "" }];
        return Response.json({ groups: [{ bucket: "needs-you", title: "Needs you", rows }], unreadTotal: 0 });
      }
      return Response.json({});
    });
    try {
      mock.config["jam.url"] = JAM;
      const ctx = fakeContext();
      ctx.secretValues.set("jam.session", stored("t2"));
      activate(ctx);
      const railView = openRail();
      const badge = () => railView.badge;
      // Well inside NEEDS_YOU_DEBOUNCE_MS: no debounce on activation or sign-out.
      await vi.waitFor(() => expect(badge()).toEqual({ value: 1, tooltip: "1 conversation needs you" }), { timeout: 1000 });
      await mock.commands.get("jam.signOut")!();
      await vi.waitFor(() => expect(badge()).toBeUndefined(), { timeout: 1000 });
    } finally {
      fetchSpy.mockRestore();
      deactivate();
    }
  });

  it("a rail click on a session with a window focuses it; without one goes to the hub", async () => {
    mock.reset();
    const home = await mkdtemp(path.join(tmpdir(), "jamext-"));
    const fsp = await import("node:fs/promises");
    await fsp.mkdir(path.join(home, ".ssh"));
    await writeFile(path.join(home, ".ssh", "config"), `Host jam-${SES}\n  HostName x\n`);
    vi.stubEnv("HOME", home);
    vi.stubEnv("XDG_CONFIG_HOME", home);
    const getExt = vi.mocked(vscode.extensions.getExtension);
    getExt.mockImplementation(() => ({}) as vscode.Extension<unknown>);
    const fetchSpy = jamFetch([]);
    try {
      mock.config["jam.url"] = JAM;
      mock.config["remote.SSH.localServerDownload"] = "always";
      const ctx = fakeContext();
      ctx.secretValues.set("jam.session", stored("t2"));
      activate(ctx);
      const rail = openRail();
      const pending = async () => JSON.parse(await readFile(`${ctx.globalStorageUri.path}/hub/pending-jam.example.com.json`, "utf8")).channel;
      const hubOpens = () => openFolderCalls().filter((c) => (c[1] as { fsPath?: string }).fsPath?.endsWith("Jam Hub (jam.example.com).code-workspace"));
      rail.receive({ type: "open", intent: { kind: "go", channel: CHN, session: SES } });
      await vi.waitFor(() => expect(hubOpens()).toHaveLength(1));
      expect(await pending()).toBe(CHN);
      rail.receive({ type: "open", intent: { kind: "go", channel: "legacy:dm:ann", session: "" } });
      await vi.waitFor(() => expect(hubOpens()).toHaveLength(2));
      expect(await pending()).toBe("legacy:dm:ann");
      ctx.state.set(`jam.connected.${SES}`, Date.now() - 91_000);
      rail.receive({ type: "open", intent: { kind: "go", channel: CHN, session: SES } });
      await vi.waitFor(() => expect(hubOpens()).toHaveLength(3));
      ctx.state.set(`jam.connected.${SES}`, Date.now());
      rail.receive({ type: "open", intent: { kind: "go", channel: CHN, session: SES } });
      await vi.waitFor(() => expect(openFolderCalls()).toHaveLength(4));
      expect(openFolderCalls()[3]).toEqual(["vscode.openFolder", expect.objectContaining({ authority: `ssh-remote+jam-${SES}` }), { forceNewWindow: true }]);
      expect(vscode.env.openExternal).not.toHaveBeenCalled();
    } finally {
      getExt.mockImplementation(() => undefined);
      fetchSpy.mockRestore();
      vi.unstubAllEnvs();
      deactivate();
    }
  });

  it("a session pane's open board message creates a board panel", async () => {
    mock.reset();
    const BRD = "brd_0abcdefghjkmnpqrstvwxyz012";
    mock.config["jam.url"] = JAM;
    activate(fakeContext());
    expect(vscode.window.registerWebviewPanelSerializer).toHaveBeenCalledWith("jam.board", expect.anything());
    const v = openView();
    v.receive({ type: "open", intent: { kind: "board", id: BRD } });
    await vi.waitFor(() => expect(mock.panels).toHaveLength(1));
    expect(mock.panels[0]!.viewType).toBe("jam.board");
    expect(mock.panels[0]!.webview.html).toContain(`data-board="${BRD}"`);
    v.receive({ type: "open", intent: { kind: "board", id: BRD } });
    await settle();
    expect(mock.panels).toHaveLength(1);
    deactivate();
  });

  it("a revoked token: the stream's and a request's 401 sign out once, every pane shows Sign in, and Sign In restarts the stream and reloads the panes (Review Focus 2)", async () => {
    mock.reset();
    const home = await mkdtemp(path.join(tmpdir(), "jamext-"));
    vi.stubEnv("XDG_CONFIG_HOME", home);
    const calls: Call[] = [];
    const fetchSpy = jamFetch(calls);
    try {
      mock.config["jam.url"] = JAM;
      remoteFolder();
      const ctx = fakeContext();
      ctx.secretValues.set("jam.session", stored("t"));
      activate(ctx);
      const a = openView();
      const b = openView();
      for (const v of [a, b]) v.receive({ type: "ready", pane: "status" });
      a.receive({ type: "request", id: 1, req: { method: "GET", path: "/app/user/api/bootstrap" } });
      await vi.waitFor(() => expect(a.posted).toContainEqual({ type: "response", id: 1, res: { kind: "signedOut" } }));
      await vi.waitFor(() => expect(calls.some((c) => c.path === "/app/user/events" && c.auth === "Bearer t")).toBe(true));
      for (const v of [a, b]) await vi.waitFor(() => expect(lastState(v)).toMatchObject({ signedIn: false, target: null }));
      await settle();
      const warned = vi.mocked(vscode.window.showWarningMessage).mock.calls.filter((c) => c[0] === "Your Jam sign-in expired or was revoked.");
      expect(warned).toHaveLength(1);

      const signingIn = mock.commands.get("jam.signIn")!();
      await vi.waitFor(() => expect(vscode.env.openExternal).toHaveBeenCalled());
      const consent = new URL(String(vi.mocked(vscode.env.openExternal).mock.calls.at(-1)![0]));
      mock.uriHandler!.handleUri({ path: "/auth", query: `state=${consent.searchParams.get("state")}&code=c` });
      await signingIn;
      await vi.waitFor(() => expect(calls.some((c) => c.path === "/app/user/events" && c.auth === "Bearer t2")).toBe(true));
      for (const v of [a, b]) {
        await vi.waitFor(() => expect(lastState(v)).toEqual({ signedIn: true, target: { session: SES, label: "alpha", channel: CHN } }));
      }
      for (const v of [a, b]) expect(JSON.stringify(v.posted)).not.toMatch(/"t2?"|Bearer|jam\.example/);
    } finally {
      fetchSpy.mockRestore();
      vi.unstubAllEnvs();
      deactivate();
    }
  });

  it("push focus follows the window's focus, from startup, and clears on deactivate", async () => {
    mock.reset();
    const calls: Call[] = [];
    const fetchSpy = jamFetch(calls);
    const focusPosts = () => calls.filter((c) => c.path === "/app/user/push/focus").map((c) => [c.auth, JSON.parse(c.body).channel]);
    try {
      mock.config["jam.url"] = JAM;
      (vscode.window.state as { focused: boolean }).focused = false;
      const ctx = fakeContext();
      ctx.secretValues.set("jam.session", stored("t2"));
      hubWindow(ctx);
      activate(ctx);
      openRail().receive({ type: "open", intent: { kind: "go", channel: CHN, session: "" } });
      await vi.waitFor(() => expect(mock.panels).toHaveLength(1));
      const v = mock.panels[0]!;
      v.setActive(true);
      v.receive({ type: "viewing", channel: CHN });
      await settle();
      expect(focusPosts()).toEqual([]);
      for (const cb of mock.windowState) cb({ focused: true });
      await vi.waitFor(() => expect(focusPosts()).toEqual([["Bearer t2", CHN]]));
      for (const cb of mock.windowState) cb({ focused: false });
      await vi.waitFor(() => expect(focusPosts()).toEqual([["Bearer t2", CHN], ["Bearer t2", null]]));
      for (const cb of mock.windowState) cb({ focused: true });
      await vi.waitFor(() => expect(focusPosts()).toHaveLength(3));
      deactivate();
      await vi.waitFor(() => expect(focusPosts().at(-1)).toEqual(["Bearer t2", null]));
    } finally {
      fetchSpy.mockRestore();
    }
  });

  it("Sign In without jam.url shows the error", async () => {
    mock.reset();
    activate(fakeContext());
    await mock.commands.get("jam.signIn")!();
    expect(vscode.window.showErrorMessage).toHaveBeenCalledWith("Set jam.url to your Jam's address");
  });

  it("a SignedOutError clears the secret and the token file and offers Sign In", async () => {
    mock.reset();
    const home = await mkdtemp(path.join(tmpdir(), "jamext-"));
    vi.stubEnv("XDG_CONFIG_HOME", home);
    try {
      const file = path.join(home, "jam", "default-app-token.json");
      await (await import("node:fs/promises")).mkdir(path.dirname(file), { recursive: true });
      await writeFile(file, "{}");
      const ctx = fakeContext();
      ctx.secretValues.set("jam.session", "{}");
      activate(ctx);
      // jam.signIn raises a SignedOutError via a failing withProgress.
      vi.mocked(vscode.window.withProgress).mockRejectedValueOnce(new SignedOutError());
      mock.config["jam.url"] = "https://jam.example.com";
      mock.answers = ["Sign In"];
      await mock.commands.get("jam.signIn")!();
      expect(ctx.secretValues.has("jam.session")).toBe(false);
      await expect(stat(file)).rejects.toThrow();
      expect(vscode.window.showWarningMessage).toHaveBeenCalledWith("Your Jam sign-in expired or was revoked.", "Sign In");
      expect(vscode.commands.executeCommand).toHaveBeenCalledWith("jam.signIn");
    } finally {
      vi.unstubAllEnvs();
    }
  });

  it("a crafted session link is refused with no API call", async () => {
    mock.reset();
    const fetchSpy = vi.spyOn(globalThis, "fetch");
    activate(fakeContext());
    mock.config["jam.url"] = "https://jam.example.com";
    for (const path of ["/session/ses_x%0AHost%20*", "/session/../../x", "/other"]) {
      mock.uriHandler!.handleUri({ path, query: "" });
    }
    await new Promise((r) => setTimeout(r, 10));
    expect(fetchSpy).not.toHaveBeenCalled();
    expect(vscode.window.showWarningMessage).toHaveBeenCalledTimes(3);
    fetchSpy.mockRestore();
  });

  it("a 401 while opening a session signs out and a new sign-in works", async () => {
    mock.reset();
    const home = await mkdtemp(path.join(tmpdir(), "jamext-"));
    vi.stubEnv("XDG_CONFIG_HOME", home);
    // The always-on events stream is off (204); every other request answers 401.
    const fetchSpy = vi
      .spyOn(globalThis, "fetch")
      .mockImplementation(async (input) => new Response(null, { status: new URL(String(input)).pathname === "/app/user/events" ? 204 : 401 }));
    // The Needs You view's activation fetch of the rail is not part of the open.
    const nonEvents = () => fetchSpy.mock.calls.filter((c) => !["/app/user/events", "/app/user/api/rail"].includes(new URL(String(c[0])).pathname));
    try {
      const file = path.join(home, "jam", "default-app-token.json");
      await (await import("node:fs/promises")).mkdir(path.dirname(file), { recursive: true });
      await writeFile(file, "{}");
      const ctx = fakeContext();
      mock.config["jam.url"] = "https://jam.example.com";
      ctx.secretValues.set("jam.session", JSON.stringify({ url: "https://jam.example.com", token: "t", user: "u", expiresAt: "x" }));
      activate(ctx);
      mock.answers = ["Sign In"];
      mock.uriHandler!.handleUri({ path: "/session/ses_0abcdefghjkmnpqrstvwxyz012", query: "" });
      await vi.waitFor(() => expect(vscode.commands.executeCommand).toHaveBeenCalledWith("jam.signIn"));
      expect(ctx.secretValues.has("jam.session")).toBe(false);
      await expect(stat(file)).rejects.toThrow();
      expect(nonEvents()).toHaveLength(1);
    } finally {
      fetchSpy.mockRestore();
      vi.unstubAllEnvs();
    }
  });

  it("Forget Host Key in a remote window uses the workspace folder's authority", async () => {
    mock.reset();
    const home = await mkdtemp(path.join(tmpdir(), "jamext-"));
    const id = "ses_0abcdefghjkmnpqrstvwxyz012";
    const bin = path.join(home, "bin");
    const out = path.join(home, "args");
    const fsp = await import("node:fs/promises");
    await fsp.mkdir(bin);
    await fsp.mkdir(path.join(home, ".ssh"));
    await writeFile(path.join(home, ".ssh", "known_hosts"), "x\n");
    await writeFile(path.join(bin, "ssh-keygen"), `#!/bin/sh\necho "$@" > ${out}\n`, { mode: 0o755 });
    vi.stubEnv("HOME", home);
    vi.stubEnv("PATH", bin);
    try {
      (vscode.workspace as { workspaceFolders: unknown }).workspaceFolders = [{ uri: { scheme: "vscode-remote", authority: `ssh-remote+jam-${id}` } }];
      activate(fakeContext());
      mock.answers = ["Forget"];
      await mock.commands.get("jam.forgetHostKey")!();
      expect(vscode.window.showErrorMessage).not.toHaveBeenCalled();
      expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(expect.stringContaining(`jam-${id}?`), "Forget");
      expect((await fsp.readFile(out, "utf8")).trim()).toBe(`-R jam-${id} -f ${path.join(home, ".ssh", "known_hosts")}`);
    } finally {
      vi.unstubAllEnvs();
    }
  });

  it("opening a session re-syncs a missing token file from the stored session", async () => {
    mock.reset();
    const home = await mkdtemp(path.join(tmpdir(), "jamext-"));
    vi.stubEnv("XDG_CONFIG_HOME", home);
    const fetchSpy = vi.spyOn(globalThis, "fetch").mockRejectedValue(new Error("offline"));
    try {
      const file = path.join(home, "jam", "default-app-token.json");
      const ctx = fakeContext();
      mock.config["jam.url"] = "https://jam.example.com";
      ctx.secretValues.set("jam.session", JSON.stringify({ url: "https://jam.example.com", token: "t", user: "u", expiresAt: "x" }));
      activate(ctx);
      mock.uriHandler!.handleUri({ path: "/session/ses_0abcdefghjkmnpqrstvwxyz012", query: "" });
      await vi.waitFor(() => expect(vscode.window.showErrorMessage).toHaveBeenCalledWith("offline"));
      expect(JSON.parse(await readFile(file, "utf8"))).toEqual({ token: "t", jam_url: "https://jam.example.com" });
      expect((await stat(file)).mode & 0o777).toBe(0o600);
    } finally {
      fetchSpy.mockRestore();
      vi.unstubAllEnvs();
    }
  });

  it("an open that read the session before a sign-out doesn't bring the token file back", async () => {
    mock.reset();
    const home = await mkdtemp(path.join(tmpdir(), "jamext-"));
    vi.stubEnv("XDG_CONFIG_HOME", home);
    const fetchSpy = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response("", { status: 401 }));
    try {
      const file = path.join(home, "jam", "default-app-token.json");
      const ctx = fakeContext();
      mock.config["jam.url"] = JAM;
      ctx.secretValues.set("jam.session", stored("t"));
      activate(ctx);
      // The stream's 401 signs out: the secret and the token file go.
      await vi.waitFor(() => expect(vscode.window.showWarningMessage).toHaveBeenCalledWith("Your Jam sign-in expired or was revoked.", "Sign In"));
      expect(ctx.secretValues.has("jam.session")).toBe(false);
      await expect(stat(file)).rejects.toThrow();
      // A cold-start open whose session read raced the sign-out still holds it.
      ctx.secrets.get = async (k: string) => (k === "jam.session" ? stored("t") : undefined);
      const before = fetchSpy.mock.calls.length;
      mock.uriHandler!.handleUri({ path: `/session/${SES}`, query: "" });
      await vi.waitFor(() => expect(fetchSpy.mock.calls.slice(before).some((c) => new URL(String(c[0])).pathname === `/app/user/api/sessions/${SES}`)).toBe(true));
      await settle();
      await expect(stat(file)).rejects.toThrow();
    } finally {
      fetchSpy.mockRestore();
      vi.unstubAllEnvs();
      deactivate();
    }
  });

  it("a failing sign-out after a 401 is shown, not lost", async () => {
    mock.reset();
    const home = await mkdtemp(path.join(tmpdir(), "jamext-"));
    vi.stubEnv("XDG_CONFIG_HOME", home);
    const fetchSpy = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response("", { status: 401 }));
    try {
      const file = path.join(home, "jam", "default-app-token.json");
      const ctx = fakeContext();
      mock.config["jam.url"] = "https://jam.example.com";
      ctx.secretValues.set("jam.session", JSON.stringify({ url: "https://jam.example.com", token: "t", user: "u", expiresAt: "x" }));
      ctx.secrets.delete = async () => {
        throw new Error("keyring locked");
      };
      activate(ctx);
      mock.uriHandler!.handleUri({ path: "/session/ses_0abcdefghjkmnpqrstvwxyz012", query: "" });
      await vi.waitFor(() => expect(vscode.window.showErrorMessage).toHaveBeenCalledWith("keyring locked"));
      await expect(stat(file)).rejects.toThrow();
    } finally {
      fetchSpy.mockRestore();
      vi.unstubAllEnvs();
    }
  });

  it("a late 401 for an old token doesn't sign out a newer sign-in", async () => {
    mock.reset();
    const home = await mkdtemp(path.join(tmpdir(), "jamext-"));
    vi.stubEnv("XDG_CONFIG_HOME", home);
    const calls: Call[] = [];
    const base = jamFetch(calls).getMockImplementation()!;
    let releaseEvents: () => void = () => {};
    const eventsHeld = new Promise<void>((r) => (releaseEvents = r));
    let releaseUpload: () => void = () => {};
    const uploadHeld = new Promise<void>((r) => (releaseUpload = r));
    const fetchSpy = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const u = new URL(String(input));
      const auth = ((init?.headers ?? {}) as Record<string, string>).Authorization ?? "";
      if (auth === "Bearer t" && u.pathname === "/app/user/events") await eventsHeld;
      if (auth === "Bearer t" && u.pathname === "/app/user/uploads") {
        calls.push({ method: "POST", path: u.pathname, auth, body: "" });
        await uploadHeld;
        return new Response("", { status: 401 });
      }
      return base(input, init);
    });
    try {
      mock.config["jam.url"] = JAM;
      remoteFolder();
      const ctx = fakeContext();
      ctx.secretValues.set("jam.session", stored("t"));
      activate(ctx);
      const a = openView();
      a.receive({ type: "ready", pane: "conversation" });
      a.receive({ type: "request", id: 1, req: { method: "POST", path: "/app/user/uploads", bytes: new ArrayBuffer(4) } });
      await vi.waitFor(() => expect(calls.some((c) => c.path === "/app/user/uploads" && c.auth === "Bearer t")).toBe(true));
      // the stream's 401 signs t out
      releaseEvents();
      await vi.waitFor(() => expect(ctx.secretValues.has("jam.session")).toBe(false));
      // sign in again: t2
      const signingIn = mock.commands.get("jam.signIn")!();
      await vi.waitFor(() => expect(vscode.env.openExternal).toHaveBeenCalled());
      const consent = new URL(String(vi.mocked(vscode.env.openExternal).mock.calls.at(-1)![0]));
      mock.uriHandler!.handleUri({ path: "/auth", query: `state=${consent.searchParams.get("state")}&code=c` });
      await signingIn;
      await vi.waitFor(() => expect(ctx.secretValues.get("jam.session")).toContain('"t2"'));
      // the upload sent with t now answers 401: t2 stays
      releaseUpload();
      await vi.waitFor(() => expect(a.posted).toContainEqual({ type: "response", id: 1, res: { kind: "signedOut" } }));
      await settle();
      expect(ctx.secretValues.get("jam.session")).toContain('"t2"');
      const warned = vi.mocked(vscode.window.showWarningMessage).mock.calls.filter((c) => c[0] === "Your Jam sign-in expired or was revoked.");
      expect(warned).toHaveLength(1);
    } finally {
      fetchSpy.mockRestore();
      vi.unstubAllEnvs();
      deactivate();
    }
  });

  it("a command's 401 and the stream's 401 sign out once between them", async () => {
    mock.reset();
    const home = await mkdtemp(path.join(tmpdir(), "jamext-"));
    vi.stubEnv("XDG_CONFIG_HOME", home);
    const held: (() => void)[] = [];
    const fetchSpy = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const u = new URL(String(input));
      const auth = ((init?.headers ?? {}) as Record<string, string>).Authorization ?? "";
      if (auth === "Bearer t" && u.pathname !== "/app/user/events") await new Promise<void>((r) => held.push(r));
      return new Response("", { status: 401 });
    });
    try {
      mock.config["jam.url"] = JAM;
      const ctx = fakeContext();
      ctx.secretValues.set("jam.session", stored("t"));
      activate(ctx);
      const focusing = mock.commands.get("jam.openSession")!();
      await vi.waitFor(() => expect(held.length).toBeGreaterThan(0));
      openView();
      await vi.waitFor(() => expect(ctx.secretValues.has("jam.session")).toBe(false));
      for (const r of held) r();
      await focusing;
      await settle();
      const warned = vi.mocked(vscode.window.showWarningMessage).mock.calls.filter((c) => c[0] === "Your Jam sign-in expired or was revoked.");
      expect(warned).toHaveLength(1);
    } finally {
      fetchSpy.mockRestore();
      vi.unstubAllEnvs();
      deactivate();
    }
  });
});

describe("notifications", () => {
  const NOTE = { title: "Alice · build-bot", body: "done?", channel: CHN, id: "sqk_1", unread: 1, kind: "messages", session: SES };
  type Fetched = { url: string; auth: string };
  // notifyFetch is jamFetch, but /app/user/events opens a stream that sends one
  // notify event and stays open until aborted.
  function notifyFetch(calls: Call[], fetched: Fetched[]) {
    const base = jamFetch(calls).getMockImplementation()!;
    return vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const u = new URL(String(input));
      const auth = ((init?.headers ?? {}) as Record<string, string>).Authorization ?? "";
      fetched.push({ url: u.toString(), auth });
      if (u.pathname !== "/app/user/events" || auth !== "Bearer t2") return base(input, init);
      const body = new ReadableStream<Uint8Array>({
        start(c) {
          c.enqueue(new TextEncoder().encode(`event: notify\ndata: ${JSON.stringify(NOTE)}\n\n`));
          init?.signal?.addEventListener("abort", () => c.error(new Error("aborted")));
        },
      });
      return new Response(body, { status: 200, headers: { "Content-Type": "text/event-stream" } });
    });
  }
  const events = (f: Fetched[]) => f.filter((c) => new URL(c.url).pathname === "/app/user/events");
  const toasts = () => vi.mocked(vscode.window.showInformationMessage).mock.calls.filter((c) => c[0] === "Alice · build-bot: done?");

  it("activate holds the events stream with no pane open", async () => {
    mock.reset();
    const calls: Call[] = [];
    const fetchSpy = jamFetch(calls);
    try {
      mock.config["jam.url"] = JAM;
      const ctx = fakeContext();
      ctx.secretValues.set("jam.session", stored("t2"));
      activate(ctx);
      await vi.waitFor(() => expect(calls.filter((c) => c.path === "/app/user/events")).toEqual([{ method: "GET", path: "/app/user/events", auth: "Bearer t2", body: "" }]));
      await settle();
      expect(calls.filter((c) => c.path === "/app/user/events")).toHaveLength(1);
    } finally {
      fetchSpy.mockRestore();
      deactivate();
    }
  });

  it("a notify event on the stream shows a toast; Open goes to the session's open window", async () => {
    mock.reset();
    const home = await mkdtemp(path.join(tmpdir(), "jamext-"));
    const fsp = await import("node:fs/promises");
    await fsp.mkdir(path.join(home, ".ssh"));
    await writeFile(path.join(home, ".ssh", "config"), `Host jam-${SES}\n  HostName x\n`);
    vi.stubEnv("HOME", home);
    vi.stubEnv("XDG_CONFIG_HOME", home);
    const getExt = vi.mocked(vscode.extensions.getExtension);
    getExt.mockImplementation(() => ({}) as vscode.Extension<unknown>);
    const calls: Call[] = [];
    const fetchSpy = notifyFetch(calls, []);
    try {
      mock.config["jam.url"] = JAM;
      mock.config["remote.SSH.localServerDownload"] = "always";
      mock.answers = ["Open"];
      const ctx = fakeContext();
      ctx.secretValues.set("jam.session", stored("t2"));
      ctx.state.set(`jam.connected.${SES}`, Date.now());
      activate(ctx);
      await vi.waitFor(() => expect(toasts()).toEqual([["Alice · build-bot: done?", "Open"]]));
      await vi.waitFor(() =>
        expect(vscode.commands.executeCommand).toHaveBeenCalledWith("vscode.openFolder", expect.objectContaining({ authority: `ssh-remote+jam-${SES}` }), { forceNewWindow: true }),
      );
      expect(mock.output.join("\n")).not.toContain("done?");
    } finally {
      getExt.mockImplementation(() => undefined);
      fetchSpy.mockRestore();
      vi.unstubAllEnvs();
      deactivate();
    }
  });

  it("two activations on one global storage dir show one toast for one event (Review Focus 1)", async () => {
    mock.reset();
    const storage = await mkdtemp(path.join(tmpdir(), "jamstore-"));
    const fetched: Fetched[] = [];
    const fetchSpy = notifyFetch([], fetched);
    try {
      mock.config["jam.url"] = JAM;
      for (const ctx of [fakeContext(storage), fakeContext(storage)]) {
        ctx.secretValues.set("jam.session", stored("t2"));
        activate(ctx);
      }
      await vi.waitFor(() => expect(events(fetched)).toHaveLength(2));
      await vi.waitFor(() => expect(toasts()).toHaveLength(1));
      await new Promise((r) => setTimeout(r, 300));
      expect(toasts()).toHaveLength(1);
    } finally {
      fetchSpy.mockRestore();
      deactivate();
    }
  });

  it("signed out: no stream, no toast", async () => {
    mock.reset();
    const fetched: Fetched[] = [];
    const fetchSpy = notifyFetch([], fetched);
    try {
      mock.config["jam.url"] = JAM;
      activate(fakeContext());
      await settle();
      expect(events(fetched)).toEqual([]);
      expect(toasts()).toEqual([]);
    } finally {
      fetchSpy.mockRestore();
      deactivate();
    }
  });

  it("changing jam.url restarts the events stream", async () => {
    mock.reset();
    const JAM2 = "https://jam2.example.com";
    const fetched: Fetched[] = [];
    const fetchSpy = notifyFetch([], fetched);
    try {
      mock.config["jam.url"] = JAM;
      const ctx = fakeContext();
      ctx.secretValues.set("jam.session", stored("t2"));
      activate(ctx);
      await vi.waitFor(() => expect(events(fetched).map((c) => c.url)).toEqual([`${JAM}/app/user/events`]));
      ctx.secretValues.set("jam.session", JSON.stringify({ url: JAM2, token: "t3", user: "u", expiresAt: "x" }));
      mock.config["jam.url"] = JAM2;
      mock.changeConfig("jam.cliPath");
      await settle();
      expect(events(fetched)).toHaveLength(1);
      mock.changeConfig("jam.url");
      await vi.waitFor(() => expect(events(fetched).at(-1)).toEqual({ url: `${JAM2}/app/user/events`, auth: "Bearer t3" }));
    } finally {
      fetchSpy.mockRestore();
      deactivate();
    }
  });
});

describe("session windows", () => {
  // sshHome makes ensureSsh find a hand-written Host block, so it succeeds with no CLI.
  const sshHome = async () => {
    const home = await mkdtemp(path.join(tmpdir(), "jamext-"));
    const fsp = await import("node:fs/promises");
    await fsp.mkdir(path.join(home, ".ssh"));
    await writeFile(path.join(home, ".ssh", "config"), `Host jam-${SES}\n  HostName x\n`);
    vi.stubEnv("HOME", home);
    vi.stubEnv("XDG_CONFIG_HOME", home);
  };
  const OTHER = "ses_1abcdefghjkmnpqrstvwxyz012";

  it("a link sets up SSH and opens the studio window in a new window; nothing goes under sessions/", async () => {
    mock.reset();
    await sshHome();
    const getExt = vi.mocked(vscode.extensions.getExtension);
    getExt.mockImplementation(() => ({}) as vscode.Extension<unknown>);
    const fetchSpy = jamFetch([]);
    try {
      mock.config["jam.url"] = JAM;
      mock.config["remote.SSH.localServerDownload"] = "always";
      const ctx = fakeContext();
      ctx.secretValues.set("jam.session", stored("t2"));
      activate(ctx);
      mock.uriHandler!.handleUri({ path: `/session/${SES}`, query: "" });
      await vi.waitFor(() => expect(openFolderCalls()).toHaveLength(1));
      expect(openFolderCalls()[0]).toEqual(["vscode.openFolder", expect.objectContaining({ authority: `ssh-remote+jam-${SES}` }), { forceNewWindow: true }]);
      expect(mock.output.join("\n")).toContain(`[${SES}] ssh ready: alias jam-${SES}`);
      expect(mock.output.join("\n")).not.toMatch(/t2|Bearer/);
      await expect(stat(path.join(ctx.globalStorageUri.fsPath, "sessions"))).rejects.toThrow();
    } finally {
      getExt.mockImplementation(() => undefined);
      fetchSpy.mockRestore();
      vi.unstubAllEnvs();
      deactivate();
    }
  });

  it("a studio window sets jam.window to session and its views show its session, even with stale focus and pins", async () => {
    mock.reset();
    const fetchSpy = jamFetch([]);
    try {
      mock.config["jam.url"] = JAM;
      const ctx = fakeContext();
      ctx.secretValues.set("jam.session", stored("t2"));
      await ctx.workspaceState.update("jam.focus", OTHER);
      await ctx.workspaceState.update("jam.pins", { "jam.conversation": OTHER });
      remoteFolder();
      activate(ctx);
      expect(mock.context.get("jam.window")).toBe("session");
      expect(vscode.window.createStatusBarItem).not.toHaveBeenCalled();
      const v = openView();
      v.receive({ type: "ready", pane: "conversation" });
      await vi.waitFor(() => expect(lastState(v)).toEqual({ signedIn: true, target: { session: SES, label: "alpha", channel: CHN } }));
      expect(mock.output.join("\n")).toContain(`window: studio session ${SES}`);
    } finally {
      fetchSpy.mockRestore();
      deactivate();
    }
  });

  it("a window on an old local session workspace is an ordinary window", () => {
    mock.reset();
    const ctx = fakeContext();
    (vscode.workspace as { workspaceFile: unknown }).workspaceFile = { scheme: "file", authority: "", path: `${ctx.globalStorageUri.path}/sessions/${SES}.code-workspace` };
    expect(() => activate(ctx)).not.toThrow();
    expect(mock.context.get("jam.window")).toBe("");
    expect(vscode.window.createStatusBarItem).not.toHaveBeenCalled();
    expect(mock.output.join("\n")).toContain("window: no session");
    deactivate();
  });

  it("the removed commands aren't registered", () => {
    mock.reset();
    activate(fakeContext());
    for (const id of ["jam.connectStudio", "jam.disconnectStudio", "jam.focusSession", "jam.conversation.pin", "jam.conversation.unpin"]) expect(mock.commands.has(id)).toBe(false);
    deactivate();
  });

  it("a studio window marks itself connected, refreshes it every beat, and clears it on deactivate", async () => {
    mock.reset();
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval", "Date"], now: 1_000_000 });
    try {
      const ctx = fakeContext();
      remoteFolder();
      activate(ctx);
      await vi.waitFor(() => expect(ctx.state.get(`jam.connected.${SES}`)).toBe(1_000_000));
      vi.advanceTimersByTime(CONNECTED_BEAT_MS);
      await vi.waitFor(() => expect(ctx.state.get(`jam.connected.${SES}`)).toBe(1_000_000 + CONNECTED_BEAT_MS));
      await deactivate();
      expect(ctx.state.get(`jam.connected.${SES}`)).toBeUndefined();
      vi.advanceTimersByTime(CONNECTED_BEAT_MS * 3);
      expect(ctx.state.get(`jam.connected.${SES}`)).toBeUndefined();
    } finally {
      vi.useRealTimers();
    }
  });

  it("a studio window opens its conversation tab once", async () => {
    mock.reset();
    const fetchSpy = jamFetch([]);
    try {
      mock.config["jam.url"] = JAM;
      const ctx = fakeContext();
      ctx.secretValues.set("jam.session", stored("t2"));
      remoteFolder();
      activate(ctx);
      await vi.waitFor(() => expect(mock.panels.map((p) => p.viewType)).toEqual(["jam.conversationTab"]));
      expect(mock.panels[0]!.webview.html).toContain(`data-channel="${CHN}"`);
      deactivate();
      mock.reset();
      remoteFolder();
      activate(ctx);
      await settle();
      expect(mock.panels).toEqual([]);
    } finally {
      fetchSpy.mockRestore();
      deactivate();
    }
  });

  it("in a studio window, a rail click on its own session reopens its conversation tab", async () => {
    mock.reset();
    const fetchSpy = jamFetch([]);
    try {
      mock.config["jam.url"] = JAM;
      const ctx = fakeContext();
      ctx.secretValues.set("jam.session", stored("t2"));
      await ctx.workspaceState.update("jam.conversationTabShown", true);
      remoteFolder();
      activate(ctx);
      await vi.waitFor(() => expect(ctx.state.get(`jam.connected.${SES}`)).toEqual(expect.any(Number)));
      openRail().receive({ type: "open", intent: { kind: "go", channel: CHN, session: SES } });
      await vi.waitFor(() => expect(mock.panels.map((p) => p.viewType)).toEqual(["jam.conversationTab"]));
      expect(openFolderCalls()).toEqual([]);
    } finally {
      fetchSpy.mockRestore();
      deactivate();
    }
  });

  it("an Artifacts ask reveals the conversation tab and scrolls", async () => {
    mock.reset();
    const fetchSpy = jamFetch([]);
    try {
      mock.config["jam.url"] = JAM;
      const ctx = fakeContext();
      ctx.secretValues.set("jam.session", stored("t2"));
      remoteFolder();
      activate(ctx);
      await vi.waitFor(() => expect(mock.panels).toHaveLength(1));
      const tab = mock.panels[0]!;
      tab.receive({ type: "ready", pane: "conversation" });
      const v = openView();
      v.receive({ type: "open", intent: { kind: "reveal", anchor: "ask-a1" } });
      await vi.waitFor(() => expect(tab.posted).toContainEqual({ type: "scrollTo", anchor: "ask-a1" }));
      expect(tab.revealed.length).toBeGreaterThan(0);
    } finally {
      fetchSpy.mockRestore();
      deactivate();
    }
  });

  it("an ordinary window leaves another window's connected mark alone", async () => {
    mock.reset();
    const ctx = fakeContext();
    ctx.state.set(`jam.connected.${SES}`, 1234);
    activate(ctx);
    await deactivate();
    expect(ctx.state.get(`jam.connected.${SES}`)).toBe(1234);
  });
});

describe("hub window", () => {
  const hubPath = (ctx: vscode.ExtensionContext) => `${ctx.globalStorageUri.path}/hub/Jam Hub (jam.example.com).code-workspace`;

  it("jam.openHub writes the hub file and opens it locally in a new window", async () => {
    mock.reset();
    mock.config["jam.url"] = JAM;
    const ctx = fakeContext();
    activate(ctx);
    await mock.commands.get("jam.openHub")!();
    expect(openFolderCalls()).toEqual([["vscode.openFolder", expect.objectContaining({ scheme: "file", fsPath: hubPath(ctx) }), { forceNewWindow: true, forceLocalWindow: true }]]);
    expect(JSON.parse(await readFile(hubPath(ctx), "utf8"))).toEqual({ folders: [] });
    await mock.commands.get("jam.openHub")!();
    expect(openFolderCalls()).toHaveLength(2);
    deactivate();
  });

  it("a window on the hub file sets jam.window to hub", () => {
    mock.reset();
    mock.config["jam.url"] = JAM;
    const ctx = fakeContext();
    (vscode.workspace as { workspaceFile: unknown }).workspaceFile = { scheme: "file", authority: "", path: hubPath(ctx) };
    activate(ctx);
    expect(mock.context.get("jam.window")).toBe("hub");
    expect(mock.output.join("\n")).toContain("window: hub");
    deactivate();
  });

  it("a rail click in an ordinary window writes pending and opens the hub", async () => {
    mock.reset();
    mock.config["jam.url"] = JAM;
    const fetchSpy = jamFetch([]);
    try {
      const ctx = fakeContext();
      ctx.secretValues.set("jam.session", stored("t2"));
      activate(ctx);
      openRail().receive({ type: "open", intent: { kind: "go", channel: CHN, session: SES } });
      await vi.waitFor(() => expect(openFolderCalls()).toHaveLength(1));
      expect(openFolderCalls()[0]![1]).toEqual(expect.objectContaining({ fsPath: hubPath(ctx) }));
      const pending = JSON.parse(await readFile(`${ctx.globalStorageUri.path}/hub/pending-jam.example.com.json`, "utf8"));
      expect(pending).toEqual({ channel: CHN, at: expect.any(Number) });
      expect(vscode.env.openExternal).not.toHaveBeenCalled();
    } finally {
      fetchSpy.mockRestore();
      deactivate();
    }
  });

  it("a hub window opens the pending conversation's tab on activation, and deletes the file", async () => {
    mock.reset();
    mock.config["jam.url"] = JAM;
    const fetchSpy = jamFetch([]);
    try {
      const ctx = fakeContext();
      ctx.secretValues.set("jam.session", stored("t2"));
      const fsp = await import("node:fs/promises");
      await fsp.mkdir(`${ctx.globalStorageUri.path}/hub`, { recursive: true });
      await writeFile(`${ctx.globalStorageUri.path}/hub/pending-jam.example.com.json`, JSON.stringify({ channel: CHN, at: Date.now() }));
      (vscode.workspace as { workspaceFile: unknown }).workspaceFile = { scheme: "file", authority: "", path: hubPath(ctx) };
      activate(ctx);
      await vi.waitFor(() => expect(mock.panels.map((p) => p.viewType)).toEqual(["jam.conversationTab"]));
      await expect(stat(`${ctx.globalStorageUri.path}/hub/pending-jam.example.com.json`)).rejects.toThrow();
    } finally {
      fetchSpy.mockRestore();
      deactivate();
    }
  });

  it("a stale pending file is ignored and deleted", async () => {
    mock.reset();
    mock.config["jam.url"] = JAM;
    const ctx = fakeContext();
    const fsp = await import("node:fs/promises");
    await fsp.mkdir(`${ctx.globalStorageUri.path}/hub`, { recursive: true });
    await writeFile(`${ctx.globalStorageUri.path}/hub/pending-jam.example.com.json`, JSON.stringify({ channel: CHN, at: Date.now() - 61_000 }));
    (vscode.workspace as { workspaceFile: unknown }).workspaceFile = { scheme: "file", authority: "", path: hubPath(ctx) };
    activate(ctx);
    await vi.waitFor(() => expect(stat(`${ctx.globalStorageUri.path}/hub/pending-jam.example.com.json`)).rejects.toThrow());
    await settle();
    expect(mock.panels).toEqual([]);
    deactivate();
  });

  it("a bad /go link warns and opens nothing", async () => {
    mock.reset();
    mock.config["jam.url"] = JAM;
    activate(fakeContext());
    mock.uriHandler!.handleUri({ path: "/go/chn_x", query: "" });
    await settle();
    expect(vscode.window.showWarningMessage).toHaveBeenCalled();
    expect(openFolderCalls()).toEqual([]);
    expect(mock.panels).toEqual([]);
    deactivate();
  });

  it("in the hub, the views follow the active conversation tab; a chat tab empties them", async () => {
    mock.reset();
    mock.config["jam.url"] = JAM;
    const CHAT = "chn_1abcdefghjkmnpqrstvwxyz012";
    const calls: Call[] = [];
    const base = jamFetch(calls).getMockImplementation()!;
    const fetchSpy = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      if (new URL(String(input)).pathname === `/app/user/api/conversations/${CHAT}`) return Response.json({ session: "", label: "Ann" });
      return base(input, init);
    });
    try {
      const ctx = fakeContext();
      ctx.secretValues.set("jam.session", stored("t2"));
      hubWindow(ctx);
      activate(ctx);
      const status = openView();
      status.receive({ type: "ready", pane: "status" });
      await vi.waitFor(() => expect(lastState(status)).toEqual({ signedIn: true, target: null }));
      const rail = openRail();
      rail.receive({ type: "open", intent: { kind: "go", channel: CHN, session: SES } });
      await vi.waitFor(() => expect(mock.panels).toHaveLength(1));
      mock.panels[0]!.setActive(true);
      await vi.waitFor(() => expect(lastState(status)).toEqual({ signedIn: true, target: { session: SES, label: "alpha", channel: CHN } }));
      rail.receive({ type: "open", intent: { kind: "go", channel: CHAT, session: "" } });
      await vi.waitFor(() => expect(mock.panels).toHaveLength(2));
      mock.panels[0]!.setActive(false);
      mock.panels[1]!.setActive(true);
      await vi.waitFor(() => expect(lastState(status)).toEqual({ signedIn: true, target: null }));
    } finally {
      fetchSpy.mockRestore();
      deactivate();
    }
  });

  it("a launcher for a listed Jam opens its hub quietly", async () => {
    mock.reset();
    mock.config["jam.url"] = JAM;
    mock.config["jam.urls"] = ["https://b.example"];
    const ctx = fakeContext();
    activate(ctx);
    mock.uriHandler!.handleUri({ path: "/hub", query: "jam=https://b.example" });
    await vi.waitFor(() => expect(openFolderCalls()).toHaveLength(1));
    expect(openFolderCalls()[0]![1]).toEqual(expect.objectContaining({ fsPath: `${ctx.globalStorageUri.path}/hub/Jam Hub (b.example).code-workspace` }));
    expect(vscode.window.showWarningMessage).not.toHaveBeenCalled();
    deactivate();
  });

  it("a launcher for an unlisted Jam warns with Add it, and opens and adds nothing", async () => {
    mock.reset();
    mock.config["jam.url"] = JAM;
    const ctx = fakeContext();
    activate(ctx);
    mock.uriHandler!.handleUri({ path: "/hub", query: "jam=https://other.example" });
    await vi.waitFor(() => expect(vscode.window.showWarningMessage).toHaveBeenCalledWith("This launcher is for https://other.example, which isn't one of your Jams.", "Add it"));
    await settle();
    expect(openFolderCalls()).toEqual([]);
    expect(mock.config["jam.urls"]).toBeUndefined();
    deactivate();
  });

  it("Add it runs Add a Jam prefilled", async () => {
    mock.reset();
    mock.config["jam.url"] = JAM;
    mock.answers = ["Add it"];
    const ctx = fakeContext();
    activate(ctx);
    mock.uriHandler!.handleUri({ path: "/hub", query: "jam=https://other.example" });
    await vi.waitFor(() => expect(vscode.window.showInputBox).toHaveBeenCalledWith(expect.objectContaining({ value: "https://other.example" })));
    deactivate();
  });

  it("Add a Jam appends it to jam.urls and signs in to it", async () => {
    mock.reset();
    mock.config["jam.url"] = JAM;
    mock.input = "https://b.example/";
    const ctx = fakeContext();
    activate(ctx);
    void mock.commands.get("jam.addJam")!();
    await vi.waitFor(() => expect(mock.config["jam.urls"]).toEqual(["https://b.example"]));
    await vi.waitFor(() => expect(vscode.env.openExternal).toHaveBeenCalled());
    expect(String(vi.mocked(vscode.env.openExternal).mock.calls.at(-1)![0])).toMatch(/^https:\/\/b\.example\/app\/auth\/app-token\?/);
    deactivate();
  });

  it("Add a Jam of a Jam already listed only signs in", async () => {
    mock.reset();
    mock.config["jam.url"] = JAM;
    mock.input = JAM;
    const ctx = fakeContext();
    activate(ctx);
    void mock.commands.get("jam.addJam")!();
    await vi.waitFor(() => expect(vscode.env.openExternal).toHaveBeenCalled());
    expect(mock.config["jam.urls"]).toBeUndefined();
    deactivate();
  });

  it("a …/hub link opens the hub", async () => {
    mock.reset();
    mock.config["jam.url"] = JAM;
    const ctx = fakeContext();
    activate(ctx);
    mock.uriHandler!.handleUri({ path: "/hub", query: "" });
    await vi.waitFor(() => expect(openFolderCalls()).toHaveLength(1));
    expect(openFolderCalls()[0]![1]).toEqual(expect.objectContaining({ fsPath: hubPath(ctx) }));
    deactivate();
  });
});

describe("several Jams", () => {
  const B = "https://b.example";
  const storedAt = (url: string, token: string) => JSON.stringify({ url, token, user: "u", expiresAt: "x" });
  const hosts = (spy: { mock: { calls: unknown[][] } }) => spy.mock.calls.map((c) => new URL(String(c[0])).host);

  it("jam.multiJam is false with one Jam and true with two", () => {
    mock.reset();
    mock.config["jam.url"] = JAM;
    activate(fakeContext());
    expect(mock.context.get("jam.multiJam")).toBe(false);
    deactivate();
    mock.reset();
    mock.config["jam.url"] = JAM;
    mock.config["jam.urls"] = [B];
    activate(fakeContext());
    expect(mock.context.get("jam.multiJam")).toBe(true);
    deactivate();
  });

  it("a link in a Jam-B window records the studio's Jam", async () => {
    mock.reset();
    const home = await mkdtemp(path.join(tmpdir(), "jamext-"));
    const fsp = await import("node:fs/promises");
    await fsp.mkdir(path.join(home, ".ssh"));
    await writeFile(path.join(home, ".ssh", "config"), `Host jam-${SES}\n  HostName x\n`);
    vi.stubEnv("HOME", home);
    vi.stubEnv("XDG_CONFIG_HOME", home);
    const getExt = vi.mocked(vscode.extensions.getExtension);
    getExt.mockImplementation(() => ({}) as vscode.Extension<unknown>);
    const fetchSpy = jamFetch([]);
    try {
      mock.config["jam.url"] = JAM;
      mock.config["jam.urls"] = [B];
      mock.config["remote.SSH.localServerDownload"] = "always";
      const ctx = fakeContext();
      ctx.secretValues.set(`jam.session:${B}`, storedAt(B, "t2"));
      await ctx.workspaceState.update("jam.windowJam", B);
      activate(ctx);
      mock.uriHandler!.handleUri({ path: `/session/${SES}`, query: "" });
      await vi.waitFor(() => expect(openFolderCalls()).toHaveLength(1));
      expect(ctx.state.get(`jam.studioJam.${SES}`)).toBe(B);
      expect(hosts(fetchSpy)).toContain("b.example");
    } finally {
      getExt.mockImplementation(() => undefined);
      fetchSpy.mockRestore();
      vi.unstubAllEnvs();
      deactivate();
    }
  });

  it("a studio window recorded for Jam B talks to B", async () => {
    mock.reset();
    const fetchSpy = jamFetch([]);
    try {
      mock.config["jam.url"] = JAM;
      mock.config["jam.urls"] = [B];
      const ctx = fakeContext();
      ctx.secretValues.set(`jam.session:${B}`, storedAt(B, "t2"));
      ctx.state.set(`jam.studioJam.${SES}`, B);
      remoteFolder();
      activate(ctx);
      const v = openView();
      v.receive({ type: "ready", pane: "status" });
      await vi.waitFor(() => expect(lastState(v)).toMatchObject({ signedIn: true }));
      // Its session lookup goes to B. (Other Jams' hosts aren't asserted
      // absent: an earlier test's retry timer may still fire into the spy.)
      const sessionHosts = fetchSpy.mock.calls.map((c) => new URL(String(c[0]))).filter((u) => u.pathname === `/app/user/api/sessions/${SES}`).map((u) => u.host);
      expect(sessionHosts).toContain("b.example");
    } finally {
      fetchSpy.mockRestore();
      deactivate();
    }
  });

  it("Switch Jam stores the choice and reloads", async () => {
    mock.reset();
    mock.config["jam.url"] = JAM;
    mock.config["jam.urls"] = [B];
    const ctx = fakeContext();
    activate(ctx);
    mock.pick = { label: B };
    await mock.commands.get("jam.switchJam")!();
    expect(ctx.workspaceState.get("jam.windowJam")).toBe(B);
    expect(vscode.commands.executeCommand).toHaveBeenCalledWith("workbench.action.reloadWindow");
    deactivate();
  });

  it("Switch Jam in a hub says the hub belongs to its Jam", async () => {
    mock.reset();
    mock.config["jam.url"] = JAM;
    mock.config["jam.urls"] = [B];
    const ctx = fakeContext();
    (vscode.workspace as { workspaceFile: unknown }).workspaceFile = { scheme: "file", authority: "", path: `${ctx.globalStorageUri.path}/hub/Jam Hub (b.example).code-workspace` };
    activate(ctx);
    await mock.commands.get("jam.switchJam")!();
    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(`This window belongs to ${B}.`);
    expect(vscode.commands.executeCommand).not.toHaveBeenCalledWith("workbench.action.reloadWindow");
    deactivate();
  });

  it("each Jam's hub is its own workspace file", async () => {
    mock.reset();
    mock.config["jam.url"] = JAM;
    mock.config["jam.urls"] = [B];
    const ctx = fakeContext();
    await ctx.workspaceState.update("jam.windowJam", B);
    activate(ctx);
    await mock.commands.get("jam.openHub")!();
    expect(openFolderCalls()[0]![1]).toEqual(expect.objectContaining({ fsPath: `${ctx.globalStorageUri.path}/hub/Jam Hub (b.example).code-workspace` }));
    deactivate();
  });
});

describe("updates", () => {
  it("a signed-in window offers Jam's newer extension, installs the downloaded .vsix and reloads", async () => {
    mock.reset();
    mock.config["jam.url"] = JAM;
    mock.answers.push("Update", "Reload Window");
    const fetchSpy = vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const u = new URL(String(input));
      if (u.pathname === "/me/vscode") return Response.json({ available: true, version: "0.5.0" });
      if (u.pathname === "/me/vscode/jam.vsix") return new Response(new Uint8Array([7, 7]));
      if (u.pathname === "/app/user/events") return new Response(null, { status: 204 });
      return Response.json({ groups: [] });
    });
    try {
      const ctx = fakeContext();
      ctx.secretValues.set("jam.session", stored("t2"));
      (ctx as { extension?: unknown }).extension = { packageJSON: { version: "0.4.0" } };
      activate(ctx);
      await vi.waitFor(() => expect(vscode.commands.executeCommand).toHaveBeenCalledWith("workbench.action.reloadWindow"));
      expect(vscode.window.showInformationMessage).toHaveBeenCalledWith("A Jam extension update is available: 0.5.0 (you have 0.4.0).", "Update", "Later");
      const install = vi.mocked(vscode.commands.executeCommand).mock.calls.find((c) => c[0] === "workbench.extensions.installExtension")!;
      const file = (install[1] as { fsPath: string }).fsPath;
      expect(file.endsWith("/updates/jam-vscode_0.5.0.vsix")).toBe(true);
      expect([...(await readFile(file))]).toEqual([7, 7]);
    } finally {
      fetchSpy.mockRestore();
      deactivate();
    }
  });

  it("a dev build never checks", async () => {
    mock.reset();
    mock.config["jam.url"] = JAM;
    const calls: Call[] = [];
    const fetchSpy = jamFetch(calls);
    try {
      const ctx = fakeContext();
      ctx.secretValues.set("jam.session", stored("t2"));
      activate(ctx);
      await new Promise((r) => setTimeout(r, 50));
      expect(calls.some((c) => c.path.startsWith("/me/vscode"))).toBe(false);
    } finally {
      fetchSpy.mockRestore();
      deactivate();
    }
  });
});
