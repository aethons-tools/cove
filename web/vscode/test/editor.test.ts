import { describe, expect, it } from "vitest";
import { editorFor } from "../src/editor";

describe("editorFor", () => {
  it("VS Code and Insiders use Microsoft's Remote - SSH, which copies the server over", () => {
    for (const s of ["vscode", "vscode-insiders"]) {
      expect(editorFor(s)).toEqual({
        name: "VS Code",
        remoteSsh: { id: "ms-vscode-remote.remote-ssh", name: "Microsoft's Remote - SSH" },
        localServerDownload: true,
      });
    }
  });
  it("VSCodium uses Open Remote - SSH, which has no localServerDownload", () => {
    expect(editorFor("vscodium")).toEqual({
      name: "VSCodium",
      remoteSsh: { id: "jeanp413.open-remote-ssh", name: "jeanp413's Open Remote - SSH" },
      localServerDownload: false,
    });
  });
});
