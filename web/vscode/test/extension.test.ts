import { mkdtemp, readFile, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import * as path from "node:path";
import { describe, expect, it, vi } from "vitest";
import * as vscode from "vscode";
import { SignedOutError } from "../src/api";
import { activate, deactivate } from "../src/extension";
import type { ExtToPane, PaneState } from "../../app/src/bridge/protocol.js" with { "resolution-mode": "import" };
import { fakeContext, fakeWebviewView, mock } from "./vscode";

const JAM = "https://jam.example.com";
const SES = "ses_0abcdefghjkmnpqrstvwxyz012";
const CHN = "chn_0abcdefghjkmnpqrstvwxyz012";
const remoteFolder = () => {
  (vscode.workspace as { workspaceFolders: unknown }).workspaceFolders = [{ uri: { scheme: "vscode-remote", authority: `ssh-remote+jam-${SES}` } }];
};
const stored = (token: string) => JSON.stringify({ url: JAM, token, user: "u", expiresAt: "x" });
type View = ReturnType<typeof fakeWebviewView>;
const openView = (): View => {
  const v = fakeWebviewView();
  void mock.viewProviders.get("jam.conversation")!.resolveWebviewView(v as unknown as vscode.WebviewView, {} as vscode.WebviewViewResolveContext, {} as vscode.CancellationToken);
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
    if (u.pathname === `/app/user/api/conversations/${CHN}`) return Response.json({ session: SES });
    return Response.json({});
  });
}

describe("activate", () => {
  it("activate registers five view providers and fifteen commands", () => {
    mock.reset();
    const ctx = fakeContext();
    activate(ctx);
    expect(vscode.commands.registerCommand).toHaveBeenCalledTimes(15);
    expect(vscode.window.registerUriHandler).toHaveBeenCalledTimes(1);
    expect(vscode.window.registerWebviewViewProvider).toHaveBeenCalledTimes(5);
    const calls = vi.mocked(vscode.window.registerWebviewViewProvider).mock.calls as unknown as [string, unknown, { webviewOptions?: { retainContextWhenHidden?: boolean } } | undefined][];
    expect(calls.map((c) => c[0])).toEqual(["jam.conversation", "jam.thinking", "jam.previews", "jam.status", "jam.artifacts"]);
    for (const [, , opts] of calls) expect(opts?.webviewOptions?.retainContextWhenHidden).toBeUndefined();
    deactivate();
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
      for (const v of [a, b]) v.receive({ type: "ready", pane: "conversation" });
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
        await vi.waitFor(() => expect(lastState(v)).toEqual({ signedIn: true, target: { session: SES, label: "alpha", channel: CHN }, pinned: false, elsewhere: false }));
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
      activate(ctx);
      const v = openView();
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

  it("jam.focusSession picks a session that unpinned panes follow; pin and unpin act on the view", async () => {
    mock.reset();
    const home = await mkdtemp(path.join(tmpdir(), "jamext-"));
    vi.stubEnv("XDG_CONFIG_HOME", home);
    const calls: Call[] = [];
    const fetchSpy = jamFetch(calls);
    try {
      mock.config["jam.url"] = JAM;
      const ctx = fakeContext();
      ctx.secretValues.set("jam.session", stored("t2"));
      activate(ctx);
      const v = openView();
      v.receive({ type: "ready", pane: "conversation" });
      await vi.waitFor(() => expect(lastState(v)).toEqual({ signedIn: true, target: null, pinned: false, elsewhere: false }));
      expect(mock.context.get("jam.conversation.hasSession")).toBe(false);
      mock.pick = { label: "alpha", pick: { label: "alpha", row: { id: CHN } } };
      await mock.commands.get("jam.focusSession")!();
      await vi.waitFor(() => expect(lastState(v)?.target).toEqual({ session: SES, label: "alpha", channel: CHN }));
      expect(mock.context.get("jam.conversation.hasSession")).toBe(true);
      await mock.commands.get("jam.conversation.pin")!();
      await vi.waitFor(() => expect(lastState(v)?.pinned).toBe(true));
      expect(mock.context.get("jam.conversation.pinned")).toBe(true);
      await mock.commands.get("jam.conversation.unpin")!();
      await vi.waitFor(() => expect(lastState(v)?.pinned).toBe(false));
    } finally {
      fetchSpy.mockRestore();
      vi.unstubAllEnvs();
      deactivate();
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
    const fetchSpy = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response("", { status: 401 }));
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
      expect(fetchSpy).toHaveBeenCalledTimes(1);
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
      const focusing = mock.commands.get("jam.focusSession")!();
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
