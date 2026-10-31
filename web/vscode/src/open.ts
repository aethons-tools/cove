// Opening a session's studio window over Remote-SSH. Pure: the VS Code
// surface is the Ui interface, built in extension.ts.
import type { JamApi } from "./api";
import type { RailResponse } from "../../app/src/api/types.js" with { "resolution-mode": "import" };
import { ApiError, SignedOutError } from "./api";
import type { Editor } from "./editor";
import { certErrorMessage } from "./ssh/cert";
import type { SshResult } from "./ssh/setup";
import { LEGACY_RE } from "./bridge/intents";
import { hubJamOf } from "./jams";
import { CHANNEL_RE, SESSION_ID_RE } from "./uri";

export interface Ui {
  error(msg: string, ...actions: string[]): Promise<string | undefined>;
  info(msg: string, ...actions: string[]): Promise<string | undefined>;
  pick<T extends { label: string }>(items: T[], placeholder: string): Promise<T | undefined>;
  openExternal(url: string): Promise<void>;
  // Opens uri in a new window, focusing one already on it.
  openFolder(uri: string): Promise<void>;
  // writeStudioFile writes a studio workspace file (studioWorkspace) into the
  // extension's studios directory and returns its URI; studioFile is that URI
  // when the file exists.
  writeStudioFile(name: string, text: string): Promise<string>;
  studioFile(name: string): Promise<string | undefined>;
  // Opens a Jam's hub window (default: this window's Jam), writing its
  // workspace file if missing, or focuses it.
  openHub(jam?: string): Promise<void>;
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

// A studio window opens from a local workspace file on its remote folder, so
// its title bar can take the session's color (docs/usage/jam/session-colors.md)
// without writing settings into the studio's own workspace.
export const studioFileName = (studio: string): string => `jam-${studio}.code-workspace`;

const COLOR_RE = /^#[0-9a-f]{6}$/;

// studioWorkspace is the studio's workspace file: its remote folder, and with
// a color, the title bar in it (dimmed when the window is inactive).
export function studioWorkspace(studio: string, color: string): string {
  const w: Record<string, unknown> = { folders: [{ uri: remoteUri(studio) }], remoteAuthority: `ssh-remote+jam-${studio}` };
  if (COLOR_RE.test(color)) {
    w.settings = {
      "workbench.colorCustomizations": {
        "titleBar.activeBackground": color,
        "titleBar.activeForeground": "#ffffff",
        "titleBar.inactiveBackground": `${color}99`,
        "titleBar.inactiveForeground": "#ffffffaa",
      },
    };
  }
  return JSON.stringify(w, null, 2) + "\n";
}

// focusStudioUri is what brings a studio's window to the front: its
// workspace file, or for a window opened before those, its remote folder.
export async function focusStudioUri(ui: Pick<Ui, "studioFile">, studio: string): Promise<string> {
  return (await ui.studioFile(studioFileName(studio))) ?? remoteUri(studio);
}

export function webUrl(base: string, channel: string): string {
  return channel ? `${base}/app/user/c/${encodeURIComponent(channel)}` : `${base}/app/user/`;
}

export type OpenDeps = {
  ui: Ui;
  editor: Editor;
  // remember records which Jam a session's studio window belongs to.
  remember?: (id: string) => Promise<void>;
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

// openSession sets up SSH for the session's studio and opens its studio
// window over Remote-SSH, from its workspace file (written fresh, so its color
// is current): a new window, or the one already on it. The studio
// window marks itself connected when it starts.
export async function openSession(d: OpenDeps, id: string): Promise<void> {
  const { ui } = d;
  await withSession(d, id, `Jam: opening ${id}…`, async ({ step, log, fail }) => {
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
    await d.remember?.(id);
    step("opening the window");
    const uri = await ui.writeStudioFile(studioFileName(id), studioWorkspace(id, s.head.color ?? ""));
    log(`openFolder ${uri}`);
    await ui.openFolder(uri);
    log("openFolder returned");
  });
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
export function connectedSet(
  st: { get<T>(k: string): T | undefined; update(k: string, v: unknown): Thenable<void>; keys(): readonly string[] },
  now: () => number = Date.now,
) {
  const PREFIX = "jam.connected.";
  const key = (id: string) => `${PREFIX}${id}`;
  const isConnected = (id: string) => {
    if (!SESSION_ID_RE.test(id)) return false;
    const at = st.get<unknown>(key(id));
    return typeof at === "number" && Math.abs(now() - at) < CONNECTED_STALE_MS;
  };
  return {
    isConnected,
    // list is the sessions with a fresh mark (the rail's ▣).
    list: () => st.keys().filter((k) => k.startsWith(PREFIX)).map((k) => k.slice(PREFIX.length)).filter(isConnected),
    setConnected: async (id: string, on: boolean) => {
      if (SESSION_ID_RE.test(id)) await st.update(key(id), on ? now() : undefined);
    },
  };
}

// A hub is a window on a Jam's hub file (jams.ts hubFileFor: a workspace with
// no folders) in the extension's global storage hub directory.
export const HUB_WORKSPACE = JSON.stringify({ folders: [] }, null, 2) + "\n";

export type WindowKind = { kind: "session"; id: string } | { kind: "hub"; file: string };

// windowKind is what a window is to Jam: a session's studio window (a jam-<id>
// remote), a hub (workspace file <hubDir>/Jam Hub (….code-workspace; hubDir as a URI, whose
// scheme is globalStorageUri's), or nothing. The directory compares
// case-insensitively on darwin and win32 (case-insensitive file systems, drive letters).
export function windowKind(
  w: { workspaceFolders?: readonly { uri: UriLike }[]; workspaceFile?: UriLike & { path: string } },
  hubDir: { scheme: string; path: string },
  platform: string,
): WindowKind | undefined {
  const remote = studioFromAuthority(windowAuthority(w));
  if (remote) return { kind: "session", id: remote };
  const f = w.workspaceFile;
  if (!f || f.scheme !== hubDir.scheme) return undefined;
  const fold = platform === "darwin" || platform === "win32";
  const norm = (p: string) => {
    const q = p.replace(/\\/g, "/").replace(/\/+$/, "");
    return fold ? q.toLowerCase() : q;
  };
  const slash = f.path.lastIndexOf("/");
  if (slash < 0 || norm(f.path.slice(0, slash)) !== norm(hubDir.path)) return undefined;
  const name = f.path.slice(slash + 1);
  return /^Jam Hub \(.+\)\.code-workspace$/.test(name) ? { kind: "hub", file: name } : undefined;
}

// windowJam is the Jam a window shows: a hub's own (none when that Jam is no
// longer listed), a studio window's recorded one, an ordinary window's chosen
// one; else (or when that isn't listed) the default, list[0].
export function windowJam(
  k: WindowKind | undefined,
  list: string[],
  o: { studioJam(id: string): string | undefined; chosen: string | undefined },
): string | undefined {
  if (k?.kind === "hub") return hubJamOf(k.file, list);
  const want = k?.kind === "session" ? o.studioJam(k.id) : o.chosen;
  return want !== undefined && list.includes(want) ? want : list[0];
}

// A pending hub request older than PENDING_MAX_MS is stale (its window gave up).
export const PENDING_MAX_MS = 60_000;

export type GoDeps = {
  // windowJam is this window's Jam; isHub says this window is its hub.
  windowJam: string | undefined;
  isConnected(id: string): boolean;
  openSession(id: string): Promise<void>;
  // focusStudio brings a studio window to the front with no sign-in (another Jam's).
  focusStudio(id: string): Promise<void>;
  isHub: boolean;
  // ownSession is a studio window's own session: going there reopens its tab.
  ownSession?: string;
  openTab(channel: string): void;
  writePending(jam: string, p: { channel: string; at: number }): Promise<void>;
  openHub(jam: string): Promise<void>;
  now(): number;
};

// goTo goes to jam's conversation: its session's studio window when one is
// open (this window's own: its conversation tab), else its tab in jam's hub.
// Outside that hub it leaves a pending request (the hub reads it on
// activation and focus) and opens or focuses the hub.
export async function goTo(d: GoDeps, jam: string, channel: string, session: string): Promise<void> {
  const here = jam === d.windowJam;
  if (here && session && session === d.ownSession) return d.openTab(channel);
  if (session && d.isConnected(session)) return here ? d.openSession(session) : d.focusStudio(session);
  if (here && d.isHub) return d.openTab(channel);
  await d.writePending(jam, { channel, at: d.now() });
  await d.openHub(jam);
}

// readPending is a pending request's channel, if raw is a fresh, valid one.
export function readPending(raw: string | undefined, now: number): string | undefined {
  if (!raw) return undefined;
  let o: unknown;
  try {
    o = JSON.parse(raw);
  } catch {
    return undefined;
  }
  if (!o || typeof o !== "object" || Array.isArray(o)) return undefined;
  const { channel, at } = o as { channel?: unknown; at?: unknown };
  if (typeof channel !== "string" || !(CHANNEL_RE.test(channel) || LEGACY_RE.test(channel))) return undefined;
  if (typeof at !== "number" || now - at > PENDING_MAX_MS || at - now > PENDING_MAX_MS) return undefined;
  return channel;
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
