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
const localPath = (ctx: vscode.ExtensionContext, id = SES) => `${ctx.globalStorageUri.path}/sessions/${id}.code-workspace`;
const localWindow = (ctx: vscode.ExtensionContext, id = SES) => {
  (vscode.workspace as { workspaceFile: unknown }).workspaceFile = { scheme: "file", authority: "", path: localPath(ctx, id) };
};
const openFolderCalls = () => vi.mocked(vscode.commands.executeCommand).mock.calls.filter((c) => c[0] === "vscode.openFolder");
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
  it("activate registers five view providers, one tree view and eighteen commands", () => {
    mock.reset();
    const ctx = fakeContext();
    activate(ctx);
    expect(vscode.commands.registerCommand).toHaveBeenCalledTimes(18);
    expect(vscode.window.createTreeView).toHaveBeenCalledWith("jam.needsYou", expect.anything());
    expect(vscode.window.registerUriHandler).toHaveBeenCalledTimes(1);
    expect(vscode.window.registerWebviewViewProvider).toHaveBeenCalledTimes(5);
    const calls = vi.mocked(vscode.window.registerWebviewViewProvider).mock.calls as unknown as [string, unknown, { webviewOptions?: { retainContextWhenHidden?: boolean } } | undefined][];
    expect(calls.map((c) => c[0])).toEqual(["jam.conversation", "jam.thinking", "jam.previews", "jam.status", "jam.artifacts"]);
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
      const view = vscode.window.createTreeView as unknown as { mock: { results: { value: { badge: unknown } }[] } };
      const badge = () => view.mock.results.at(-1)!.value.badge;
      await vi.waitFor(() => expect(badge()).toEqual({ value: 2, tooltip: "2 conversations need you" }), { timeout: 6000 });
      push("event: changed\ndata: {}\n\n");
      await vi.waitFor(() => expect(badge()).toEqual({ value: 1, tooltip: "1 conversation needs you" }), { timeout: 6000 });
      expect(mock.context.get("jam.signedIn")).toBe(true);
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
      const view = vscode.window.createTreeView as unknown as { mock: { results: { value: { badge: unknown } }[] } };
      const badge = () => view.mock.results.at(-1)!.value.badge;
      // Well inside NEEDS_YOU_DEBOUNCE_MS: no debounce on activation or sign-out.
      await vi.waitFor(() => expect(badge()).toEqual({ value: 1, tooltip: "1 conversation needs you" }), { timeout: 1000 });
      expect(mock.context.get("jam.signedIn")).toBe(true);
      await mock.commands.get("jam.signOut")!();
      await vi.waitFor(() => expect(badge()).toBeUndefined(), { timeout: 1000 });
      expect(mock.context.get("jam.signedIn")).toBe(false);
    } finally {
      fetchSpy.mockRestore();
      deactivate();
    }
  });

  it("jam.needsYou.open on a session row opens its code window; on a chat row opens the web app", async () => {
    mock.reset();
    const CHAT = "chn_1abcdefghjkmnpqrstvwxyz012";
    const fetchSpy = vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const u = new URL(String(input));
      if (u.pathname === "/app/user/events") return new Response(null, { status: 204 });
      if (u.pathname === `/app/user/api/conversations/${CHN}`) return Response.json({ session: SES });
      if (u.pathname === `/app/user/api/conversations/${CHAT}`) return Response.json({ session: "" });
      if (u.pathname === `/app/user/api/sessions/${SES}`) return Response.json({ label: "alpha", channel: CHN, ssh: "" });
      return Response.json({ groups: [] });
    });
    try {
      mock.config["jam.url"] = JAM;
      const ctx = fakeContext();
      ctx.secretValues.set("jam.session", stored("t2"));
      activate(ctx);
      const open = mock.commands.get("jam.needsYou.open")!;
      const r = (id: string) => ({ id, label: "x", project: "p", kind: "session", needsYou: true, mission: "" });
      await open(r(CHAT));
      expect(vscode.env.openExternal).toHaveBeenCalledWith(expect.objectContaining({ toString: expect.any(Function) }));
      expect(String(vi.mocked(vscode.env.openExternal).mock.calls.at(-1)![0])).toContain(CHAT);
      vi.mocked(vscode.env.openExternal).mockClear();
      vi.mocked(vscode.commands.executeCommand).mockClear();
      await open(r(CHN));
      // No SSH is needed to open: the session's local window opens.
      await vi.waitFor(() =>
        expect(vscode.commands.executeCommand).toHaveBeenCalledWith("vscode.openFolder", expect.objectContaining({ path: localPath(ctx) }), { forceNewWindow: true }),
      );
    } finally {
      fetchSpy.mockRestore();
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

  it("a notify event on the stream shows a toast; Open calls openSession", async () => {
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
      activate(ctx);
      await vi.waitFor(() => expect(toasts()).toEqual([["Alice · build-bot: done?", "Open"]]));
      await vi.waitFor(() =>
        expect(vscode.commands.executeCommand).toHaveBeenCalledWith("vscode.openFolder", expect.objectContaining({ path: localPath(ctx) }), { forceNewWindow: true }),
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

describe("local session windows (connect on demand)", () => {
  // sshHome makes ensureSsh find a hand-written Host block, so it succeeds with no CLI.
  const sshHome = async () => {
    const home = await mkdtemp(path.join(tmpdir(), "jamext-"));
    const fsp = await import("node:fs/promises");
    await fsp.mkdir(path.join(home, ".ssh"));
    await writeFile(path.join(home, ".ssh", "config"), `Host jam-${SES}\n  HostName x\n`);
    vi.stubEnv("HOME", home);
    vi.stubEnv("XDG_CONFIG_HOME", home);
  };
  const items = () => vi.mocked(vscode.window.createStatusBarItem).mock.results.map((r) => r.value as { text: string; command: string; shown: boolean });

  it("a link opens the session's local workspace file in a new window; the file is {folders: []} with no token", async () => {
    mock.reset();
    const calls: Call[] = [];
    const fetchSpy = jamFetch(calls);
    try {
      mock.config["jam.url"] = JAM;
      const ctx = fakeContext();
      ctx.secretValues.set("jam.session", stored("t2"));
      activate(ctx);
      mock.uriHandler!.handleUri({ path: `/session/${SES}`, query: "" });
      await vi.waitFor(() => expect(openFolderCalls()).toHaveLength(1));
      const [, uri, opts] = openFolderCalls()[0]!;
      expect(opts).toEqual({ forceNewWindow: true });
      expect((uri as { scheme: string; path: string }).scheme).toBe("file");
      expect((uri as { path: string }).path).toBe(localPath(ctx));
      const text = await readFile(localPath(ctx), "utf8");
      expect(JSON.parse(text)).toEqual({ folders: [] });
      expect(text).not.toMatch(/t2|Bearer/);
      expect(calls.some((c) => c.path === `/app/user/api/sessions/${SES}`)).toBe(true);
    } finally {
      fetchSpy.mockRestore();
      deactivate();
    }
  });

  it("a link to a connected session opens its remote window instead", async () => {
    mock.reset();
    const fetchSpy = jamFetch([]);
    try {
      mock.config["jam.url"] = JAM;
      const ctx = fakeContext();
      ctx.secretValues.set("jam.session", stored("t2"));
      ctx.state.set("jam.connected", [SES]);
      activate(ctx);
      mock.uriHandler!.handleUri({ path: `/session/${SES}`, query: "" });
      await vi.waitFor(() => expect(openFolderCalls()).toHaveLength(1));
      expect(openFolderCalls()[0]).toEqual(["vscode.openFolder", expect.objectContaining({ authority: `ssh-remote+jam-${SES}` }), { forceNewWindow: true }]);
    } finally {
      fetchSpy.mockRestore();
      deactivate();
    }
  });

  it("a local session window: the panes follow its session, Connect shows, and nothing forwards", async () => {
    mock.reset();
    const fetchSpy = jamFetch([]);
    try {
      mock.config["jam.url"] = JAM;
      const ctx = fakeContext();
      ctx.secretValues.set("jam.session", stored("t2"));
      localWindow(ctx);
      activate(ctx);
      expect(items().filter((i) => i.shown).map((i) => [i.text, i.command])).toEqual([["$(plug) Connect studio", "jam.connectStudio"]]);
      expect(mock.context.get("jam.window")).toBe("local");
      const v = openView();
      v.receive({ type: "ready", pane: "conversation" });
      await vi.waitFor(() => expect(lastState(v)?.target).toEqual({ session: SES, label: "alpha", channel: CHN }));
      // A preview of the window's own session opens its web URL: forwarding needs the remote.
      v.receive({ type: "open", intent: { kind: "preview", session: SES, port: 3000, url: "https://p.example.com/" } });
      await vi.waitFor(() => expect(vscode.env.openExternal).toHaveBeenCalledWith(expect.objectContaining({ toString: expect.any(Function) })));
      expect(String(vi.mocked(vscode.env.openExternal).mock.calls.at(-1)![0])).toBe("https://p.example.com/");
      expect(vscode.env.asExternalUri).not.toHaveBeenCalled();
    } finally {
      fetchSpy.mockRestore();
      deactivate();
    }
  });

  it("a workspace file of the same name elsewhere is not a session window", async () => {
    mock.reset();
    const ctx = fakeContext();
    (vscode.workspace as { workspaceFile: unknown }).workspaceFile = { scheme: "file", authority: "", path: `/tmp/elsewhere/sessions/${SES}.code-workspace` };
    activate(ctx);
    expect(items().filter((i) => i.shown)).toEqual([]);
    expect(mock.context.get("jam.window")).toBe("");
    deactivate();
  });

  it("a plain local window shows neither item", () => {
    mock.reset();
    activate(fakeContext());
    expect(items().filter((i) => i.shown)).toEqual([]);
    deactivate();
  });

  it("Connect in a local window sets up SSH, marks the session, and reloads this window onto the remote", async () => {
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
      localWindow(ctx);
      activate(ctx);
      await mock.commands.get("jam.connectStudio")!();
      expect(openFolderCalls()).toEqual([["vscode.openFolder", expect.objectContaining({ authority: `ssh-remote+jam-${SES}` }), { forceReuseWindow: true }]]);
      expect(ctx.state.get("jam.connected")).toEqual([SES]);
      expect(mock.output.join("\n")).toContain(`[${SES}] ssh ready: alias jam-${SES}`);
      expect(mock.output.join("\n")).not.toMatch(/t2|Bearer/);
    } finally {
      getExt.mockImplementation(() => undefined);
      fetchSpy.mockRestore();
      vi.unstubAllEnvs();
      deactivate();
    }
  });

  it("Connect outside a session window says so", async () => {
    mock.reset();
    activate(fakeContext());
    await mock.commands.get("jam.connectStudio")!();
    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith("This window isn't a Jam session window. Open a session first (Jam: Open Session…).");
    expect(openFolderCalls()).toEqual([]);
    deactivate();
  });

  it("a remote session window: Disconnect shows and is marked connected; Disconnect reloads onto the local workspace and unmarks", async () => {
    mock.reset();
    const ctx = fakeContext();
    remoteFolder();
    activate(ctx);
    expect(items().filter((i) => i.shown).map((i) => [i.text, i.command])).toEqual([["$(debug-disconnect) Disconnect studio", "jam.disconnectStudio"]]);
    expect(mock.context.get("jam.window")).toBe("remote");
    await vi.waitFor(() => expect(ctx.state.get("jam.connected")).toEqual([SES]));
    await mock.commands.get("jam.disconnectStudio")!();
    expect(openFolderCalls()).toEqual([["vscode.openFolder", expect.objectContaining({ scheme: "file", path: localPath(ctx) }), { forceReuseWindow: true }]]);
    expect(ctx.state.get("jam.connected")).toEqual([]);
    expect(JSON.parse(await readFile(localPath(ctx), "utf8"))).toEqual({ folders: [] });
    deactivate();
  });

  it("deactivating a remote session window unmarks it; a local one leaves the set alone", async () => {
    mock.reset();
    const ctx = fakeContext();
    remoteFolder();
    activate(ctx);
    await vi.waitFor(() => expect(ctx.state.get("jam.connected")).toEqual([SES]));
    await deactivate();
    expect(ctx.state.get("jam.connected")).toEqual([]);

    mock.reset();
    const ctx2 = fakeContext();
    ctx2.state.set("jam.connected", [SES]);
    localWindow(ctx2, "ses_1abcdefghjkmnpqrstvwxyz012");
    activate(ctx2);
    await deactivate();
    expect(ctx2.state.get("jam.connected")).toEqual([SES]);
  });
});
