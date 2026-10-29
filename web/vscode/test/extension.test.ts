import { describe, expect, it } from "vitest";
import * as vscode from "vscode";
import { activate, deactivate } from "../src/extension";
import { fakeContext, mock } from "./vscode";

describe("activate", () => {
  it("registers four commands and a uri handler", () => {
    mock.reset();
    const ctx = fakeContext();
    activate(ctx);
    expect(vscode.commands.registerCommand).toHaveBeenCalledTimes(4);
    expect(vscode.window.registerUriHandler).toHaveBeenCalledTimes(1);
    expect(ctx.subscriptions.length).toBe(5);
    deactivate();
  });
});
