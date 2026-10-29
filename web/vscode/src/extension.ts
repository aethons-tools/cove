// The VS Code layer: the only module that imports vscode. It builds the real
// dependencies and wires the commands and the URI handler to the pure
// modules beside it.
import * as os from "node:os";
import * as vscode from "vscode";
import { SignedOutError } from "./api";
import { Auth } from "./auth";
import { settings } from "./config";
import { appTokenPath, configDir } from "./paths";
import { route } from "./uri";

const notYet = () => void vscode.window.showInformationMessage("Not yet available");

const jamSettings = () => {
  const c = vscode.workspace.getConfiguration("jam");
  return settings((k) => c.get<string>(k));
};

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

  // guard runs a command body, sending a SignedOutError to signedOut and
  // showing any other error.
  const guard = (fn: () => Promise<void>) => async (): Promise<void> => {
    try {
      await fn();
    } catch (e) {
      if (e instanceof SignedOutError) await signedOut();
      else void vscode.window.showErrorMessage(e instanceof Error ? e.message : String(e));
    }
  };

  const signIn = guard(async () => {
    const url = jamSettings().url;
    if (url instanceof Error) throw url;
    const s = await vscode.window.withProgress(
      { location: vscode.ProgressLocation.Notification, title: "Signing in to Jam in your browser…", cancellable: true },
      (_p, token) => {
        token.onCancellationRequested(() => auth.cancel());
        return auth.signIn(url);
      },
    );
    void vscode.window.showInformationMessage(`Signed in to Jam as ${s.user}`);
  });

  const reg = (id: string, cb: (...a: unknown[]) => unknown) => ctx.subscriptions.push(vscode.commands.registerCommand(id, cb));
  reg("jam.signIn", signIn);
  reg("jam.signOut", guard(() => auth.signOut()));
  reg("jam.openSession", notYet);
  reg("jam.forgetHostKey", notYet);

  ctx.subscriptions.push(
    vscode.window.registerUriHandler({
      handleUri(uri) {
        const r = route(uri.path, uri.query);
        if (r.kind === "auth") {
          if (!auth.handleCallback(r.query)) void vscode.window.showWarningMessage("Jam: no sign-in is waiting for that link.");
        } else if (r.kind === "bad") {
          void vscode.window.showWarningMessage(`Jam: ${r.reason}`);
        } else notYet();
      },
    }),
  );
}

export function deactivate(): void {}
