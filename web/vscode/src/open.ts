// Opening a session's studio in a Remote-SSH window. Pure: the VS Code
// surface is the Ui interface, built in extension.ts.
import type { JamApi } from "./api";
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
  remoteAuthority(): string | undefined;
}

export const remoteUri = (studio: string): string => `vscode-remote://ssh-remote+jam-${studio}/home/agent/workspace`;

export function webUrl(base: string, channel: string): string {
  return channel ? `${base}/app/user/c/${encodeURIComponent(channel)}` : `${base}/app/user/`;
}

export type OpenDeps = {
  ui: Ui;
  signedIn: () => Promise<{ api: JamApi; url: string }>;
  ssh: (api: JamApi, studio: string) => Promise<SshResult>;
};

const WEB = "Open in the web app";
const ASKED = "jam.askedLocalServerDownload";

export function studioFromAuthority(a: string | undefined): string | undefined {
  const p = "ssh-remote+jam-";
  if (!a?.startsWith(p)) return undefined;
  const id = a.slice(p.length);
  return SESSION_ID_RE.test(id) ? id : undefined;
}

// openSession validates the id first, so a crafted link reaches no API call
// and no file. A SignedOutError (401) propagates to the caller's sign-out.
export async function openSession(d: OpenDeps, id: string): Promise<void> {
  const { ui } = d;
  if (!SESSION_ID_RE.test(id)) {
    await ui.error("That isn't a Jam session link.");
    return;
  }
  const { api, url } = await d.signedIn();
  let head;
  try {
    head = await api.sessionHead(id);
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) {
      if ((await ui.error("No such session, or you can't see it.", WEB)) === WEB) await ui.openExternal(`${url}/app/user/`);
      return;
    }
    throw e;
  }
  if (head.ssh === "") {
    const msg = `You can't SSH into ${head.label}'s studio (ask an operator to share it).`;
    if ((await ui.error(msg, WEB)) === WEB) await ui.openExternal(webUrl(url, head.channel));
    return;
  }
  if (!ui.hasRemoteSsh()) {
    if ((await ui.error("Opening a studio needs Microsoft's Remote - SSH extension.", "Install")) === "Install") await ui.installRemoteSsh();
    return;
  }
  try {
    await d.ssh(api, id);
  } catch (e) {
    if (e instanceof SignedOutError) throw e;
    await ui.error(e instanceof ApiError ? certErrorMessage(e, id) : e instanceof Error ? e.message : String(e));
    return;
  }
  if (!ui.remembered(ASKED) && ui.getGlobal("remote.SSH", "localServerDownload") !== "always") {
    const a = await ui.info(
      "Jam studios can't download VS Code Server themselves. Set Remote-SSH's localServerDownload to 'always'?",
      "Set it",
      "Not now",
    );
    if (a === "Set it") await ui.setGlobal("remote.SSH", "localServerDownload", "always");
    await ui.remember(ASKED);
  }
  await ui.openFolder(remoteUri(id));
}

export async function pickSession(d: OpenDeps): Promise<void> {
  const { api } = await d.signedIn();
  const rail = await api.rail();
  const seen = new Set<string>();
  const items: { label: string; description: string; detail: string; id: string }[] = [];
  for (const g of rail.groups) {
    if (g.bucket !== "needs-you" && g.bucket !== "active") continue;
    for (const r of g.rows) {
      if (r.kind !== "studio" || seen.has(r.id)) continue;
      seen.add(r.id);
      items.push({ label: (g.bucket === "needs-you" ? "$(bell) " : "") + r.label, description: r.project, detail: r.mission, id: r.id });
    }
  }
  const p = await d.ui.pick(items, "Open which session's studio?");
  if (!p) return;
  const session = (await api.conversation(p.id)).session;
  if (!session) {
    await d.ui.error("That conversation has no session you can open");
    return;
  }
  await openSession(d, session);
}
