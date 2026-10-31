import { describe, expect, it, vi } from "vitest";
import { Auth, SESSION_KEY } from "../src/auth";
import type { Session } from "../src/auth";
import { Auths } from "../src/auths";

const A = "https://a.example";
const B = "https://b.example";

function setup(def: string | undefined = A) {
  const made: { key: string; tokenFile: string; auth: Auth }[] = [];
  const auths = new Auths({
    defaultUrl: () => def,
    tokenFile: (app) => `/cfg/${app}-app-token.json`,
    make: (key, tokenFile) => {
      const auth = new Auth({
        secrets: { get: async () => undefined, store: async () => {}, delete: async () => {} },
        openExternal: async () => true,
        fetch: vi.fn() as unknown as typeof fetch,
        uriScheme: "vscode",
        hostname: "h",
        tokenFile,
        key,
      });
      made.push({ key, tokenFile, auth });
      return auth;
    },
  });
  return { auths, made };
}

describe("Auths", () => {
  it("the default Jam keeps jam.session and default-app-token.json", () => {
    const { auths, made } = setup();
    auths.authFor(A);
    expect(made.map((m) => [m.key, m.tokenFile])).toEqual([[SESSION_KEY, "/cfg/default-app-token.json"]]);
  });

  it("another Jam gets jam.session:<url> and vscode-<slug>-app-token.json", () => {
    const { auths, made } = setup();
    auths.authFor(B);
    expect(made.map((m) => [m.key, m.tokenFile])).toEqual([[`${SESSION_KEY}:${B}`, "/cfg/vscode-b.example-app-token.json"]]);
  });

  it("authFor caches per Jam", () => {
    const { auths, made } = setup();
    expect(auths.authFor(B)).toBe(auths.authFor(B));
    expect(auths.authFor(A)).not.toBe(auths.authFor(B));
    expect(made).toHaveLength(2);
  });

  it("onChange reports the Jam of a sign-out, for Auths made before and after subscribing", async () => {
    const { auths } = setup();
    const a = auths.authFor(A);
    const seen: [string, Session | undefined][] = [];
    auths.onChange((url, s) => void seen.push([url, s]));
    const b = auths.authFor(B);
    await a.signOut();
    await b.signOut();
    expect(seen).toEqual([[A, undefined], [B, undefined]]);
  });
});
