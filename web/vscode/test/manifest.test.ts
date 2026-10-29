import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import * as vscode from "vscode";
import { activate } from "../src/extension";
import { fakeContext, mock } from "./vscode";

const pkg = JSON.parse(readFileSync(join(__dirname, "..", "package.json"), "utf8"));

describe("the manifest", () => {
  it("scopes both settings to the application, so a workspace can't set them (Review Focus 1)", () => {
    const props = pkg.contributes.configuration.properties;
    expect(Object.keys(props).sort()).toEqual(["jam.cliPath", "jam.url"]);
    for (const k of ["jam.url", "jam.cliPath"]) {
      expect(props[k].scope).toBe("application");
      expect(props[k].type).toBe("string");
      expect(props[k].default).toBe("");
    }
  });

  it("is the UI-side aethons.jam extension for VS Code 1.105+", () => {
    expect(pkg.extensionKind).toEqual(["ui"]);
    expect(pkg.publisher).toBe("aethons");
    expect(pkg.name).toBe("jam");
    expect(pkg.displayName).toBe("Jam");
    expect(pkg.engines.vscode).toBe("^1.105.0");
    expect(pkg.main).toBe("./dist/extension.js");
    expect(pkg.activationEvents).toEqual(["onUri"]);
  });

  it("registers every contributed command in activate", () => {
    mock.reset();
    activate(fakeContext());
    const registered = vi_registered();
    const ids = pkg.contributes.commands.map((c: { command: string }) => c.command);
    expect(ids.sort()).toEqual([
      "jam.conversation.pin",
      "jam.conversation.unpin",
      "jam.focusSession",
      "jam.forgetHostKey",
      "jam.openSession",
      "jam.signIn",
      "jam.signOut",
    ]);
    for (const id of ids) expect(registered).toContain(id);
  });

  it("contributes the Jam activity-bar container with the Conversation webview view", () => {
    expect(pkg.contributes.viewsContainers).toEqual({ activitybar: [{ id: "jam", title: "Jam", icon: "media/jam.svg" }] });
    expect(pkg.contributes.views).toEqual({ jam: [{ type: "webview", id: "jam.conversation", name: "Conversation" }] });
    const cmd = (id: string) => pkg.contributes.commands.find((c: { command: string }) => c.command === id);
    expect(cmd("jam.focusSession")).toMatchObject({ title: "Focus Session…", category: "Jam" });
    expect(cmd("jam.conversation.pin")).toMatchObject({ title: "Pin to This Session", icon: "$(pin)" });
    expect(cmd("jam.conversation.unpin")).toMatchObject({ title: "Follow the Window's Session", icon: "$(pinned)" });
  });

  it("shows pin only with a session and unpinned, unpin only when pinned, neither in the palette", () => {
    expect(pkg.contributes.menus["view/title"]).toEqual([
      { command: "jam.conversation.pin", when: "view == jam.conversation && jam.conversation.hasSession && !jam.conversation.pinned", group: "navigation" },
      { command: "jam.conversation.unpin", when: "view == jam.conversation && jam.conversation.pinned", group: "navigation" },
    ]);
    expect(pkg.contributes.menus.commandPalette).toEqual([
      { command: "jam.conversation.pin", when: "false" },
      { command: "jam.conversation.unpin", when: "false" },
    ]);
  });

  it("media/jam.svg is a monochrome 24x24 mark", () => {
    const svg = readFileSync(join(__dirname, "..", "media", "jam.svg"), "utf8");
    expect(svg).toContain('viewBox="0 0 24 24"');
    expect(svg).toContain('fill="currentColor"');
    expect(svg.match(/<path/g)).toHaveLength(1);
  });

  // vsce applies every "!" after every exclusion, so "!dist/**" would also
  // ship the .vsix being written into dist/: name what dist/ ships instead.
  it(".vscodeignore ships dist (the extension and the webview) and media/**, not an old .vsix", () => {
    const lines = readFileSync(join(__dirname, "..", ".vscodeignore"), "utf8").split("\n").map((l) => l.trim()).filter(Boolean);
    expect(lines).toEqual(["**", "!dist/extension.js", "!dist/webview/**", "!media/**", "!package.json", "!README.md"]);
  });
});

function vi_registered(): string[] {
  return (vscode.commands.registerCommand as unknown as { mock: { calls: unknown[][] } }).mock.calls.map((c) => c[0] as string);
}
