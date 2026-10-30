// Reports push focus: the channel of the first attached pane that is visible,
// in a focused window, and showing a channel. Mirrors web/app useFocusReport.
export const FOCUS_REFRESH_MS = 30_000;

type PaneFocus = { visible: boolean; channel: string | null };

export class FocusReporter {
  private panes = new Map<string, PaneFocus>();
  private focused = false;
  private last: string | null = null;
  private timer: ReturnType<typeof setInterval> | undefined;

  constructor(
    private d: {
      post: (channel: string | null) => Promise<void>;
      setInterval: typeof setInterval;
      clearInterval: typeof clearInterval;
    },
  ) {}

  pane(id: string, s: PaneFocus): void {
    this.panes.set(id, { visible: s.visible, channel: s.channel });
    this.update();
  }

  drop(id: string): void {
    this.panes.delete(id);
    this.update();
  }

  windowFocused(f: boolean): void {
    this.focused = f;
    this.update();
  }

  dispose(): void {
    this.clearTimer();
    this.panes.clear();
    if (this.last !== null) {
      this.last = null;
      this.send(null);
    }
  }

  private clearTimer(): void {
    if (this.timer !== undefined) this.d.clearInterval(this.timer);
    this.timer = undefined;
  }

  private send(channel: string | null): void {
    try {
      this.d.post(channel).catch(() => undefined);
    } catch {
      // posting is best effort
    }
  }

  private update(): void {
    let cur: string | null = null;
    if (this.focused) {
      for (const p of this.panes.values()) {
        if (p.visible && p.channel) {
          cur = p.channel;
          break;
        }
      }
    }
    if (cur === this.last) return;
    this.last = cur;
    this.clearTimer();
    this.send(cur);
    if (cur !== null) this.timer = this.d.setInterval(() => this.send(cur), FOCUS_REFRESH_MS);
  }
}
