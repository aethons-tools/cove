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
import { openSession, pickSession, studioFromAuthority, windowAuthority } from "./open";
import type { OpenDeps, Ui } from "./open";
import { appTokenPath, configDir, knownHostsFile, sshConfigFile } from "./paths";
import { findSshKeygen } from "./ssh/cli";
import { listJamStudios } from "./ssh/config";
import type { ExecFile } from "./ssh/keys";
import { ensureSsh } from "./ssh/setup";
import { SESSION_ID_RE, route } from "./uri";
import { readFile } from "node:fs/promises";
import { Router } from "./bridge/router";
import { FocusModel, focusSessionCommand, sessionResolver } from "./focus";
import { FocusReporter } from "./focusreport";
import { LiveStream } from "./live";
import { ConversationViewProvider } from "./panes/conversation";
import { perform } from "./panes/actions";
import type { ActionDeps } from "./panes/actions";
import { PaneHub } from "./panes/hub";

const run: ExecFile = (file, args) => promisify(execFile)(file, args).then((r) => ({ stdout: String(r.stdout), stderr: String(r.stderr) }));

const jamSettings = () => {
  const c = vscode.workspace.getConfiguration("jam");
  return settings((k) => c.get<string>(k));
};

let reporter: FocusReporter | undefined;

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

  const ui: Ui = {
    error: async (m, ...a) => vscode.window.showErrorMessage(m, ...a),
    info: async (m, ...a) => vscode.window.showInformationMessage(m, ...a),
    pick: async (items, placeholder) => vscode.window.showQuickPick(items, { placeHolder: placeholder }),
    openExternal: async (u) => void (await vscode.env.openExternal(vscode.Uri.parse(u))),
    openFolder: async (u) => void (await vscode.commands.executeCommand("vscode.openFolder", vscode.Uri.parse(u), { forceNewWindow: true })),
    hasRemoteSsh: () => vscode.extensions.getExtension("ms-vscode-remote.remote-ssh") !== undefined,
    installRemoteSsh: async () => void (await vscode.commands.executeCommand("workbench.extensions.installExtension", "ms-vscode-remote.remote-ssh")),
    getGlobal: (section, key) => vscode.workspace.getConfiguration(section).inspect(key)?.globalValue,
    setGlobal: (section, key, v) => Promise.resolve(vscode.workspace.getConfiguration(section).update(key, v, vscode.ConfigurationTarget.Global)),
    remembered: (k) => ctx.globalState.get<boolean>(k) === true,
    remember: async (k) => ctx.globalState.update(k, true),
    windowAuthority: () => windowAuthority(vscode.workspace),
  };

  // The API is built only from auth.session(currentJamUrl): the stored session,
  // else a fresh sign-in.
  let signedInUrl = "";
  const openDeps: OpenDeps = {
    ui,
    signedIn: async () => {
      const url = jamSettings().url;
      if (url instanceof Error) throw url;
      const s = (await auth.session(url)) ?? (await signInWithProgress(url));
      await auth.syncTokenFile(s);
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

  // --- The Conversation view and what it shares with the window. ---
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
  const router = new Router({ session: current, fetch: (...a) => fetch(...a), onSignedOut: panesSignedOut });
  const windowDefault = studioFromAuthority(windowAuthority(vscode.workspace));
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

  // Signing in reconnects the stream with the new token; signing out
  // reconnects it to nothing, so the old token is no longer used.
  const offAuth = auth.onChange((s) => {
    if (s) signingOut = false;
    live.restart();
  });
  ctx.subscriptions.push({ dispose: offAuth });

  const exec = (c: string, ...a: unknown[]) => vscode.commands.executeCommand(c, ...a);
  const actionDeps: ActionDeps = {
    openExternal: (u) => vscode.env.openExternal(vscode.Uri.parse(u)),
    executeCommand: exec,
    asExternalUri: async (u) => (await vscode.env.asExternalUri(vscode.Uri.parse(u))).toString(),
    // Until the board editor tab (Task 7), a board opens in the browser.
    openBoard: (id) => {
      const u = jamUrl();
      if (!(u instanceof Error)) void vscode.env.openExternal(vscode.Uri.parse(`${u}/app/user/boards/${id}`));
    },
    openSession: (id) => openSession(openDeps, id),
    scrollConversation: (anchor) => conversation.scrollTo(anchor),
    error: (m) => void vscode.window.showErrorMessage(m),
  };
  const conversation: ConversationViewProvider = new ConversationViewProvider({
    extensionUri: ctx.extensionUri,
    hub,
    router,
    model,
    reporter: rep,
    auth,
    jamUrl,
    perform: (a) => perform(a, actionDeps),
    windowStudio: () => studioFromAuthority(windowAuthority(vscode.workspace)),
    executeCommand: exec,
      });
  ctx.subscriptions.push(vscode.window.registerWebviewViewProvider(ConversationViewProvider.viewType, conversation));

  const view = ConversationViewProvider.viewType;
  const reg = (id: string, cb: (...a: unknown[]) => unknown) => ctx.subscriptions.push(vscode.commands.registerCommand(id, cb));
  reg("jam.signIn", signIn);
  reg("jam.signOut", guard(() => auth.signOut()));
  reg("jam.openSession", guard(() => pickSession(openDeps)));
  reg("jam.forgetHostKey", forget);
  reg(
    "jam.focusSession",
    guard(async () => {
      const { api } = await openDeps.signedIn();
      await focusSessionCommand({ api, ui, model, hasDefault: windowDefault !== undefined });
    }),
  );
  reg("jam.conversation.pin", guard(async () => void (model.isPinned(view) || (await model.togglePin(view)))));
  reg("jam.conversation.unpin", guard(async () => void (model.isPinned(view) && (await model.togglePin(view)))));

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

export function deactivate(): void {
  reporter?.dispose();
  reporter = undefined;
}
