import { mkdtemp, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import * as path from "node:path";
import { describe, expect, it, vi } from "vitest";
import * as vscode from "vscode";
import { SignedOutError } from "../src/api";
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

  it("Sign In without jam.url shows the error", async () => {
    mock.reset();
    activate(fakeContext());
    await mock.commands.get("jam.signIn")!();
    expect(vscode.window.showErrorMessage).toHaveBeenCalledWith("Set jam.url to your Jam's address");
  });

  it("a SignedOutError clears the secret and the token file and offers Sign In", async () => {
    mock.reset();
    const home = await mkdtemp(path.join(tmpdir(), "jamext-"));
    vi.stubEnv("XDG_CONFIG_HOME", home);
    try {
      const file = path.join(home, "jam", "default-app-token.json");
      await (await import("node:fs/promises")).mkdir(path.dirname(file), { recursive: true });
      await writeFile(file, "{}");
      const ctx = fakeContext();
      ctx.secretValues.set("jam.session", "{}");
      activate(ctx);
      // jam.signIn raises a SignedOutError via a failing withProgress.
      vi.mocked(vscode.window.withProgress).mockRejectedValueOnce(new SignedOutError());
      mock.config["jam.url"] = "https://jam.example.com";
      mock.answers = ["Sign In"];
      await mock.commands.get("jam.signIn")!();
      expect(ctx.secretValues.has("jam.session")).toBe(false);
      await expect(stat(file)).rejects.toThrow();
      expect(vscode.window.showWarningMessage).toHaveBeenCalledWith("Your Jam sign-in expired or was revoked.", "Sign In");
      expect(vscode.commands.executeCommand).toHaveBeenCalledWith("jam.signIn");
    } finally {
      vi.unstubAllEnvs();
    }
  });

  it("a crafted session link is refused with no API call", async () => {
    mock.reset();
    const fetchSpy = vi.spyOn(globalThis, "fetch");
    activate(fakeContext());
    mock.config["jam.url"] = "https://jam.example.com";
    for (const path of ["/session/ses_x%0AHost%20*", "/session/../../x", "/other"]) {
      mock.uriHandler!.handleUri({ path, query: "" });
    }
    await new Promise((r) => setTimeout(r, 10));
    expect(fetchSpy).not.toHaveBeenCalled();
    expect(vscode.window.showWarningMessage).toHaveBeenCalledTimes(3);
    fetchSpy.mockRestore();
  });

  it("a 401 while opening a session signs out and a new sign-in works", async () => {
    mock.reset();
    const home = await mkdtemp(path.join(tmpdir(), "jamext-"));
    vi.stubEnv("XDG_CONFIG_HOME", home);
    const fetchSpy = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response("", { status: 401 }));
    try {
      const file = path.join(home, "jam", "default-app-token.json");
      await (await import("node:fs/promises")).mkdir(path.dirname(file), { recursive: true });
      await writeFile(file, "{}");
      const ctx = fakeContext();
      mock.config["jam.url"] = "https://jam.example.com";
      ctx.secretValues.set("jam.session", JSON.stringify({ url: "https://jam.example.com", token: "t", user: "u", expiresAt: "x" }));
      activate(ctx);
      mock.answers = ["Sign In"];
      mock.uriHandler!.handleUri({ path: "/session/ses_0abcdefghjkmnpqrstvwxyz012", query: "" });
      await vi.waitFor(() => expect(vscode.commands.executeCommand).toHaveBeenCalledWith("jam.signIn"));
      expect(ctx.secretValues.has("jam.session")).toBe(false);
      await expect(stat(file)).rejects.toThrow();
      expect(fetchSpy).toHaveBeenCalledTimes(1);
    } finally {
      fetchSpy.mockRestore();
      vi.unstubAllEnvs();
    }
  });
});
