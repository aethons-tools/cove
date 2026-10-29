// The VS Code layer: the only module that imports vscode. It builds the real
// dependencies and wires the commands and the URI handler to the pure
// modules beside it.
import * as vscode from "vscode";

const notYet = () => void vscode.window.showInformationMessage("Not yet available");

export function activate(ctx: vscode.ExtensionContext): void {
  for (const id of ["jam.signIn", "jam.signOut", "jam.openSession", "jam.forgetHostKey"]) {
    ctx.subscriptions.push(vscode.commands.registerCommand(id, notYet));
  }
  ctx.subscriptions.push(vscode.window.registerUriHandler({ handleUri: notYet }));
}

export function deactivate(): void {}
