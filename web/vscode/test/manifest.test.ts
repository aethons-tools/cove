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
      ["jam.forgetHostKey", "jam.openSession", "jam.signIn", "jam.signOut"],
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
        { type: "webview", id: "jam.conversation", name: "Conversation", when: "jam.window == session" },
        { type: "webview", id: "jam.status", name: "Status", when: "jam.window == session" },
        { type: "webview", id: "jam.artifacts", name: "Artifacts", visibility: "collapsed", when: "jam.window == session" },
        { type: "webview", id: "jam.previews", name: "Previews", visibility: "collapsed", when: "jam.window == session" },
      ],
      jamPanel: [{ type: "webview", id: "jam.thinking", name: "Thinking", when: "jam.window == session" }],
    });
  });

  it("no menus and no view welcome", () => {
    expect(pkg.contributes.menus).toBeUndefined();
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
