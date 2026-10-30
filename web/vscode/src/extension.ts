// The VS Code layer: the only module that imports vscode. It builds the real
// dependencies and wires the commands and the URI handler to the pure
// modules beside it.
import { execFile } from "node:child_process";
import * as os from "node:os";
import { promisify } from "node:util";
import * as vscode from "vscode";
import { JamApi, SignedOutError } from "./api";
import { Auth } from "./auth";
import { settings } from "./config";
import { forgetHostKey } from "./hostkey";
import {
  CONNECTED_BEAT_MS,
  connectStudio,
  connectedSet,
  disconnectStudio,
  openSession,
  pickSession,
  SESSION_WORKSPACE,
  sessionOfPick,
  statusBarItem,
  studioFromAuthority,
  webUrl,
  windowAuthority,
  windowKind,
} from "./open";
import type { RailRow } from "./open";
import type { OpenDeps, Ui } from "./open";
import { appTokenPath, configDir, knownHostsFile, sshConfigFile } from "./paths";
import { findSshKeygen } from "./ssh/cli";
import { listJamStudios } from "./ssh/config";
import type { ExecFile } from "./ssh/keys";
import { ensureSsh } from "./ssh/setup";
import { SESSION_ID_RE, route } from "./uri";
import { readFile } from "node:fs/promises";
import * as fsp from "node:fs/promises";
import { Router } from "./bridge/router";
import { FocusModel, focusSessionCommand, sessionResolver } from "./focus";
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
import { ThinkingStreams } from "./thinking";
import { Updater } from "./update";

const run: ExecFile = (file, args) => promisify(execFile)(file, args).then((r) => ({ stdout: String(r.stdout), stderr: String(r.stderr) }));
// The OS notifier's execFile: no shell, argv only, with a timeout and an abort signal.
const runNotify: OsDeps["execFile"] = (file, args, o) =>
  promisify(execFile)(file, args, { timeout: o.timeout, signal: o.signal }).then((r) => ({ stdout: String(r.stdout), stderr: String(r.stderr) }));

const jamSettings = () => {
  const c = vscode.workspace.getConfiguration("jam");
  return settings((k) => c.get<string>(k));
};

let reporter: FocusReporter | undefined;
// Run on deactivate: a remote session window's best-effort "no longer connected".
let onDeactivate: (() => Thenable<void>) | undefined;

export function activate(ctx: vscode.ExtensionContext): void {
  const auth = new Auth({
    secrets: ctx.secrets,
    openExternal: (u) => vscode.env.openExternal(vscode.Uri.parse(u)),
    fetch: (...a) => fetch(...a),
    uriScheme: vscode.env.uriScheme,
    hostname: os.hostname(),
    tokenFile: appTokenPath(configDir(process.env, os.homedir())),
  });

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
    const url = jamSettings().url;
    if (url instanceof Error) throw url;
    const s = await signInWithProgress(url);
    void vscode.window.showInformationMessage(`Signed in to Jam as ${s.user}`);
  });

  const output = vscode.window.createOutputChannel("Jam");
  ctx.subscriptions.push(output);
  const log = (m: string) => output.appendLine(m);

  // A session's local window is a workspace file in global storage, holding
  // no folders and nothing secret.
  const sessionsDir = vscode.Uri.joinPath(ctx.globalStorageUri, "sessions");
  const localWorkspace = async (id: string): Promise<string> => {
    const f = vscode.Uri.joinPath(sessionsDir, `${id}.code-workspace`);
    await fsp.mkdir(sessionsDir.fsPath, { recursive: true });
    await fsp.writeFile(f.fsPath, SESSION_WORKSPACE, { flag: "wx" }).catch((e: NodeJS.ErrnoException) => {
      if (e.code !== "EEXIST") throw e;
    });
    return f.toString();
  };
  const connected = connectedSet(ctx.globalState);

  const ui: Ui = {
    error: async (m, ...a) => vscode.window.showErrorMessage(m, ...a),
    info: async (m, ...a) => vscode.window.showInformationMessage(m, ...a),
    pick: async (items, placeholder) => vscode.window.showQuickPick(items, { placeHolder: placeholder }),
    openExternal: async (u) => void (await vscode.env.openExternal(vscode.Uri.parse(u))),
    // openFolder keeps the current window's remote authority unless told
    // otherwise, so a local workspace opened from a studio window would stay on
    // its SSH connection: anything not vscode-remote opens forceLocalWindow.
    openFolder: async (u, o) => {
      const uri = vscode.Uri.parse(u);
      const local = uri.scheme === "vscode-remote" ? {} : { forceLocalWindow: true };
      await vscode.commands.executeCommand("vscode.openFolder", uri, { ...(o?.reuse ? { forceReuseWindow: true } : { forceNewWindow: true }), ...local });
    },
    localWorkspace,
    isConnected: connected.isConnected,
    setConnected: connected.setConnected,
    hasRemoteSsh: () => vscode.extensions.getExtension("ms-vscode-remote.remote-ssh") !== undefined,
    installRemoteSsh: async () => void (await vscode.commands.executeCommand("workbench.extensions.installExtension", "ms-vscode-remote.remote-ssh")),
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
    log,
    signedIn: async () => {
      const url = jamSettings().url;
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
  const jamUrl = (): string | Error => jamSettings().url;
  // The current sign-in for jam.url, read fresh on every use: tokens come only
  // from auth.session(currentJamUrl).
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
  // The window's session: a jam-<id> remote, or a local session window. Only
  // the remote (windowStudio) can forward a preview's port.
  const kind = windowKind(vscode.workspace, sessionsDir, process.platform);
  log(`window: ${kind ? `${kind.kind} session ${kind.id}` : "no session"} (workspace file ${vscode.workspace.workspaceFile?.toString() ?? "none"}, sessions dir ${sessionsDir.toString()})`);
  const windowDefault = kind?.id;
  const windowStudio = kind?.kind === "remote" ? kind.id : undefined;
  void vscode.commands.executeCommand("setContext", "jam.window", kind?.kind ?? "");
  // The Connect/Disconnect item shows only while signed in.
  const item = statusBarItem(kind);
  if (item) {
    const sb = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Left, 100);
    Object.assign(sb, item);
    const showIf = (on: boolean) => (on ? sb.show() : sb.hide());
    void current().then((s) => showIf(s !== undefined), () => showIf(false));
    const off = auth.onChange((s) => showIf(s !== undefined));
    ctx.subscriptions.push(sb, { dispose: off });
  }
  if (kind?.kind === "remote") {
    // Reloading a remote window deactivates and reactivates it: mark it again.
    // The mark is refreshed while the window lives, so one it never got to
    // clear (closed, crashed) goes stale.
    const id = kind.id;
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
    windowDefault,
    state: ctx.workspaceState,
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

  // The Needs You view and the badge on the Jam activity-bar icon.
  let treeView: vscode.TreeView<RailRow> | undefined;
  const needsYou = new NeedsYou({
    session: current,
    api: (s) => new JamApi(s.url, s.token, (...a) => fetch(...a)),
    setBadge: (b) => {
      if (treeView) treeView.badge = b;
    },
    setContext: (k, v) => void vscode.commands.executeCommand("setContext", k, v),
    onSignedOut: panesSignedOut,
    setTimeout,
    clearTimeout,
  });
  treeView = vscode.window.createTreeView("jam.needsYou", { treeDataProvider: needsYou });
  ctx.subscriptions.push(treeView, { dispose: () => needsYou.dispose() });

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
    scrollConversation: (anchor) => providers.get("conversation")!.scrollTo(anchor),
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
    openSession: (id) => guard(() => openSession(openDeps, id))(),
    openExternal: (u) => vscode.env.openExternal(vscode.Uri.parse(u)),
    log,
  });
  void needsYou.refresh();
  const offLive = live.subscribe((e) => {
    void notifier.handle(e);
    needsYou.onLive(e);
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
      if (e.affectsConfiguration("jam.url")) {
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
  const notSessionWindow = async () =>
    void (await vscode.window.showInformationMessage("This window isn't a Jam session window. Open a session first (Jam: Open Session…)."));
  reg(
    "jam.connectStudio",
    guard(async () => (kind?.kind === "local" ? connectStudio(openDeps, kind.id) : notSessionWindow())),
  );
  reg(
    "jam.disconnectStudio",
    guard(async () => (kind?.kind === "remote" ? disconnectStudio(openDeps, kind.id) : notSessionWindow())),
  );
  reg(
    "jam.focusSession",
    guard(async () => {
      const { api } = await openDeps.signedIn();
      await focusSessionCommand({ api, ui, model, hasDefault: windowDefault !== undefined });
    }),
  );
  // A needs-you row opens its session's code window; a chat opens the web app.
  reg("jam.needsYou.open", (row) =>
    guard(async () => {
      const r = row as RailRow;
      const { api, url } = await openDeps.signedIn();
      const session = await sessionOfPick(api, { label: r.label, row: r });
      if (session) await openSession(openDeps, session);
      else await ui.openExternal(webUrl(url, r.id));
    })(),
  );
  for (const name of SESSION_PANES) {
    const view = `jam.${name}`;
    reg(`${view}.pin`, guard(async () => void (model.isPinned(view) || (await model.togglePin(view)))));
    reg(`${view}.unpin`, guard(async () => void (model.isPinned(view) && (await model.togglePin(view)))));
  }

  ctx.subscriptions.push(
    vscode.window.registerUriHandler({
      handleUri(uri) {
        const r = route(uri.path, uri.query);
        if (r.kind === "auth") {
          if (!auth.handleCallback(r.query)) void vscode.window.showWarningMessage("Jam: no sign-in is waiting for that link.");
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
