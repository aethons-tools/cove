// One window notifies per event: each claims a lock file named for the event id,
// and only the creator wins. Files are only created, never held, so a crash
// leaves nothing stale; old ones are swept.
import { createHash } from "node:crypto";
import { join } from "node:path";

export const NOTIFY_UNFOCUSED_DELAY_MS = 250;
export const NOTIFY_LOCK_TTL_MS = 15 * 60_000;
export const NOTIFY_SWEEP_MS = 60_000;

export type LockDeps = {
  dir: string;
  sleep: (ms: number) => Promise<void>;
  now: () => number;
  fs: Pick<typeof import("node:fs/promises"), "mkdir" | "open" | "readdir" | "stat" | "unlink">;
  // onError hears the first unexpected fs error (the window then notifies on
  // every event), so the caller can log why.
  onError?: (e: unknown) => void;
};

export class NotifyLock {
  private lastSweep = -Infinity;
  private reported = false;

  constructor(private d: LockDeps) {}

  // claim reports whether this window should notify. Unfocused windows wait
  // first, so a focused one wins. An unexpected fs error claims: two
  // notifications beat none.
  async claim(id: string, focused: boolean): Promise<boolean> {
    if (!focused) await this.d.sleep(NOTIFY_UNFOCUSED_DELAY_MS);
    try {
      await this.d.fs.mkdir(this.d.dir, { recursive: true });
      const name = createHash("sha256").update(id).digest("hex").slice(0, 32) + ".lock";
      const h = await this.d.fs.open(join(this.d.dir, name), "wx");
      await h.close();
      await this.sweep();
      return true;
    } catch (e) {
      if ((e as NodeJS.ErrnoException)?.code === "EEXIST") return false;
      this.report(e);
      return true;
    }
  }

  private report(e: unknown): void {
    if (this.reported) return;
    this.reported = true;
    try {
      this.d.onError?.(e);
    } catch {
      // reporting must not fail a claim
    }
  }

  private async sweep(): Promise<void> {
    const now = this.d.now();
    if (now - this.lastSweep < NOTIFY_SWEEP_MS) return;
    this.lastSweep = now;
    try {
      for (const n of await this.d.fs.readdir(this.d.dir)) {
        if (!n.endsWith(".lock")) continue;
        try {
          const p = join(this.d.dir, n);
          if (now - (await this.d.fs.stat(p)).mtimeMs > NOTIFY_LOCK_TTL_MS) await this.d.fs.unlink(p);
        } catch {
          // another window got there first, or it is unreadable
        }
      }
    } catch {
      // best effort
    }
  }
}
