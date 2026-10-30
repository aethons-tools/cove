// vscodeLink and vscodiumLink open session id's studio in VS Code or
// VSCodium through the Jam extension (web/vscode; docs/usage/jam/vscode.md).
export const vscodeLink = (id: string) => `vscode://aethons.jam/session/${encodeURIComponent(id)}`;
export const vscodiumLink = (id: string) => `vscodium://aethons.jam/session/${encodeURIComponent(id)}`;
