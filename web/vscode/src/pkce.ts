// PKCE (RFC 7636) helpers for the app-token sign-in.
import { createHash, randomBytes } from "node:crypto";

const b64url = (b: Buffer): string => b.toString("base64url");

export function challengeFor(verifier: string): string {
  return b64url(createHash("sha256").update(verifier).digest());
}

export function newPkce(): { state: string; verifier: string; challenge: string } {
  const verifier = b64url(randomBytes(32));
  return { state: b64url(randomBytes(32)), verifier, challenge: challengeFor(verifier) };
}

export function consentUrl(
  base: string,
  p: { redirect: string; state: string; challenge: string; client: string },
): string {
  const q = new URLSearchParams({
    redirect: p.redirect,
    state: p.state,
    code_challenge: p.challenge,
    code_challenge_method: "S256",
    client: p.client,
  });
  return `${base}/app/auth/app-token?${q}`;
}

// redirectFor is the callback URL for this editor, or an Error for a build
// (code-oss, ...) whose scheme Jam doesn't accept.
export function redirectFor(uriScheme: string): string | Error {
  if (uriScheme === "vscode" || uriScheme === "vscode-insiders") return `${uriScheme}://aethons.jam/auth`;
  return new Error(`Jam sign-in supports VS Code and VS Code Insiders, not "${uriScheme}"`);
}
