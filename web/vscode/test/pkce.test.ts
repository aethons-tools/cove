import { describe, expect, it } from "vitest";
import { challengeFor, consentUrl, newPkce, redirectFor } from "../src/pkce";

describe("pkce", () => {
  it("challengeFor matches the RFC 7636 vector", () => {
    expect(challengeFor("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk")).toBe("E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM");
  });
  it("newPkce gives a 43-char verifier and distinct states", () => {
    const a = newPkce();
    expect(a.verifier).toMatch(/^[A-Za-z0-9_-]{43}$/);
    expect(a.challenge).toBe(challengeFor(a.verifier));
    expect(newPkce().state).not.toBe(a.state);
  });
  it("consentUrl is exact", () => {
    expect(consentUrl("https://jam.example.com", { redirect: "vscode://aethons.jam/auth", state: "S", challenge: "C", client: "VS Code on host" })).toBe(
      "https://jam.example.com/app/auth/app-token?redirect=vscode%3A%2F%2Faethons.jam%2Fauth&state=S&code_challenge=C&code_challenge_method=S256&client=VS+Code+on+host",
    );
  });
  it("redirectFor accepts vscode, insiders and vscodium only", () => {
    expect(redirectFor("vscode")).toBe("vscode://aethons.jam/auth");
    expect(redirectFor("vscode-insiders")).toBe("vscode-insiders://aethons.jam/auth");
    expect(redirectFor("vscodium")).toBe("vscodium://aethons.jam/auth");
    expect(redirectFor("vscodium-insiders")).toBeInstanceOf(Error);
    expect(redirectFor("code-oss")).toBeInstanceOf(Error);
  });
});
