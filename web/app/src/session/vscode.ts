// vscodeLink opens session id's studio in VS Code through the Jam extension
// (web/vscode; docs/usage/jam/vscode.md).
export const vscodeLink = (id: string) => `vscode://aethons.jam/session/${encodeURIComponent(id)}`;
