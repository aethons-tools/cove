import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import * as vscode from "vscode";
import { activate } from "../src/extension";
import { fakeContext, mock } from "./vscode";

const NAMES = ["conversation", "thinking", "previews", "status", "artifacts"];
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
    expect(pkg.activationEvents).toEqual(["onUri", "onWebviewPanel:jam.board", "onStartupFinished"]);
  });

  it("activationEvents include onStartupFinished", () => {
    expect(pkg.activationEvents).toContain("onStartupFinished");
  });

  it("every contributed command is registered by activate", () => {
    mock.reset();
    activate(fakeContext());
    const registered = vi_registered();
    const ids = pkg.contributes.commands.map((c: { command: string }) => c.command);
    expect(ids.sort()).toEqual(
      ["jam.connectStudio", "jam.disconnectStudio", "jam.focusSession", "jam.forgetHostKey", "jam.needsYou.open", "jam.openSession", "jam.signIn", "jam.signOut", ...NAMES.flatMap((n) => [`jam.${n}.pin`, `jam.${n}.unpin`])].sort(),
    );
    for (const id of ids) expect(registered).toContain(id);
  });

  it("contributes the five views, with Thinking in the jamPanel container", () => {
    expect(pkg.contributes.viewsContainers).toEqual({
      activitybar: [{ id: "jam", title: "Jam", icon: "media/jam.svg" }],
      panel: [{ id: "jamPanel", title: "Jam Thinking", icon: "media/jam.svg" }],
    });
    expect(pkg.icon).toBe("media/icon.png");
    expect(pkg.contributes.views).toEqual({
      jam: [
        { type: "webview", id: "jam.conversation", name: "Conversation" },
        { type: "webview", id: "jam.status", name: "Status" },
        { type: "webview", id: "jam.artifacts", name: "Artifacts", visibility: "collapsed" },
        { type: "webview", id: "jam.previews", name: "Previews", visibility: "collapsed" },
        { id: "jam.needsYou", name: "Needs You", visibility: "collapsed" },
      ],
      jamPanel: [{ type: "webview", id: "jam.thinking", name: "Thinking" }],
    });
    const cmd = (id: string) => pkg.contributes.commands.find((c: { command: string }) => c.command === id);
    expect(cmd("jam.focusSession")).toMatchObject({ title: "Focus Session…", category: "Jam" });
    expect(cmd("jam.connectStudio")).toEqual({ command: "jam.connectStudio", title: "Connect to Studio", category: "Jam" });
    expect(cmd("jam.disconnectStudio")).toEqual({ command: "jam.disconnectStudio", title: "Disconnect Studio", category: "Jam" });
    for (const n of NAMES) {
      expect(cmd(`jam.${n}.pin`)).toEqual({ command: `jam.${n}.pin`, title: "Pin to This Session", icon: "$(pin)" });
      expect(cmd(`jam.${n}.unpin`)).toEqual({ command: `jam.${n}.unpin`, title: "Follow the Window's Session", icon: "$(pinned)" });
    }
  });

  it("jam.needsYou is a collapsed tree view in the jam container, with two welcome entries", () => {
    const v = pkg.contributes.views.jam.find((x: { id: string }) => x.id === "jam.needsYou");
    expect(v).toEqual({ id: "jam.needsYou", name: "Needs You", visibility: "collapsed" });
    expect(pkg.contributes.views.jam.at(-1)).toBe(v);
    expect(v.type).toBeUndefined();
    expect(pkg.contributes.viewsWelcome).toEqual([
      { view: "jam.needsYou", contents: "Sign in to Jam to see what needs you.\n[Sign In](command:jam.signIn)", when: "!jam.signedIn" },
      { view: "jam.needsYou", contents: "Nothing needs you right now.", when: "jam.signedIn" },
    ]);
    expect(pkg.contributes.menus.commandPalette).toContainEqual({ command: "jam.needsYou.open", when: "false" });
  });

  it("the ten pin commands: pin only with a session and unpinned, unpin only when pinned, none in the palette", () => {
    expect(pkg.contributes.menus["view/title"]).toEqual(
      NAMES.flatMap((n) => [
        { command: `jam.${n}.pin`, when: `view == jam.${n} && jam.${n}.hasSession && !jam.${n}.pinned`, group: "navigation" },
        { command: `jam.${n}.unpin`, when: `view == jam.${n} && jam.${n}.pinned`, group: "navigation" },
      ]),
    );
    expect(pkg.contributes.menus.commandPalette).toEqual([
      ...NAMES.flatMap((n) => [
        { command: `jam.${n}.pin`, when: "false" },
        { command: `jam.${n}.unpin`, when: "false" },
      ]),
      { command: "jam.needsYou.open", when: "false" },
      { command: "jam.connectStudio", when: "jam.window == local" },
      { command: "jam.disconnectStudio", when: "jam.window == remote" },
    ]);
  });

  // The activity bar and panel tint their icon, so jam.svg is the J mark in
  // currentColor; jam-color.svg (board tabs) is the brand mark, j-logo-plain.svg.
  it("media/jam.svg is the J mark, monochrome", () => {
    const svg = readFileSync(join(__dirname, "..", "media", "jam.svg"), "utf8");
    expect(svg).toContain('viewBox="0 0 144 144"');
    expect(svg.match(/fill="currentColor"/g)).toHaveLength(2);
    expect(svg).not.toMatch(/#[0-9a-f]{3,6}/i);
    expect(svg.match(/<path|<circle/g)).toHaveLength(2);
  });

  it("media/jam-color.svg is the brand J mark", () => {
    const brand = readFileSync(join(__dirname, "..", "..", "..", "internal", "jam", "uiassets", "static", "brand", "j-logo-plain.svg"), "utf8");
    expect(readFileSync(join(__dirname, "..", "media", "jam-color.svg"), "utf8")).toBe(brand);
  });

  // vsce applies every "!" after every exclusion, so "!dist/**" would also
  // ship the .vsix being written into dist/: name what dist/ ships instead.
  it(".vscodeignore ships dist (the extension and the webview) and media/**, not an old .vsix", () => {
    const lines = readFileSync(join(__dirname, "..", ".vscodeignore"), "utf8").split("\n").map((l) => l.trim()).filter(Boolean);
    expect(lines).toEqual(["**", "!dist/extension.js", "!dist/webview/**", "!dist/vendor/**", "!media/**", "!package.json", "!README.md"]);
  });
});

function vi_registered(): string[] {
  return (vscode.commands.registerCommand as unknown as { mock: { calls: unknown[][] } }).mock.calls.map((c) => c[0] as string);
}
