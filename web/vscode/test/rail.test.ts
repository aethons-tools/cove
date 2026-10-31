import { describe, expect, it, vi } from "vitest";
import type * as vs from "vscode";
import type { ExtToPane, PaneState } from "../../app/src/bridge/protocol.js" with { "resolution-mode": "import" };
import type { Session } from "../src/auth";
import { CONNECTED_BEAT_MS } from "../src/open";
import { RailViewProvider } from "../src/panes/rail";
import { fakeWebviewView, Uri } from "./vscode";

const JAM = "https://jam.example.com";
const SES = "ses_0abcdefghjkmnpqrstvwxyz012";
const SIGNED_IN: Session = { url: JAM, token: "tok", user: "u", expiresAt: "" };
type View = ReturnType<typeof fakeWebviewView>;

function setup(windows: () => string[] = () => [SES]) {
  let sess: Session | undefined = SIGNED_IN;
  const subs = new Set<(s: Session | undefined) => void>();
  const auth = {
    session: async (u: string) => (sess && sess.url === u ? sess : undefined),
    onChange: (cb: (s: Session | undefined) => void) => (subs.add(cb), () => void subs.delete(cb)),
  };
  const setSession = (s: Session | undefined) => {
    sess = s;
    for (const cb of [...subs]) cb(s);
  };
  const win = vi.fn(windows);
  const provider = new RailViewProvider({
    extensionUri: Uri.parse("file:///ext") as unknown as vs.Uri,
    hub: { attach: () => () => undefined },
    router: { handle: vi.fn() },
    thinking: { subscribe: vi.fn() },
    jamUrl: () => JAM,
    windowStudio: undefined,
    perform: vi.fn(),
    executeCommand: vi.fn(async () => undefined),
    auth,
    windows: win,
  });
  const open = (): View => {
    const v = fakeWebviewView();
    provider.resolveWebviewView(v as unknown as vs.WebviewView, {} as vs.WebviewViewResolveContext, {} as vs.CancellationToken);
    return v;
  };
  return { provider, open, setSession, win };
}
const lastState = (v: View) => (v.posted as ExtToPane[]).filter((m) => m.type === "state").map((m) => (m as { state: PaneState }).state).at(-1);

describe("RailViewProvider", () => {
  it("is jam.rail and renders the rail pane", () => {
    const s = setup();
    expect(s.provider.viewType).toBe("jam.rail");
    expect(s.open().webview.html).toContain('data-pane="rail"');
  });

  it("state carries the open windows", async () => {
    const v = setup().open();
    v.receive({ type: "ready", pane: "rail" });
    await vi.waitFor(() => expect(lastState(v)).toEqual({ signedIn: true, target: null, windows: [SES] }));
  });

  it("signed out posts signedIn false, and signing in again posts true", async () => {
    const s = setup();
    const v = s.open();
    s.setSession(undefined);
    await vi.waitFor(() => expect(lastState(v)).toMatchObject({ signedIn: false }));
    s.setSession(SIGNED_IN);
    await vi.waitFor(() => expect(lastState(v)).toMatchObject({ signedIn: true }));
  });

  it("setBadge before and after resolve sets view.badge", () => {
    const s = setup();
    s.provider.setBadge({ value: 2, tooltip: "2 conversations need you" });
    const v = s.open() as View & { badge?: unknown };
    expect(v.badge).toEqual({ value: 2, tooltip: "2 conversations need you" });
    s.provider.setBadge(undefined);
    expect(v.badge).toBeUndefined();
  });

  it("refreshes every CONNECTED_BEAT_MS while resolved, and stops on dispose", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    try {
      const s = setup();
      const v = s.open();
      const before = s.win.mock.calls.length;
      vi.advanceTimersByTime(CONNECTED_BEAT_MS);
      await vi.waitFor(() => expect(s.win.mock.calls.length).toBeGreaterThan(before));
      v.dispose();
      const after = s.win.mock.calls.length;
      vi.advanceTimersByTime(CONNECTED_BEAT_MS * 3);
      expect(s.win.mock.calls.length).toBe(after);
    } finally {
      vi.useRealTimers();
    }
  });
});
