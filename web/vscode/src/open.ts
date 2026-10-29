// Opening a session's studio in a Remote-SSH window. Pure: the VS Code
// surface is the Ui interface, built in extension.ts.
import type { JamApi } from "./api";
import type { RailResponse } from "../../app/src/api/types.js" with { "resolution-mode": "import" };
import { ApiError, SignedOutError } from "./api";
import { certErrorMessage } from "./ssh/cert";
import type { SshResult } from "./ssh/setup";
import { SESSION_ID_RE } from "./uri";

export interface Ui {
  error(msg: string, ...actions: string[]): Promise<string | undefined>;
  info(msg: string, ...actions: string[]): Promise<string | undefined>;
  pick<T extends { label: string }>(items: T[], placeholder: string): Promise<T | undefined>;
  openExternal(url: string): Promise<void>;
  openFolder(uri: string): Promise<void>;
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

// openSession validates the id first, so a crafted link reaches no API call
// and no file. A SignedOutError (401) propagates to the caller's sign-out.
// Every step and outcome goes to the log, prefixed with the session id.
export async function openSession(d: OpenDeps, id: string): Promise<void> {
  const { ui } = d;
  const log = (m: string) => d.log(`[${id}] ${m}`);
  if (!SESSION_ID_RE.test(id)) {
    d.log(`open: refused a bad session id (${JSON.stringify(id.slice(0, 80))})`);
    await ui.error("That isn't a Jam session link.");
    return;
  }
  const fail = async (msg: string, ...actions: string[]) => {
    log(`stopped: ${msg}`);
    return ui.error(msg, ...actions);
  };
  try {
    await ui.progress(`Jam: opening ${id}…`, async (report) => {
      const step = (m: string) => {
        log(m);
        report(m);
      };
      step("checking the session");
      const { api, url } = await d.signedIn();
      let head;
      try {
        head = await api.sessionHead(id);
      } catch (e) {
        if (e instanceof ApiError && e.status === 404) {
          if ((await fail("No such session, or you can't see it.", WEB)) === WEB) await ui.openExternal(`${url}/app/user/`);
          return;
        }
        throw e;
      }
      log(`session found: ${head.label}`);
      if (head.ssh === "") {
        const msg = `You can't SSH into ${head.label}'s studio (ask an operator to share it).`;
        if ((await fail(msg, WEB)) === WEB) await ui.openExternal(webUrl(url, head.channel));
        return;
      }
      if (!ui.hasRemoteSsh()) {
        if ((await fail("Opening a studio needs Microsoft's Remote - SSH extension.", "Install")) === "Install") await ui.installRemoteSsh();
        return;
      }
      step(`setting up SSH for ${head.label}`);
      try {
        const r = await d.ssh(api, id);
        log(`ssh ready: alias ${r.alias}`);
      } catch (e) {
        if (e instanceof SignedOutError) throw e;
        await fail(e instanceof ApiError ? certErrorMessage(e, id) : e instanceof Error ? e.message : String(e));
        return;
      }
      if (!ui.remembered(ASKED) && ui.getGlobal("remote.SSH", "localServerDownload") !== "always") {
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
      step("opening the window");
      const uri = remoteUri(id);
      log(`openFolder ${uri}`);
      await ui.openFolder(uri);
      log("openFolder returned");
    });
  } catch (e) {
    log(`error: ${e instanceof Error ? e.message : String(e)}`);
    throw e;
  }
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
