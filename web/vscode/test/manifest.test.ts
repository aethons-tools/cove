import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import * as vscode from "vscode";
import { activate } from "../src/extension";
import { fakeContext, mock } from "./vscode";

const pkg = JSON.parse(readFileSync(join(__dirname, "..", "package.json"), "utf8"));

describe("the manifest", () => {
  it("scopes every setting to the application, so a workspace can't set them (Review Focus 1)", () => {
    const props = pkg.contributes.configuration.properties;
    expect(Object.keys(props).sort()).toEqual(["jam.cliPath", "jam.url", "jam.urls"]);
    expect(props["jam.urls"].scope).toBe("application");
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
    expect(pkg.activationEvents).toEqual(["onUri", "onWebviewPanel:jam.board", "onWebviewPanel:jam.conversationTab", "onStartupFinished"]);
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
      ["jam.addJam", "jam.forgetHostKey", "jam.openHub", "jam.openSession", "jam.signIn", "jam.signOut", "jam.switchJam"],
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
        { type: "webview", id: "jam.rail", name: "Conversations" },
        { type: "webview", id: "jam.status", name: "Session Status", when: "jam.window == session || jam.window == hub" },
        { type: "webview", id: "jam.artifacts", name: "Artifacts", visibility: "collapsed", when: "jam.window == session || jam.window == hub" },
        { type: "webview", id: "jam.previews", name: "Previews", visibility: "collapsed", when: "jam.window == session || jam.window == hub" },
      ],
      jamPanel: [{ type: "webview", id: "jam.thinking", name: "Thinking", when: "jam.window == session || jam.window == hub" }],
    });
  });

  it("jam.urls is an application-scoped list of more Jams", () => {
    expect(pkg.contributes.configuration.properties["jam.urls"]).toEqual({
      type: "array",
      items: { type: "string" },
      default: [],
      scope: "application",
      markdownDescription: expect.any(String),
    });
  });

  it("Open Hub is a Jam command", () => {
    expect(pkg.contributes.commands).toContainEqual({ command: "jam.openHub", title: "Open Hub", category: "Jam" });
  });

  it("the palette shows Switch Jam only with several Jams; no view welcome", () => {
    expect(pkg.contributes.menus).toEqual({
      commandPalette: [{ command: "jam.switchJam", when: "jam.multiJam" }],
      "view/title": [{ command: "jam.switchJam", when: "view == jam.rail && jam.multiJam && jam.window == ''", group: "navigation" }],
    });
    expect(pkg.contributes.commands).toContainEqual({ command: "jam.switchJam", title: "Switch Jam…", category: "Jam", icon: "$(arrow-swap)" });
    expect(pkg.contributes.viewsWelcome).toBeUndefined();
  });
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
