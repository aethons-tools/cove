// One sign-in per Jam: the default Jam (jam.url) keeps the original secret
// and the CLI's default token file; every other Jam gets its own secret and
// a vscode-<slug> token profile, which its ssh block names (--app).
import { Auth, SESSION_KEY } from "./auth";
import type { Session } from "./auth";
import { appFor } from "./jams";

export type AuthsDeps = {
  make: (key: string, tokenFile: string) => Auth;
  defaultUrl: () => string | undefined;
  tokenFile: (app: string) => string;
};

export class Auths {
  private byKey = new Map<string, { auth: Auth; url: string }>();
  private subs = new Set<(url: string, s: Session | undefined) => void>();

  constructor(private d: AuthsDeps) {}

  // primary is the default Jam's Auth, even while jam.url is unset or bad.
  primary(): Auth {
    return this.authFor(this.d.defaultUrl() ?? "", true);
  }

  // authFor is url's Auth, made on first use.
  authFor(url: string, isDefault = url === this.d.defaultUrl()): Auth {
    const def = isDefault;
    const key = def ? SESSION_KEY : `${SESSION_KEY}:${url}`;
    const tokenFile = this.d.tokenFile(def ? "default" : appFor(url, this.d.defaultUrl()));
    const id = `${key}\n${tokenFile}`;
    const have = this.byKey.get(id);
    if (have) {
      have.url = url;
      return have.auth;
    }
    const auth = this.d.make(key, tokenFile);
    const entry = { auth, url };
    this.byKey.set(id, entry);
    auth.onChange((s) => {
      for (const cb of [...this.subs]) cb(entry.url, s);
    });
    return auth;
  }

  // onChange reports every Jam's sign-in and sign-out, with its URL.
  onChange(cb: (url: string, s: Session | undefined) => void): () => void {
    this.subs.add(cb);
    return () => void this.subs.delete(cb);
  }
}
