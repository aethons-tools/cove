// Fans the window's one live stream out to every attached pane.
import type { ExtToPane } from "../../../app/src/bridge/protocol.js" with { "resolution-mode": "import" };
import type { LiveEvent, LiveStream } from "../live.js";

export interface PanePort {
  readonly id: string;
  post(m: ExtToPane): void;
}

type Forwarded = Extract<ExtToPane, { type: "live" }>["event"];
const FORWARD = new Set<string>(["open", "changed", "presence", "board"]);

export class PaneHub {
  private panes = new Set<PanePort>();
  private unsub: (() => void) | undefined;

  constructor(
    private live: LiveStream,
    private onEvent?: (e: LiveEvent) => void,
  ) {}

  attach(p: PanePort): () => void {
    this.panes.add(p);
    if (this.panes.size === 1) this.unsub = this.live.subscribe((e) => this.handle(e));
    return () => {
      if (!this.panes.delete(p)) return;
      if (this.panes.size === 0) {
        this.unsub?.();
        this.unsub = undefined;
      }
    };
  }

  broadcast(m: ExtToPane): void {
    for (const p of [...this.panes]) p.post(m);
  }

  private handle(e: LiveEvent): void {
    this.onEvent?.(e);
    if (FORWARD.has(e.event)) this.broadcast({ type: "live", event: e.event as Forwarded, data: e.data });
  }
}
