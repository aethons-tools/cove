import { mkdtemp, readFile, stat, writeFile } from "node:fs/promises";
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

  it("Forget Host Key in a remote window uses the workspace folder's authority", async () => {
    mock.reset();
    const home = await mkdtemp(path.join(tmpdir(), "jamext-"));
    const id = "ses_0abcdefghjkmnpqrstvwxyz012";
    const bin = path.join(home, "bin");
    const out = path.join(home, "args");
    const fsp = await import("node:fs/promises");
    await fsp.mkdir(bin);
    await fsp.mkdir(path.join(home, ".ssh"));
    await writeFile(path.join(home, ".ssh", "known_hosts"), "x\n");
    await writeFile(path.join(bin, "ssh-keygen"), `#!/bin/sh\necho "$@" > ${out}\n`, { mode: 0o755 });
    vi.stubEnv("HOME", home);
    vi.stubEnv("PATH", bin);
    try {
      (vscode.workspace as { workspaceFolders: unknown }).workspaceFolders = [{ uri: { scheme: "vscode-remote", authority: `ssh-remote+jam-${id}` } }];
      activate(fakeContext());
      mock.answers = ["Forget"];
      await mock.commands.get("jam.forgetHostKey")!();
      expect(vscode.window.showErrorMessage).not.toHaveBeenCalled();
      expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(expect.stringContaining(`jam-${id}?`), "Forget");
      expect((await fsp.readFile(out, "utf8")).trim()).toBe(`-R jam-${id} -f ${path.join(home, ".ssh", "known_hosts")}`);
    } finally {
      vi.unstubAllEnvs();
    }
  });

  it("opening a session re-syncs a missing token file from the stored session", async () => {
    mock.reset();
    const home = await mkdtemp(path.join(tmpdir(), "jamext-"));
    vi.stubEnv("XDG_CONFIG_HOME", home);
    const fetchSpy = vi.spyOn(globalThis, "fetch").mockRejectedValue(new Error("offline"));
    try {
      const file = path.join(home, "jam", "default-app-token.json");
      const ctx = fakeContext();
      mock.config["jam.url"] = "https://jam.example.com";
      ctx.secretValues.set("jam.session", JSON.stringify({ url: "https://jam.example.com", token: "t", user: "u", expiresAt: "x" }));
      activate(ctx);
      mock.uriHandler!.handleUri({ path: "/session/ses_0abcdefghjkmnpqrstvwxyz012", query: "" });
      await vi.waitFor(() => expect(vscode.window.showErrorMessage).toHaveBeenCalledWith("offline"));
      expect(JSON.parse(await readFile(file, "utf8"))).toEqual({ token: "t", jam_url: "https://jam.example.com" });
      expect((await stat(file)).mode & 0o777).toBe(0o600);
    } finally {
      fetchSpy.mockRestore();
      vi.unstubAllEnvs();
    }
  });

  it("a failing sign-out after a 401 is shown, not lost", async () => {
    mock.reset();
    const home = await mkdtemp(path.join(tmpdir(), "jamext-"));
    vi.stubEnv("XDG_CONFIG_HOME", home);
    const fetchSpy = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response("", { status: 401 }));
    try {
      const file = path.join(home, "jam", "default-app-token.json");
      const ctx = fakeContext();
      mock.config["jam.url"] = "https://jam.example.com";
      ctx.secretValues.set("jam.session", JSON.stringify({ url: "https://jam.example.com", token: "t", user: "u", expiresAt: "x" }));
      ctx.secrets.delete = async () => {
        throw new Error("keyring locked");
      };
      activate(ctx);
      mock.uriHandler!.handleUri({ path: "/session/ses_0abcdefghjkmnpqrstvwxyz012", query: "" });
      await vi.waitFor(() => expect(vscode.window.showErrorMessage).toHaveBeenCalledWith("keyring locked"));
      await expect(stat(file)).rejects.toThrow();
    } finally {
      fetchSpy.mockRestore();
      vi.unstubAllEnvs();
    }
  });
});
