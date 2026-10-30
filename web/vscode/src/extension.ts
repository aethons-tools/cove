// The VS Code layer: the only module that imports vscode. It builds the real
// dependencies and wires the commands and the URI handler to the pure
// modules beside it.
import { execFile } from "node:child_process";
import * as os from "node:os";
import { promisify } from "node:util";
import * as vscode from "vscode";
import { ApiError, JamApi, SignedOutError } from "./api";
import { Auth } from "./auth";
import type { Session } from "./auth";
import { appFor, hubFileFor, jamList, pendingFileFor } from "./jams";
import { Auths } from "./auths";
import { normalizeJamUrl, settings } from "./config";
import { editorFor } from "./editor";
import { forgetHostKey } from "./hostkey";
import {
  CONNECTED_BEAT_MS,
  connectedSet,
  openSession,
  pickSession,
  goTo,
  HUB_WORKSPACE,
  readPending,
  studioFromAuthority,
  windowAuthority,
  windowJam,
  windowKind,
} from "./open";
import type { GoDeps, OpenDeps, Ui } from "./open";
import { appTokenPath, configDir, knownHostsFile, sshConfigFile } from "./paths";
import { findSshKeygen } from "./ssh/cli";
import { listJamStudios } from "./ssh/config";
import type { ExecFile } from "./ssh/keys";
import { ensureSsh } from "./ssh/setup";
import { SESSION_ID_RE, route } from "./uri";
import { readFile } from "node:fs/promises";
import * as fsp from "node:fs/promises";
import { Router } from "./bridge/router";
import { FocusModel, sessionResolver } from "./focus";
import { FocusReporter } from "./focusreport";
import { LiveStream } from "./live";
import { ChannelGate } from "./notify/event";
import { NotifyLock } from "./notify/lock";
import { Notifier } from "./notify/notifier";
import { OsNotifier } from "./notify/os";
import type { OsDeps } from "./notify/os";
import { perform } from "./panes/actions";
import type { ActionDeps } from "./panes/actions";
import type { IntentAction } from "./bridge/intents";
import { BoardPanels } from "./panes/board";
import { PaneHub } from "./panes/hub";
import { SESSION_PANES, SessionPaneProvider } from "./panes/session";
import { NeedsYou } from "./needsyou";
import { RailViewProvider } from "./panes/rail";
import { ConversationPanels } from "./panes/conversation";
import { ThinkingStreams } from "./thinking";
import { Updater } from "./update";

const run: ExecFile = (file, args) => promisify(execFile)(file, args).then((r) => ({ stdout: String(r.stdout), stderr: String(r.stderr) }));
// The OS notifier's execFile: no shell, argv only, with a timeout and an abort signal.
const runNotify: OsDeps["execFile"] = (file, args, o) =>
  promisify(execFile)(file, args, { timeout: o.timeout, signal: o.signal }).then((r) => ({ stdout: String(r.stdout), stderr: String(r.stderr) }));

const jamSettings = () => {
  const c = vscode.workspace.getConfiguration("jam");
  return settings((k) => c.get(k));
};

let reporter: FocusReporter | undefined;
const TAB_SHOWN = "jam.conversationTabShown";
const WINDOW_JAM = "jam.windowJam";
// Run on deactivate: a remote session window's best-effort "no longer connected".
let onDeactivate: (() => Thenable<void>) | undefined;

export function activate(ctx: vscode.ExtensionContext): void {
  // Which window this is (a studio window, a hub, or neither) and which Jam
  // it shows. A hub or studio window keeps its Jam; an ordinary window shows
  // the one picked with Jam: Switch Jam…, else jam.url (read fresh, so a
  // changed jam.url still applies).
  const hubDir = vscode.Uri.joinPath(ctx.globalStorageUri, "hub");
  const kind = windowKind(vscode.workspace, hubDir, process.platform);
  const jams = () => {
    const c = vscode.workspace.getConfiguration("jam");
    return jamList(String(c.get("url") ?? ""), c.get("urls"));
  };
  const windowJamUrl = (): string | undefined =>
    windowJam(kind, jams().list, {
      studioJam: (id) => ctx.globalState.get<string>(`jam.studioJam.${id}`),
      chosen: ctx.workspaceState.get<string>(WINDOW_JAM),
    });
  // jamUrl is this window's Jam, or why there is none.
  const jamUrl = (): string | Error => {
    const u = windowJamUrl();
    if (u !== undefined) return u;
    return kind?.kind === "hub" ? new Error("This hub's Jam isn't one of your Jams any more (jam.url, jam.urls)") : jamSettings().url;
  };
  // One sign-in per Jam (auths.ts); auth is this window's Jam's.
  const auths = new Auths({
    make: (key, tokenFile) =>
      new Auth({
        secrets: ctx.secrets,
        openExternal: (u) => vscode.env.openExternal(vscode.Uri.parse(u)),
        fetch: (...a) => fetch(...a),
        uriScheme: vscode.env.uriScheme,
        hostname: os.hostname(),
        tokenFile,
        key,
      }),
    defaultUrl: () => {
      const u = jamSettings().url;
      return u instanceof Error ? undefined : u;
    },
    tokenFile: (app) => appTokenPath(configDir(process.env, os.homedir()), app),
  });
  // auth is this window's Jam's sign-in: the same calls as one Auth, routed
  // to the Jam they name (signOut and cancel: this window's).
  const authNow = () => {
    const u = jamUrl();
    return u instanceof Error ? auths.primary() : auths.authFor(u);
  };
  const auth = {
    session: (url: string) => auths.authFor(url).session(url),
    onChange: (cb: (s: Session | undefined) => void) =>
      auths.onChange((u, s) => {
        if (u === jamUrl()) cb(s);
      }),
    signIn: (url: string) => auths.authFor(url).signIn(url),
    signOut: () => authNow().signOut(),
    cancel: () => authNow().cancel(),
    syncTokenFile: (s: Session) => auths.authFor(s.url).syncTokenFile(s),
    handleCallback: (q: URLSearchParams) => auths.handleCallback(q),
  };

  // signedOut clears the session and offers to sign in again; every
  // SignedOutError goes through it.
  const signedOut = async (): Promise<void> => {
    await auth.signOut();
    const pick = await vscode.window.showWarningMessage("Your Jam sign-in expired or was revoked.", "Sign In");
    if (pick === "Sign In") await vscode.commands.executeCommand("jam.signIn");
  };

  // signOutOnce is the one signed-out path for commands, the panes' requests,
  // the live stream and the focused-session lookup: the first 401 of a sign-in
  // runs signedOut(), the rest wait for the next sign-in. A failing sign-out
  // (e.g. a locked keyring) is shown, not lost, and the next 401 tries again.
  let signingOut = false;
  const show = (x: unknown) => void vscode.window.showErrorMessage(x instanceof Error ? x.message : String(x));
  const signOutOnce = async (): Promise<void> => {
    if (signingOut) return;
    signingOut = true;
    try {
      await signedOut();
    } catch (e) {
      signingOut = false;
      show(e);
    }
  };

  // guard runs a command body, sending a SignedOutError to signOutOnce and
  // showing any other error.
  const guard = (fn: () => Promise<void>) => async (): Promise<void> => {
    try {
      await fn();
    } catch (e) {
      if (!(e instanceof SignedOutError)) return show(e);
      await signOutOnce();
    }
  };

  const signInWithProgress = (url: string) =>
    vscode.window.withProgress(
      { location: vscode.ProgressLocation.Notification, title: "Signing in to Jam in your browser…", cancellable: true },
      (_p, token) => {
        token.onCancellationRequested(() => auth.cancel());
        return auth.signIn(url);
      },
    );

  const signIn = guard(async () => {
    const url = jamUrl();
    if (url instanceof Error) throw url;
    const s = await signInWithProgress(url);
    void vscode.window.showInformationMessage(`Signed in to Jam as ${s.user}`);
  });

  const output = vscode.window.createOutputChannel("Jam");
  ctx.subscriptions.push(output);
  const log = (m: string) => output.appendLine(m);

  const connected = connectedSet(ctx.globalState);

  const editor = editorFor(vscode.env.uriScheme);
  const ui: Ui = {
    error: async (m, ...a) => vscode.window.showErrorMessage(m, ...a),
    info: async (m, ...a) => vscode.window.showInformationMessage(m, ...a),
    pick: async (items, placeholder) => vscode.window.showQuickPick(items, { placeHolder: placeholder }),
    openExternal: async (u) => void (await vscode.env.openExternal(vscode.Uri.parse(u))),
    openFolder: async (u) => void (await vscode.commands.executeCommand("vscode.openFolder", vscode.Uri.parse(u), { forceNewWindow: true })),
    // The hub is local: from a studio window it must not stay on the SSH connection.
    openHub: async (jam) => {
      const j = jam ?? jamUrl();
      if (j instanceof Error) throw j;
      const f = vscode.Uri.joinPath(hubDir, hubFileFor(j));
      await fsp.mkdir(hubDir.fsPath, { recursive: true });
      await fsp.writeFile(f.fsPath, HUB_WORKSPACE, { flag: "wx" }).catch((e: NodeJS.ErrnoException) => {
        if (e.code !== "EEXIST") throw e;
      });
      await vscode.commands.executeCommand("vscode.openFolder", f, { forceNewWindow: true, forceLocalWindow: true });
    },
    hasRemoteSsh: () => vscode.extensions.getExtension(editor.remoteSsh.id) !== undefined,
    installRemoteSsh: async () => void (await vscode.commands.executeCommand("workbench.extensions.installExtension", editor.remoteSsh.id)),
    getGlobal: (section, key) => vscode.workspace.getConfiguration(section).inspect(key)?.globalValue,
    setGlobal: (section, key, v) => Promise.resolve(vscode.workspace.getConfiguration(section).update(key, v, vscode.ConfigurationTarget.Global)),
    remembered: (k) => ctx.globalState.get<boolean>(k) === true,
    remember: async (k) => ctx.globalState.update(k, true),
    windowAuthority: () => windowAuthority(vscode.workspace),
    progress: (title, work) =>
      Promise.resolve(
        vscode.window.withProgress({ location: vscode.ProgressLocation.Notification, title }, (p) => work((message) => p.report({ message }))),
      ),
  };

  // The API is built only from auth.session(currentJamUrl): the stored session,
  // else a fresh sign-in.
  let signedInUrl = "";
  const openDeps: OpenDeps = {
    ui,
    editor,
    log,
    remember: async (id) => {
      const u = jamUrl();
      if (typeof u === "string") await ctx.globalState.update(`jam.studioJam.${id}`, u);
    },
    signedIn: async () => {
      const url = jamUrl();
      if (url instanceof Error) throw url;
      const s = (await auth.session(url)) ?? (await signInWithProgress(url));
      // A session read before a concurrent sign-out must not rewrite the
      // token file that sign-out just removed.
      if (!signingOut) await auth.syncTokenFile(s);
      signedInUrl = url;
      return { api: new JamApi(url, s.token, (...a) => fetch(...a)), url };
    },
    ssh: (api, studio) =>
      ensureSsh(
        {
          api,
          jamUrl: signedInUrl,
          ...(signedInUrl === auths.defaultUrl() ? {} : { app: appFor(signedInUrl, auths.defaultUrl()) }),
          cliPath: jamSettings().cliPath,
          env: process.env,
          home: os.homedir(),
          platform: process.platform,
          run,
          now: () => new Date(),
        },
        studio,
      ),
  };

  const forget = guard(async () => {
    const home = os.homedir();
    let studio = studioFromAuthority(ui.windowAuthority());
    if (!studio) {
      const text = await readFile(sshConfigFile(home), "utf8").catch(() => "");
      const items = listJamStudios(text).map((label) => ({ label }));
      studio = (await ui.pick(items, "Forget the host key of which studio?"))?.label;
    }
    if (!studio) return;
    if (!SESSION_ID_RE.test(studio)) throw new Error(`${studio} isn't a Jam studio id`);
    const msg = `Forget the pinned host key of jam-${studio}? Do this after the studio was upgraded and Remote-SSH reports the host identification changed.`;
    if ((await ui.info(msg, "Forget")) !== "Forget") return;
    const keygen = await findSshKeygen(process.env.PATH ?? "");
    if (keygen instanceof Error) throw keygen;
    await forgetHostKey(`jam-${studio}`, knownHostsFile(home), keygen, run);
    void vscode.window.showInformationMessage(`Forgot the host key of jam-${studio}.`);
  });

  // --- The session views and what they share with the window. ---
  // The current sign-in for this window's Jam, read fresh on every use: tokens
  // come only from auth.session(jamUrl()).
  const current = async (): Promise<{ url: string; token: string } | undefined> => {
    const url = jamUrl();
    if (url instanceof Error) return undefined;
    const s = await auth.session(url);
    return s && { url, token: s.token };
  };
  const apiNow = async (): Promise<JamApi> => {
    const s = await current();
    if (!s) throw new SignedOutError();
    return new JamApi(s.url, s.token, (...a) => fetch(...a));
  };

  // panesSignedOut hears the token a 401 refused: it signs out only while that
  // token is still the current one, so a late 401 for an old token can't sign
  // out a newer sign-in.
  const panesSignedOut = (token: string): void =>
    void (async () => {
      if ((await current())?.token === token) await signOutOnce();
    })().catch(show);

  const live = new LiveStream({
    session: current,
    fetch: (...a) => fetch(...a),
    onSignedOut: panesSignedOut,
    setTimeout,
    clearTimeout,
    random: Math.random,
  });
  const thinking = new ThinkingStreams({
    session: current,
    fetch: (...a) => fetch(...a),
    onSignedOut: panesSignedOut,
    setTimeout,
    clearTimeout,
    random: Math.random,
  });
  const router = new Router({ session: current, fetch: (...a) => fetch(...a), onSignedOut: panesSignedOut, log });
  // The window's session: a jam-<id> studio window's. Only a studio window
  // shows the session views (jam.window) and can forward a preview's port.
  const windowStudio = kind?.kind === "session" ? kind.id : undefined;
  const wj = jamUrl();
  log(`window: ${windowStudio ? `studio session ${windowStudio}` : kind?.kind === "hub" ? "hub" : "no session"} on ${wj instanceof Error ? "no Jam" : wj}`);
  void vscode.commands.executeCommand("setContext", "jam.window", kind?.kind ?? "");
  const { list: jamsNow, bad } = jams();
  if (bad.length > 0) log(`jam.urls: skipped ${bad.length} entr${bad.length === 1 ? "y" : "ies"} that aren't Jam addresses`);
  void vscode.commands.executeCommand("setContext", "jam.multiJam", jamsNow.length > 1);
  if (windowStudio) {
    // Reloading a studio window deactivates and reactivates it: mark it again.
    // The mark is refreshed while the window lives, so one it never got to
    // clear (closed, crashed) goes stale.
    const id = windowStudio;
    void connected.setConnected(id, true).catch(show);
    const beat = setInterval(
      () => void connected.setConnected(id, true).catch((e) => log(`[${id}] refreshing the connected mark failed: ${e instanceof Error ? e.message : String(e)}`)),
      CONNECTED_BEAT_MS,
    );
    ctx.subscriptions.push({ dispose: () => clearInterval(beat) });
    onDeactivate = () => {
      clearInterval(beat);
      return connected.setConnected(id, false);
    };
  }
  const model = new FocusModel({
    windowSession: windowStudio,
    resolve: async (session) => {
      const s = await current();
      if (!s) throw new SignedOutError();
      try {
        return await sessionResolver(new JamApi(s.url, s.token, (...a) => fetch(...a)))(session);
      } catch (e) {
        if (e instanceof SignedOutError) panesSignedOut(s.token);
        throw e;
      }
    },
  });
  const hub = new PaneHub(live, (e) => {
    if (e.event === "presence" || e.event === "open") model.invalidate();
  });
  const rep = new FocusReporter({
    post: async (channel) => (await apiNow()).pushFocus(channel),
    setInterval,
    clearInterval,
  });
  reporter = rep;
  rep.windowFocused(vscode.window.state.focused);
  ctx.subscriptions.push(vscode.window.onDidChangeWindowState((s) => rep.windowFocused(s.focused)));

  // The needs-you count is the badge on the rail view (the Jam activity-bar icon).
  let rail: RailViewProvider | undefined;
  let badge: Parameters<RailViewProvider["setBadge"]>[0];
  const needsYou = new NeedsYou({
    session: current,
    api: (s) => new JamApi(s.url, s.token, (...a) => fetch(...a)),
    setBadge: (b) => {
      badge = b;
      rail?.setBadge(b);
    },
    onSignedOut: panesSignedOut,
    setTimeout,
    clearTimeout,
  });
  ctx.subscriptions.push({ dispose: () => needsYou.dispose() });

  // Signing in reconnects the streams with the new token; signing out
  // reconnects them to nothing, so the old token is no longer used.
  const offAuth = auth.onChange((s) => {
    if (s) signingOut = false;
    live.restart();
    thinking.restart();
    void needsYou.refresh(); // at once: only live events are debounced
  });
  ctx.subscriptions.push({ dispose: offAuth });

  const exec = (c: string, ...a: unknown[]) => vscode.commands.executeCommand(c, ...a);
  const actionDeps: ActionDeps = {
    openExternal: (u) => vscode.env.openExternal(vscode.Uri.parse(u)),
    executeCommand: exec,
    asExternalUri: async (u) => (await vscode.env.asExternalUri(vscode.Uri.parse(u))).toString(),
    openBoard: (id) => boards.open(id),
    openSession: (id) => guard(() => openSession(openDeps, id))(),
    // A rail click: the session's studio window when one is open, else (until
    // the hub) the conversation in the web app.
    goTo: (channel, session) => goToConversation(channel, session),
    // An ask elsewhere: the window's conversation tab (opened if need be: the
    // hub or a studio window only), scrolled to it.
    revealConversation: (anchor) =>
      guard(async () => {
        const t = await model.target();
        if (!t || !("channel" in t) || !kind) return;
        if (!conversations.has(t.channel)) conversations.open(t.channel);
        conversations.reveal(t.channel, anchor ?? undefined);
      })(),
    error: (m) => void vscode.window.showErrorMessage(m),
  };
  const portDeps = {
    hub,
    router,
    thinking,
    jamUrl,
    windowStudio,
    perform: (a: IntentAction) => void perform(a, actionDeps),
    executeCommand: exec,
  };
  const boards = new BoardPanels({
    ...portDeps,
    extensionUri: ctx.extensionUri,
    auth,
    createPanel: (...a) => vscode.window.createWebviewPanel(...a),
  });
  ctx.subscriptions.push(
    vscode.window.registerWebviewPanelSerializer(BoardPanels.viewType, { deserializeWebviewPanel: async (p, s) => boards.restore(p, s) }),
  );
  const conversations = new ConversationPanels({
    ...portDeps,
    extensionUri: ctx.extensionUri,
    auth,
    createPanel: (...a) => vscode.window.createWebviewPanel(...a),
    resolve: async (channel) => {
      const conv = await (await apiNow()).conversation(channel).catch((e: unknown) => {
        if (e instanceof ApiError && e.status === 404) return undefined;
        throw e;
      });
      return conv ? { session: conv.session, label: conv.label, channel } : "gone";
    },
    reporter: rep,
    // The hub's views follow its active conversation tab (a chat: none).
    onActive: (t) => {
      if (kind?.kind === "hub") model.setSession(t.session || undefined);
    },
  });
  ctx.subscriptions.push(
    vscode.window.registerWebviewPanelSerializer(ConversationPanels.viewType, { deserializeWebviewPanel: async (p, s) => conversations.restore(p, s) }),
  );
  // Going to a conversation: its studio window, else its hub tab (from
  // another window, by a pending request the hub reads; see readPending).
  const pendingFile = (): string => {
    const j = jamUrl();
    if (j instanceof Error) throw j;
    return vscode.Uri.joinPath(hubDir, pendingFileFor(j)).fsPath;
  };
  const goDeps: GoDeps = {
    isConnected: connected.isConnected,
    openSession: (id) => openSession(openDeps, id),
    isHub: kind?.kind === "hub",
    ownSession: windowStudio,
    openTab: (c) => conversations.open(c),
    writePending: async (p) => {
      await fsp.mkdir(hubDir.fsPath, { recursive: true });
      await fsp.writeFile(pendingFile(), JSON.stringify(p));
    },
    openHub: () => ui.openHub(),
    now: Date.now,
  };
  const goToConversation = (channel: string, session: string) => guard(() => goTo(goDeps, channel, session))();
  if (kind?.kind === "hub") {
    const takePending = async (): Promise<void> => {
      const file = pendingFile();
      const raw = await fsp.readFile(file, "utf8").catch(() => undefined);
      if (raw === undefined) return;
      await fsp.rm(file, { force: true });
      const c = readPending(raw, Date.now());
      if (c) conversations.open(c);
    };
    void takePending().catch((e) => log(`hub: reading the pending request failed: ${e instanceof Error ? e.message : String(e)}`));
    ctx.subscriptions.push(
      vscode.window.onDidChangeWindowState((st) => {
        if (st.focused) void takePending().catch(() => undefined);
      }),
    );
  }
  // A studio window opens its own conversation tab the first time only; after
  // that the tab comes back with the window, or stays closed if closed.
  if (kind?.kind === "session" && !ctx.workspaceState.get<boolean>(TAB_SHOWN)) {
    void model
      .target()
      .then(async (t) => {
        if (!t || !("channel" in t)) return;
        conversations.open(t.channel);
        await ctx.workspaceState.update(TAB_SHOWN, true);
      })
      .catch((e: unknown) => log(`opening the conversation tab failed: ${e instanceof Error ? e.message : String(e)}`));
  }
  rail = new RailViewProvider({ extensionUri: ctx.extensionUri, ...portDeps, auth, windows: connected.list });
  rail.setBadge(badge);
  ctx.subscriptions.push(vscode.window.registerWebviewViewProvider(rail.viewType, rail));
  const providers = new Map<string, SessionPaneProvider>();
  for (const name of SESSION_PANES) {
    const p = new SessionPaneProvider(name, {
      extensionUri: ctx.extensionUri,
      ...portDeps,
      model,
      reporter: rep,
      auth,
    });
    providers.set(name, p);
    ctx.subscriptions.push(vscode.window.registerWebviewViewProvider(p.viewType, p));
  }

  // --- Notifications: the window holds its one stream while signed in. ---
  const osNotifier = new OsNotifier({ platform: process.platform, extensionPath: ctx.extensionPath, execFile: runNotify, chmod: fsp.chmod, log });
  const notifier = new Notifier({
    gate: new ChannelGate(),
    lock: new NotifyLock({
      dir: vscode.Uri.joinPath(ctx.globalStorageUri, "notify").fsPath,
      sleep: (ms) => new Promise((r) => setTimeout(r, ms)),
      now: Date.now,
      fs: fsp,
      onError: (e) => log(`notify: the notification lock failed, so this window shows every notification: ${e instanceof Error ? e.message : String(e)}`),
    }),
    os: osNotifier,
    focused: () => vscode.window.state.focused,
    jamUrl,
    scheme: vscode.env.uriScheme,
    now: Date.now,
    toast: (kind, m, a) => (kind === "needs" ? vscode.window.showWarningMessage(m, a) : vscode.window.showInformationMessage(m, a)),
    goTo: (channel, session) => goToConversation(channel, session),
    log,
  });
  void needsYou.refresh();
  const offLive = live.subscribe((e) => {
    void notifier.handle(e);
    needsYou.onLive(e);
    rail?.refresh();
  });
  ctx.subscriptions.push({ dispose: offLive }, { dispose: () => osNotifier.dispose() });

  // --- Updates: Jam serves the extension it was released with (update.ts). ---
  const updateLock = new NotifyLock({
    dir: vscode.Uri.joinPath(ctx.globalStorageUri, "notify").fsPath,
    sleep: (ms) => new Promise((r) => setTimeout(r, ms)),
    now: Date.now,
    fs: fsp,
  });
  const updater = new Updater({
    version: String((ctx.extension?.packageJSON as { version?: unknown } | undefined)?.version ?? "0.0.0"),
    session: current,
    fetch: (...a) => fetch(...a),
    claim: (id) => updateLock.claim(id, vscode.window.state.focused),
    state: ctx.globalState,
    ask: (m, ...a) => vscode.window.showInformationMessage(m, ...a),
    install: async (vsix, version) => {
      const dir = vscode.Uri.joinPath(ctx.globalStorageUri, "updates");
      await fsp.mkdir(dir.fsPath, { recursive: true });
      const f = vscode.Uri.joinPath(dir, `jam-vscode_${version}.vsix`);
      await fsp.writeFile(f.fsPath, vsix);
      await vscode.commands.executeCommand("workbench.extensions.installExtension", f);
    },
    reload: () => vscode.commands.executeCommand("workbench.action.reloadWindow"),
    log,
    now: Date.now,
    setInterval,
    clearInterval: (h) => clearInterval(h as ReturnType<typeof setInterval>),
  });
  updater.start();
  ctx.subscriptions.push({ dispose: () => updater.dispose() });
  // A new jam.url means a new Jam: the permanent stream follows it.
  ctx.subscriptions.push(
    vscode.workspace.onDidChangeConfiguration((e) => {
      if (e.affectsConfiguration("jam.url") || e.affectsConfiguration("jam.urls")) {
        live.restart();
        void needsYou.refresh();
      }
    }),
  );

  const reg = (id: string, cb: (...a: unknown[]) => unknown) => ctx.subscriptions.push(vscode.commands.registerCommand(id, cb));
  reg("jam.signIn", signIn);
  reg("jam.signOut", guard(() => auth.signOut()));
  reg("jam.openSession", guard(() => pickSession(openDeps)));
  reg("jam.forgetHostKey", forget);
  reg("jam.openHub", guard(() => ui.openHub()));
  // Switch Jam: an ordinary window's Jam is picked here, then the window
  // reloads onto it. A hub or studio window belongs to its Jam.
  reg(
    "jam.switchJam",
    guard(async () => {
      const here = jamUrl();
      if (kind && typeof here === "string") return void (await vscode.window.showInformationMessage(`This window belongs to ${here}.`));
      const p = await vscode.window.showQuickPick(
        jams().list.map((u) => ({ label: u, description: u === here ? "this window" : undefined })),
        { placeHolder: "Show which Jam in this window?" },
      );
      if (!p || p.label === here) return;
      await ctx.workspaceState.update(WINDOW_JAM, p.label);
      await vscode.commands.executeCommand("workbench.action.reloadWindow");
    }),
  );

  ctx.subscriptions.push(
    vscode.window.registerUriHandler({
      handleUri(uri) {
        const r = route(uri.path, uri.query);
        if (r.kind === "auth") {
          if (!auth.handleCallback(r.query)) void vscode.window.showWarningMessage("Jam: no sign-in is waiting for that link.");
        } else if (r.kind === "hub") {
          // A launcher names its Jam; until hubs are per Jam, say so when it isn't this one.
          const want = r.jam ? normalizeJamUrl(r.jam) : undefined;
          const have = jamSettings().url;
          if (typeof want === "string" && want !== have) {
            void vscode.window.showWarningMessage(`This Jam Hub launcher is for ${want}, but jam.url is ${have instanceof Error ? "not set" : have}.`);
          }
          void guard(() => ui.openHub())();
        } else if (r.kind === "go") {
          void goToConversation(r.channel, r.session);
        } else if (r.kind === "bad") {
          void vscode.window.showWarningMessage(`Jam: ${r.reason}`);
        } else void guard(() => openSession(openDeps, r.id))();
      },
    }),
  );
}

export function deactivate(): Thenable<void> | undefined {
  reporter?.dispose();
  reporter = undefined;
  const d = onDeactivate;
  onDeactivate = undefined;
  return d?.();
}
