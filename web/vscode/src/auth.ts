// Sign-in with an app token (PKCE): open the consent page in the browser,
// wait for the vscode:// callback, exchange the code, and keep the session
// in SecretStorage and in the CLI's token file.
import { editorFor } from "./editor";
import { consentUrl, newPkce, redirectFor } from "./pkce";
import { appTokenFileHolds, removeAppTokenFile, writeAppTokenFile } from "./tokenfile";

export const SIGN_IN_TIMEOUT_MS = 5 * 60_000;
export const SESSION_KEY = "jam.session";

export type Session = { url: string; token: string; user: string; expiresAt: string };

export interface SecretsLike {
  get(k: string): Thenable<string | undefined>;
  store(k: string, v: string): Thenable<void>;
  delete(k: string): Thenable<void>;
}

type Pending = {
  state: string;
  settle: (r: { code: string } | Error) => void;
};

export class Auth {
  private pending: Pending | undefined;
  private signOuts = 0; // counts sign-outs, so a racing syncTokenFile can tell
  private listeners = new Set<(s: Session | undefined) => void>();

  constructor(
    private d: {
      secrets: SecretsLike;
      openExternal: (url: string) => Thenable<boolean>;
      fetch: typeof fetch;
      uriScheme: string;
      hostname: string;
      tokenFile: string;
      setTimeout?: typeof setTimeout;
    },
  ) {}

  readonly onChange = (cb: (s: Session | undefined) => void): (() => void) => {
    this.listeners.add(cb);
    return () => void this.listeners.delete(cb);
  };

  private emit(s: Session | undefined): void {
    for (const cb of [...this.listeners]) cb(s);
  }

  async session(url: string): Promise<Session | undefined> {
    const raw = await this.d.secrets.get(SESSION_KEY);
    if (!raw) return undefined;
    try {
      const s = JSON.parse(raw) as Session;
      return s.url === url && typeof s.token === "string" && s.token !== "" ? s : undefined;
    } catch {
      return undefined;
    }
  }

  async signIn(url: string): Promise<Session> {
    const redirect = redirectFor(this.d.uriScheme);
    if (redirect instanceof Error) throw redirect;
    this.pending?.settle(new Error("Sign-in was replaced by a newer one"));
    const p = newPkce();
    const set = this.d.setTimeout ?? setTimeout;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const got = new Promise<{ code: string } | Error>((resolve) => {
      const me: Pending = {
        state: p.state,
        settle: (r) => {
          if (this.pending === me) this.pending = undefined;
          resolve(r);
        },
      };
      this.pending = me;
      timer = set(() => me.settle(new Error("Sign-in timed out")), SIGN_IN_TIMEOUT_MS);
    });
    try {
      await this.d.openExternal(
        consentUrl(url, { redirect, state: p.state, challenge: p.challenge, client: `${editorFor(this.d.uriScheme).name} on ${this.d.hostname}` }),
      );
      const r = await got;
      if (r instanceof Error) throw r;
      const s = await this.exchange(url, r.code, p.verifier, redirect);
      // The file first: a failed write leaves no secret behind, so the
      // extension never believes it is signed in while the CLI isn't.
      await writeAppTokenFile(this.d.tokenFile, { token: s.token, jamUrl: url });
      try {
        await this.d.secrets.store(SESSION_KEY, JSON.stringify(s));
      } catch (e) {
        await removeAppTokenFile(this.d.tokenFile).catch(() => {});
        throw e;
      }
      this.emit(s);
      return s;
    } finally {
      clearTimeout(timer);
    }
  }

  private async exchange(url: string, code: string, verifier: string, redirect: string): Promise<Session> {
    const res = await this.d.fetch(`${url}/app/auth/app-token/exchange`, {
      method: "POST",
      headers: { "Content-Type": "application/json", Accept: "application/json" },
      redirect: "error",
      body: JSON.stringify({ code, code_verifier: verifier, redirect }),
    });
    if (res.status === 400) throw new Error("Jam refused the sign-in code; try again");
    if (!res.ok) throw new Error(`Jam sign-in failed (HTTP ${res.status})`);
    let j: { token?: unknown; expires_at?: unknown; user?: unknown };
    try {
      j = (await res.json()) as typeof j;
    } catch {
      j = {};
    }
    if (typeof j?.token !== "string" || j.token === "" || typeof j.user !== "string" || j.user === "") {
      throw new Error("Jam sign-in failed (unexpected response)");
    }
    return { url, token: j.token, user: j.user, expiresAt: typeof j.expires_at === "string" ? j.expires_at : "" };
  }

  // handleCallback is true when q belongs to the pending sign-in.
  handleCallback(q: URLSearchParams): boolean {
    const p = this.pending;
    if (!p || q.get("state") !== p.state) return false;
    const code = q.get("code");
    const err = q.get("error");
    if (err || !code) {
      p.settle(new Error(err && err !== "access_denied" ? `Sign-in failed: ${err}` : "Sign-in was cancelled"));
    } else {
      p.settle({ code });
    }
    return true;
  }

  // cancel abandons the pending sign-in (the user dismissed the progress).
  cancel(): void {
    this.pending?.settle(new Error("Sign-in was cancelled"));
  }

  // syncTokenFile makes the CLI's token file hold s, rewriting it only when it
  // differs: it heals a file deleted by hand, by the other VS Code flavour's
  // sign-out, or left unwritten by a failed sign-in.
  // A sign-out that starts meanwhile wins: the file it removed stays removed.
  async syncTokenFile(s: Session): Promise<void> {
    const gen = this.signOuts;
    const f = { token: s.token, jamUrl: s.url };
    if (await appTokenFileHolds(this.d.tokenFile, f)) return;
    await writeAppTokenFile(this.d.tokenFile, f);
    // Only our write is undone, not a newer sign-in's.
    if (this.signOuts !== gen && (await appTokenFileHolds(this.d.tokenFile, f))) await removeAppTokenFile(this.d.tokenFile);
  }

  // signOut deletes the secret and the token file independently, so one
  // failing still clears the other; it then rethrows the first error.
  async signOut(): Promise<void> {
    this.signOuts++;
    const r = await Promise.allSettled([this.d.secrets.delete(SESSION_KEY), removeAppTokenFile(this.d.tokenFile)]);
    this.emit(undefined);
    const failed = r.find((x): x is PromiseRejectedResult => x.status === "rejected");
    if (failed) throw failed.reason;
  }
}
