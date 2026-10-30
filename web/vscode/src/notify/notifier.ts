// Notifier turns the stream's `notify` events into a VS Code notification and,
// while VS Code isn't focused, an OS notification. Pure: every VS Code call is a dep.
// A notification's text is someone's message, so it is never logged.
import type { LiveEvent } from "../live.js";
import { inertText, notifyTarget, parseNotify, targetLink } from "./event.js";
import type { ChannelGate, NotifyKind, NotifyTarget } from "./event.js";
import type { NotifyLock } from "./lock.js";
import type { OsNotifier } from "./os.js";

export type NotifierDeps = {
  gate: Pick<ChannelGate, "admit">;
  lock: Pick<NotifyLock, "claim">;
  os: Pick<OsNotifier, "show">;
  focused: () => boolean;
  jamUrl: () => string | Error;
  scheme: string;
  now: () => number;
  toast: (kind: NotifyKind, message: string, action: "Open") => Thenable<string | undefined>;
  // goTo goes to a conversation: its studio window, else its hub tab.
  goTo: (channel: string, session: string) => unknown;
  log: (m: string) => void;
};

const errText = (e: unknown): string => (e instanceof Error ? e.message : String(e));

export class Notifier {
  constructor(private d: NotifierDeps) {}

  // handle ignores everything but event "notify". It never throws, and never
  // waits for the toast or the OS notification.
  async handle(e: LiveEvent): Promise<void> {
    if (e.event !== "notify") return;
    const n = parseNotify(e.data);
    if (!n) return this.log(`notify: dropped a malformed event (${e.data.length} chars)`);
    // The gate sees every event before the lock, so every window drops the same ones.
    if (!this.d.gate.admit(n, this.d.now())) return;
    const url = this.d.jamUrl();
    if (url instanceof Error) return;
    if (!(await this.d.lock.claim(n.id, this.d.focused()))) return;
    const t = notifyTarget(n);
    const open = () => this.open(t);
    try {
      // VS Code parses links in the text: a message must not become one.
      this.d.toast(n.kind, inertText(`${n.title}: ${n.body}`), "Open").then(
        (a) => {
          if (a === "Open") open();
        },
        (x: unknown) => this.log(`notify: the notification failed: ${errText(x)}`),
      );
    } catch (x) {
      this.log(`notify: the notification failed: ${errText(x)}`);
    }
    if (!this.d.focused()) void this.d.os.show({ title: n.title, body: n.body, link: targetLink(t, this.d.scheme), group: n.channel, onClick: open });
  }

  private open(t: NotifyTarget): void {
    Promise.resolve()
      .then(() => this.d.goTo(t.channel, t.session))
      .catch((x: unknown) => this.log(`notify: opening failed: ${errText(x)}`));
  }

  private log(m: string): void {
    try {
      this.d.log(m);
    } catch {
      /* logging must not fail a notification */
    }
  }
}
