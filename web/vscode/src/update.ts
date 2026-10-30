// The extension keeps itself current with the Jam it signs in to: Jam serves
// the .vsix it was released with (GET /me/vscode, /me/vscode/jam.vsix), and a
// signed-in window offers it when it is newer than this one. Pure: the VS Code
// surface is UpdateDeps, built in extension.ts.

export const UPDATE_CHECK_MS = 6 * 60 * 60_000;
// "Later" holds the offer of that version back for a day, in every window.
export const UPDATE_LATER_MS = 24 * 60 * 60_000;
export const UPDATE_LATER_KEY = "jam.update.later";

const UPDATE = "Update";
const LATER = "Later";
const RELOAD = "Reload Window";

// parseVersion reads "x.y.z" (digits only); anything else is undefined.
export function parseVersion(v: unknown): [number, number, number] | undefined {
  if (typeof v !== "string") return undefined;
  const m = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/.exec(v);
  return m ? [Number(m[1]), Number(m[2]), Number(m[3])] : undefined;
}

// offers reports whether Jam's extension version should be offered over
// mine: only a newer one (a rolled-back Jam never downgrades anyone), and
// never to or from a dev build (0.0.0).
export function offers(mine: string, jams: string): boolean {
  const a = parseVersion(mine);
  const b = parseVersion(jams);
  if (!a || !b || a.every((n) => n === 0) || b.every((n) => n === 0)) return false;
  for (let i = 0; i < 3; i++) if (a[i] !== b[i]) return b[i]! > a[i]!;
  return false;
}

export type UpdateDeps = {
  version: string; // this extension's
  // The current sign-in for jam.url (undefined: signed out, no check).
  session: () => Promise<{ url: string; token: string } | undefined>;
  fetch: typeof globalThis.fetch;
  // claim reports whether this window makes the offer (one window per id).
  claim: (id: string) => Promise<boolean>;
  state: { get<T>(k: string): T | undefined; update(k: string, v: unknown): Thenable<void> };
  ask: (msg: string, ...actions: string[]) => Thenable<string | undefined>;
  // install installs the .vsix bytes (as version) and resolves when done.
  install: (vsix: Uint8Array, version: string) => Promise<void>;
  reload: () => Thenable<unknown>;
  log: (m: string) => void;
  now: () => number;
  setInterval: (fn: () => void, ms: number) => unknown;
  clearInterval: (h: unknown) => void;
};

export class Updater {
  private timer: unknown;
  private running = false;

  constructor(private d: UpdateDeps) {}

  // start checks now and every UPDATE_CHECK_MS.
  start(): void {
    void this.check();
    this.timer = this.d.setInterval(() => void this.check(), UPDATE_CHECK_MS);
  }

  dispose(): void {
    if (this.timer !== undefined) this.d.clearInterval(this.timer);
    this.timer = undefined;
  }

  // check asks Jam for its extension and offers it when it is newer. It never
  // throws, and never signs out: a failed check just waits for the next one.
  async check(): Promise<void> {
    if (this.running) return;
    this.running = true;
    try {
      await this.checkOnce();
    } catch (e) {
      this.d.log(`update: the check failed: ${e instanceof Error ? e.message : String(e)}`);
    } finally {
      this.running = false;
    }
  }

  private async checkOnce(): Promise<void> {
    if (!parseVersion(this.d.version) || this.d.version === "0.0.0") return; // a dev build
    const s = await this.d.session();
    if (!s) return;
    const res = await this.get(s, "/me/vscode");
    if (res.status === 404) return; // a Jam that predates serving the extension
    if (!res.ok) throw new Error(`GET /me/vscode: HTTP ${res.status}`);
    const info = (await res.json()) as { available?: unknown; version?: unknown };
    const version = typeof info.version === "string" ? info.version : "";
    if (info.available !== true || !offers(this.d.version, version)) return;
    const later = this.d.state.get<{ version?: unknown; until?: unknown }>(UPDATE_LATER_KEY);
    if (later && later.version === version && typeof later.until === "number" && this.d.now() < later.until) return;
    // One window offers each version per check period.
    if (!(await this.d.claim(`update:${version}:${Math.floor(this.d.now() / UPDATE_CHECK_MS)}`))) return;
    this.d.log(`update: Jam serves the extension ${version} (this is ${this.d.version})`);
    const pick = await this.d.ask(`A Jam extension update is available: ${version} (you have ${this.d.version}).`, UPDATE, LATER);
    if (pick !== UPDATE) {
      if (pick === LATER) await this.d.state.update(UPDATE_LATER_KEY, { version, until: this.d.now() + UPDATE_LATER_MS });
      return;
    }
    const vsix = await this.get(s, "/me/vscode/jam.vsix");
    if (!vsix.ok) throw new Error(`GET /me/vscode/jam.vsix: HTTP ${vsix.status}`);
    await this.d.install(new Uint8Array(await vsix.arrayBuffer()), version);
    this.d.log(`update: installed the extension ${version}`);
    if ((await this.d.ask(`Installed the Jam extension ${version}. Reload to use it (other VS Code windows too).`, RELOAD)) === RELOAD) {
      await this.d.reload();
    }
  }

  private get(s: { url: string; token: string }, path: string): Promise<Response> {
    return this.d.fetch(s.url + path, { headers: { Authorization: `Bearer ${s.token}` }, redirect: "error" });
  }
}
