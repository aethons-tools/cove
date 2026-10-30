// Opening a session's window (local), and connecting it to the session's
// studio over Remote-SSH and back. Pure: the VS Code
// surface is the Ui interface, built in extension.ts.
import type { JamApi } from "./api";
import type { RailResponse } from "../../app/src/api/types.js" with { "resolution-mode": "import" };
import { ApiError, SignedOutError } from "./api";
import type { Editor } from "./editor";
import { certErrorMessage } from "./ssh/cert";
import type { SshResult } from "./ssh/setup";
import { SESSION_ID_RE } from "./uri";

export interface Ui {
  error(msg: string, ...actions: string[]): Promise<string | undefined>;
  info(msg: string, ...actions: string[]): Promise<string | undefined>;
  pick<T extends { label: string }>(items: T[], placeholder: string): Promise<T | undefined>;
  openExternal(url: string): Promise<void>;
  // Opens uri in a new window (focusing one already on it), or with reuse in this one.
  openFolder(uri: string, o?: { reuse: true }): Promise<void>;
  // The session's local workspace file (written if missing), as a URI.
  localWorkspace(id: string): Promise<string>;
  // Whether a studio window for the session has refreshed its mark lately.
  isConnected(id: string): boolean;
  // Sets (refreshes) or clears the session's connected mark.
  setConnected(id: string, on: boolean): Promise<void>;
  hasRemoteSsh(): boolean;
  installRemoteSsh(): Promise<void>;
  getGlobal(section: string, key: string): unknown;
  setGlobal(section: string, key: string, v: unknown): Promise<void>;
  remembered(key: string): boolean;
  remember(key: string): Promise<void>;
  windowAuthority(): string | undefined;
  // Runs work under a non-cancellable progress notification; step(msg) updates it.
  progress<T>(title: string, work: (step: (msg: string) => void) => Promise<T>): Promise<T>;
}

export const remoteUri = (studio: string): string => `vscode-remote://ssh-remote+jam-${studio}/home/agent/workspace`;

export function webUrl(base: string, channel: string): string {
  return channel ? `${base}/app/user/c/${encodeURIComponent(channel)}` : `${base}/app/user/`;
}

export type OpenDeps = {
  ui: Ui;
  editor: Editor;
  signedIn: () => Promise<{ api: JamApi; url: string }>;
  ssh: (api: JamApi, studio: string) => Promise<SshResult>;
  // The "Jam" output channel. Never pass it a token.
  log: (m: string) => void;
};

const WEB = "Open in the web app";
const ASKED = "jam.askedLocalServerDownload";

// "ssh-remote+jam-<id>", or Remote-SSH's hex-encoded JSON form of the same host
// ("ssh-remote+<hex of {"hostName":"jam-<id>",…}>"). Anything else has no studio.
export function studioFromAuthority(a: string | undefined): string | undefined {
  const p = "ssh-remote+";
  if (!a?.startsWith(p)) return undefined;
  let host = a.slice(p.length);
  if (/^(?:[0-9a-f]{2})+$/i.test(host) && !host.startsWith("jam-")) {
    try {
      const o: unknown = JSON.parse(Buffer.from(host, "hex").toString("utf8"));
      const h = o && typeof o === "object" ? (o as { hostName?: unknown }).hostName : undefined;
      if (typeof h !== "string") return undefined;
      host = h;
    } catch {
      return undefined;
    }
  }
  if (!host.startsWith("jam-")) return undefined;
  const id = host.slice(4);
  return SESSION_ID_RE.test(id) ? id : undefined;
}

type UriLike = { scheme: string; authority: string };
// The window's remote authority. env.remoteAuthority is proposed API, so read
// it off the first vscode-remote folder (or the workspace file).
export function windowAuthority(w: { workspaceFolders?: readonly { uri: UriLike }[]; workspaceFile?: UriLike }): string | undefined {
  for (const u of [...(w.workspaceFolders ?? []).map((f) => f.uri), ...(w.workspaceFile ? [w.workspaceFile] : [])]) {
    if (u.scheme === "vscode-remote") return u.authority;
  }
  return undefined;
}

type Step = (m: string) => void;
type Fail = (msg: string, ...actions: string[]) => Promise<string | undefined>;

// withSession validates the id (so a crafted link reaches no API call and no
// file), then runs work under progress, logging every step and outcome
// prefixed with the session id. A SignedOutError (401) propagates to the
// caller's sign-out.
async function withSession(
  d: OpenDeps,
  id: string,
  title: string,
  work: (c: { step: Step; log: (m: string) => void; fail: Fail }) => Promise<void>,
): Promise<void> {
  const log = (m: string) => d.log(`[${id}] ${m}`);
  if (!SESSION_ID_RE.test(id)) {
    d.log(`open: refused a bad session id (${JSON.stringify(id.slice(0, 80))})`);
    await d.ui.error("That isn't a Jam session link.");
    return;
  }
  const fail: Fail = async (msg, ...actions) => {
    log(`stopped: ${msg}`);
    return d.ui.error(msg, ...actions);
  };
  try {
    await d.ui.progress(title, (report) =>
      work({
        log,
        fail,
        step: (m) => {
          log(m);
          report(m);
        },
      }),
    );
  } catch (e) {
    log(`error: ${e instanceof Error ? e.message : String(e)}`);
    throw e;
  }
}

// checkSession signs in and reads the session's head; a 404 is shown (with
// the web app) and gives undefined.
async function checkSession(d: OpenDeps, id: string, step: Step, log: (m: string) => void, fail: Fail) {
  step("checking the session");
  const { api, url } = await d.signedIn();
  try {
    const head = await api.sessionHead(id);
    log(`session found: ${head.label}`);
    return { api, url, head };
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) {
      if ((await fail("No such session, or you can't see it.", WEB)) === WEB) await d.ui.openExternal(`${url}/app/user/`);
      return undefined;
    }
    throw e;
  }
}

type Head = Awaited<ReturnType<JamApi["sessionHead"]>>;

// prepareSsh is what a studio window needs first: SSH access to the session,
// Remote-SSH, and the SSH setup (key, cert, ssh_config block) via d.ssh. It
// reports a failure through fail and gives false; a SignedOutError propagates.
// The setup runs before every remote open: OpenSSH loads the cert before the
// ProxyCommand runs, so ssh-proxy's renewal only helps the next connection.
async function prepareSsh(
  d: OpenDeps,
  s: { api: JamApi; url: string; head: Head },
  id: string,
  step: Step,
  log: (m: string) => void,
  fail: Fail,
): Promise<boolean> {
  const { ui } = d;
  const { api, url, head } = s;
  if (head.ssh === "") {
    const msg = `You can't SSH into ${head.label}'s studio (ask an operator to share it).`;
    if ((await fail(msg, WEB)) === WEB) await ui.openExternal(webUrl(url, head.channel));
    return false;
  }
  if (!ui.hasRemoteSsh()) {
    if ((await fail(`Connecting to a studio needs ${d.editor.remoteSsh.name} extension.`, "Install")) === "Install") await ui.installRemoteSsh();
    return false;
  }
  step(`setting up SSH for ${head.label}`);
  try {
    const r = await d.ssh(api, id);
    log(`ssh ready: alias ${r.alias}`);
    return true;
  } catch (e) {
    if (e instanceof SignedOutError) throw e;
    await fail(e instanceof ApiError ? certErrorMessage(e, id) : e instanceof Error ? e.message : String(e));
    return false;
  }
}

// openSession opens the session's local window (its workspace file, in a new
// window or the one already on it): no SSH, so the studio can idle. A session
// flagged connected (its studio window is open) renews its SSH setup and opens
// (focuses) that window instead; if that fails, the flag is cleared and the
// local window opens.
export async function openSession(d: OpenDeps, id: string): Promise<void> {
  await withSession(d, id, `Jam: opening ${id}…`, async ({ step, log, fail }) => {
    const s = await checkSession(d, id, step, log, fail);
    if (!s) return;
    let remote = false;
    if (d.ui.isConnected(id)) {
      remote = await prepareSsh(d, s, id, step, log, async (m) => {
        log(`can't reconnect: ${m}; opening the local window`);
        return undefined;
      });
      if (!remote) await d.ui.setConnected(id, false);
    }
    step("opening the window");
    let uri: string;
    if (remote) {
      log("connected: opening its studio window");
      uri = remoteUri(id);
    } else uri = await d.ui.localWorkspace(id);
    log(`openFolder ${uri}`);
    await d.ui.openFolder(uri);
    log("openFolder returned");
  });
}

// connectStudio sets up SSH for the session's studio and reloads this window
// onto it over Remote-SSH. The studio window flags itself connected when it starts.
export async function connectStudio(d: OpenDeps, id: string): Promise<void> {
  const { ui } = d;
  await withSession(d, id, `Jam: connecting ${id}…`, async ({ step, log, fail }) => {
    const s = await checkSession(d, id, step, log, fail);
    if (!s || !(await prepareSsh(d, s, id, step, log, fail))) return;
    // Open Remote - SSH (VSCodium) always downloads the server in the studio.
    if (d.editor.localServerDownload && !ui.remembered(ASKED) && ui.getGlobal("remote.SSH", "localServerDownload") !== "always") {
      log("asking about Remote-SSH localServerDownload");
      const a = await ui.info(
        "Jam studios can't download VS Code Server themselves. Set Remote-SSH's localServerDownload to 'always'?",
        "Set it",
        "Not now",
      );
      if (a === "Set it") await ui.setGlobal("remote.SSH", "localServerDownload", "always");
      await ui.remember(ASKED);
      log(`localServerDownload answer: ${a ?? "dismissed"}`);
    }
    step("connecting the window");
    const uri = remoteUri(id);
    log(`openFolder ${uri} (this window)`);
    await ui.openFolder(uri, { reuse: true });
    log("openFolder returned");
  });
}

// disconnectStudio clears the session's connected flag and reloads this window
// onto the session's local workspace; Remote-SSH closes the connection, so the
// studio can idle. The flag is cleared here, before the reload: a write from
// the studio window's deactivate can be lost as the window reloads.
export async function disconnectStudio(d: OpenDeps, id: string): Promise<void> {
  if (!SESSION_ID_RE.test(id)) {
    d.log(`disconnect: refused a bad session id (${JSON.stringify(id.slice(0, 80))})`);
    return;
  }
  const uri = await d.ui.localWorkspace(id);
  await d.ui.setConnected(id, false);
  d.log(`[${id}] disconnecting: openFolder ${uri} (this window)`);
  await d.ui.openFolder(uri, { reuse: true });
}

// A studio window refreshes its session's connected mark every
// CONNECTED_BEAT_MS; a mark older than CONNECTED_STALE_MS is a window that
// went away without clearing it (closed, crashed, VS Code quit).
export const CONNECTED_BEAT_MS = 30_000;
export const CONNECTED_STALE_MS = 90_000;

// connectedSet keeps, per session, the mark "a studio (remote) window for this
// session is open": the time a studio window last refreshed it (ms since the
// epoch). A studio window sets it on activation, refreshes it every
// CONNECTED_BEAT_MS and clears it on deactivate; a stale mark, or anything
// not a time, is not connected. One key per session (jam.connected.<id>, in
// a Memento such as globalState), so windows never overwrite each other's.
export function connectedSet(st: { get<T>(k: string): T | undefined; update(k: string, v: unknown): Thenable<void> }, now: () => number = Date.now) {
  const key = (id: string) => `jam.connected.${id}`;
  return {
    isConnected: (id: string) => {
      if (!SESSION_ID_RE.test(id)) return false;
      const at = st.get<unknown>(key(id));
      return typeof at === "number" && Math.abs(now() - at) < CONNECTED_STALE_MS;
    },
    setConnected: async (id: string, on: boolean) => {
      if (SESSION_ID_RE.test(id)) await st.update(key(id), on ? now() : undefined);
    },
  };
}

export type WindowKind = { kind: "local" | "remote"; id: string };

// windowKind is the session a window belongs to: a jam-<id> remote, or a
// local session window, whose workspace file is <sessionsDir>/<id>.code-workspace
// (sessionsDir as a URI: its scheme is the platform's globalStorageUri's, which
// may be vscode-userdata rather than file). The directory compares case-insensitively on
// darwin and win32 (case-insensitive file systems, drive letters); the id never.
export function windowKind(
  w: { workspaceFolders?: readonly { uri: UriLike }[]; workspaceFile?: UriLike & { path: string } },
  sessionsDir: { scheme: string; path: string },
  platform: string,
): WindowKind | undefined {
  const remote = studioFromAuthority(windowAuthority(w));
  if (remote) return { kind: "remote", id: remote };
  const f = w.workspaceFile;
  if (!f || f.scheme !== sessionsDir.scheme) return undefined;
  const fold = platform === "darwin" || platform === "win32";
  const norm = (p: string) => {
    const q = p.replace(/\\/g, "/").replace(/\/+$/, "");
    return fold ? q.toLowerCase() : q;
  };
  const slash = f.path.lastIndexOf("/");
  if (slash < 0 || norm(f.path.slice(0, slash)) !== norm(sessionsDir.path)) return undefined;
  const m = /^(.+)\.code-workspace$/.exec(f.path.slice(slash + 1));
  return m && SESSION_ID_RE.test(m[1]!) ? { kind: "local", id: m[1]! } : undefined;
}

export const SESSION_WORKSPACE = JSON.stringify({ folders: [] }, null, 2) + "\n";

// statusBarItem is the window's Connect/Disconnect item, if any.
export function statusBarItem(k: WindowKind | undefined): { text: string; command: string; tooltip: string } | undefined {
  if (k?.kind === "local") return { text: "$(plug) Connect studio", command: "jam.connectStudio", tooltip: "Jam: connect this window to the session's studio over Remote-SSH" };
  if (k?.kind === "remote") return { text: "$(debug-disconnect) Disconnect studio", command: "jam.disconnectStudio", tooltip: "Jam: disconnect from the studio so it can idle" };
  return undefined;
}

export type RailRow = RailResponse["groups"][number]["rows"][number];
export type SessionPick = { label: string; description?: string; detail?: string; row: RailRow };

// Live studio rows (needs-you first), one per channel.
export async function sessionPicks(api: JamApi): Promise<SessionPick[]> {
  const rail = await api.rail();
  const seen = new Set<string>();
  const items: SessionPick[] = [];
  for (const g of rail.groups) {
    if (g.bucket !== "needs-you" && g.bucket !== "active") continue;
    for (const r of g.rows) {
      if (r.kind !== "studio" || seen.has(r.id)) continue;
      seen.add(r.id);
      items.push({ label: (g.bucket === "needs-you" ? "$(bell) " : "") + r.label, description: r.project, detail: r.mission, row: r });
    }
  }
  return items;
}

export async function sessionOfPick(api: JamApi, p: SessionPick): Promise<string | undefined> {
  return (await api.conversation(p.row.id)).session || undefined;
}

export async function pickSession(d: OpenDeps): Promise<void> {
  const { api } = await d.signedIn();
  const p = await d.ui.pick(await sessionPicks(api), "Open which session's studio?");
  if (!p) return;
  const session = await sessionOfPick(api, p);
  if (!session) {
    await d.ui.error("That conversation has no session you can open");
    return;
  }
  await openSession(d, session);
}
