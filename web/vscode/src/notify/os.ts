// OS notifications for events that arrive while VS Code is unfocused. macOS runs the
// vendored terminal-notifier.app (ad-hoc signed, not notarized; copied unmodified from
// node-notifier@12.0.0 by scripts/vscode.sh) and falls back to osascript; Linux runs
// notify-send. Child processes are started without a shell and every value is a
// separate argv entry. No token ever reaches argv.
import { join } from "node:path";

export const NOTIFY_WAIT_MS = 10 * 60_000;
const QUICK_MS = 10_000;
// terminal-notifier's first run waits on macOS's notification permission prompt.
const FIRST_RUN_MS = 120_000;
// At most this many notify-send --wait children at once across channels; the oldest is aborted.
export const MAX_WAITING = 8;

export type OsNote = { title: string; body: string; link: string; group: string; onClick: () => void };
export type Child = { stdout: string; stderr?: string };
export type OsDeps = {
  platform: NodeJS.Platform;
  extensionPath: string;
  execFile: (file: string, args: string[], o: { timeout: number; signal?: AbortSignal }) => Promise<Child>; // rejects on non-zero exit/ENOENT
  chmod: (p: string, mode: number) => Promise<void>;
  log: (m: string) => void; // the Jam output channel
};

// terminal-notifier reads its options through NSUserDefaults' argument domain, which
// parses a value starting with - [ ( { < or " as an option or a property list. A
// leading U+200B keeps every title and message a plain string.
const ZWSP = "\u200b";

export function terminalNotifierArgs(n: OsNote): string[] {
  return ["-title", ZWSP + n.title, "-message", ZWSP + n.body, "-open", n.link, "-group", n.group];
}

// The values are argv items of the script, never interpolated into it. There is no click target.
export function osascriptArgs(n: OsNote): string[] {
  return ["-e", "on run argv", "-e", "display notification (item 2 of argv) with title (item 1 of argv)", "-e", "end run", "--", n.title, n.body];
}

export function escapeMarkup(s: string): string {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

export function notifySendArgs(n: OsNote, actions: boolean, icon: string): string[] {
  const a = ["--app-name=Jam", `--icon=${icon}`];
  if (actions) a.push("--action=default=Open", "--wait");
  a.push("--", n.title, escapeMarkup(n.body));
  return a;
}

type ErrLike = { code?: unknown; signal?: unknown; killed?: unknown; name?: unknown };
const errOf = (e: unknown): ErrLike => (typeof e === "object" && e !== null ? (e as ErrLike) : {});
const msg = (e: unknown): string => (e instanceof Error ? e.message : String(e));

// A child that never ran, or was killed by something else (Gatekeeper's SIGKILL has
// killed:false). Our own timeout kill sets killed:true and is not one.
function isSpawnFailure(e: unknown): boolean {
  const x = errOf(e);
  if (x.code === "ENOENT" || x.code === "EACCES" || x.code === "EPERM") return true;
  return typeof x.signal === "string" && x.signal !== "" && x.killed !== true;
}

// terminal-notifier prints "Permission to send notifications was declined." when the
// user declined or muted it; falling back would bring the banners back.
function declined(...outs: unknown[]): boolean {
  return outs.some((o) => typeof o === "string" && o.includes("declined"));
}

// A notify-send --wait that was aborted or timed out: a close without a click.
function isQuietClose(e: unknown, sig: AbortSignal): boolean {
  const x = errOf(e);
  return sig.aborted || x.code === "ABORT_ERR" || x.name === "AbortError" || x.killed === true || (typeof x.signal === "string" && x.signal !== "");
}

type Linux = "off" | "actions" | "plain";

export class OsNotifier {
  private logged = new Set<string>();
  private chmodded = false;
  private osascriptOnly = false;
  private tnRan = false;
  private probe?: Promise<Linux>;
  private waiting = new Map<string, AbortController>();
  private disposed = false;

  constructor(private d: OsDeps) {}

  private logOnce(key: string, m: string): void {
    if (this.logged.has(key)) return;
    this.logged.add(key);
    try {
      this.d.log(m);
    } catch {
      /* logging must not fail a notification */
    }
  }

  // show never throws. On Linux with actions it resolves when the notification closes
  // (up to NOTIFY_WAIT_MS), so a caller must not await it on a path that blocks.
  async show(n: OsNote): Promise<void> {
    if (this.disposed) return;
    try {
      if (this.d.platform === "darwin") await this.darwin(n);
      else if (this.d.platform === "linux") await this.linux(n);
    } catch (e) {
      this.logOnce("unexpected", `OS notification failed: ${msg(e)}`);
    }
  }

  dispose(): void {
    this.disposed = true;
    for (const c of this.waiting.values()) c.abort();
    this.waiting.clear();
  }

  private async darwin(n: OsNote): Promise<void> {
    if (!this.osascriptOnly) {
      const bin = join(this.d.extensionPath, "dist", "vendor", "mac.noindex", "terminal-notifier.app", "Contents", "MacOS", "terminal-notifier");
      if (!this.chmodded) {
        this.chmodded = true;
        try {
          await this.d.chmod(bin, 0o755);
        } catch {
          /* vsce may drop the exec bit; the run below reports a real problem */
        }
      }
      const timeout = this.tnRan ? QUICK_MS : FIRST_RUN_MS;
      this.tnRan = true;
      try {
        const r = await this.d.execFile(bin, terminalNotifierArgs(n), { timeout });
        if (declined(r?.stdout, r?.stderr)) this.logOnce("declined", "terminal-notifier: notification permission was declined; no OS notifications");
        return;
      } catch (e) {
        const x = errOf(e) as { stdout?: unknown; stderr?: unknown };
        if (declined(x.stdout, x.stderr, msg(e))) {
          this.logOnce("declined", "terminal-notifier: notification permission was declined; no OS notifications");
          return;
        }
        if (isSpawnFailure(e)) {
          this.osascriptOnly = true;
          this.logOnce("tn", `terminal-notifier could not run (${msg(e)}); using osascript for this session`);
        } else {
          this.logOnce("tn", `terminal-notifier failed (${msg(e)}); using osascript for this notification`);
        }
      }
    }
    try {
      await this.d.execFile("osascript", osascriptArgs(n), { timeout: QUICK_MS });
    } catch (e) {
      this.logOnce("osascript", `osascript failed: ${msg(e)}`);
    }
  }

  private async capability(): Promise<Linux> {
    this.probe ??= (async (): Promise<Linux> => {
      try {
        const r = await this.d.execFile("notify-send", ["--help"], { timeout: QUICK_MS });
        return r.stdout.includes("--action") ? "actions" : "plain";
      } catch (e) {
        this.logOnce("probe", `notify-send is not available (${msg(e)}); no OS notifications`);
        return "off";
      }
    })();
    return this.probe;
  }

  private async linux(n: OsNote): Promise<void> {
    const cap = await this.capability();
    if (cap === "off" || this.disposed) return;
    const icon = join(this.d.extensionPath, "media", "jam.svg");
    const args = notifySendArgs(n, cap === "actions", icon);
    if (cap === "plain") {
      try {
        await this.d.execFile("notify-send", args, { timeout: QUICK_MS });
      } catch (e) {
        this.logOnce("send", `notify-send failed: ${msg(e)}`);
      }
      return;
    }
    // At most one waiting notify-send per channel.
    this.waiting.get(n.group)?.abort();
    this.waiting.delete(n.group);
    const ac = new AbortController();
    this.waiting.set(n.group, ac);
    while (this.waiting.size > MAX_WAITING) {
      const oldest = this.waiting.keys().next().value as string;
      this.waiting.get(oldest)?.abort();
      this.waiting.delete(oldest);
    }
    try {
      const r = await this.d.execFile("notify-send", args, { timeout: NOTIFY_WAIT_MS, signal: ac.signal });
      if (r.stdout.trim() === "default") n.onClick();
    } catch (e) {
      if (!isQuietClose(e, ac.signal)) this.logOnce("send", `notify-send failed: ${msg(e)}`);
    } finally {
      if (this.waiting.get(n.group) === ac) this.waiting.delete(n.group);
    }
  }
}
