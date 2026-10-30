// The editor the extension runs in, from vscode.env.uriScheme: VS Code (or
// Insiders) connects a studio with Microsoft's Remote - SSH, VSCodium with
// Open Remote - SSH (Microsoft's is licensed to VS Code only). Pure.
export type Editor = {
  name: string;
  remoteSsh: { id: string; name: string };
  // Whether its Remote-SSH has localServerDownload (download the server here, copy it over).
  localServerDownload: boolean;
};

const VSCODE: Editor = {
  name: "VS Code",
  remoteSsh: { id: "ms-vscode-remote.remote-ssh", name: "Microsoft's Remote - SSH" },
  localServerDownload: true,
};

const VSCODIUM: Editor = {
  name: "VSCodium",
  remoteSsh: { id: "jeanp413.open-remote-ssh", name: "jeanp413's Open Remote - SSH" },
  localServerDownload: false,
};

export const editorFor = (uriScheme: string): Editor => (uriScheme === "vscodium" ? VSCODIUM : VSCODE);
