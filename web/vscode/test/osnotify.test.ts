import { describe, expect, it, vi } from "vitest";
import { NOTIFY_WAIT_MS, OsNotifier, escapeMarkup, notifySendArgs, osascriptArgs, terminalNotifierArgs } from "../src/notify/os";
import type { OsDeps, OsNote } from "../src/notify/os";

const note = (over: Partial<OsNote> = {}): OsNote => ({
  title: "T",
  body: "B",
  link: "https://jam.example/app/#/chn_1",
  group: "chn_1",
  onClick: vi.fn(),
  ...over,
});

type Call = { file: string; args: string[]; o: { timeout: number; signal?: AbortSignal } };
type Reply = { stdout: string } | Error | ((c: Call) => Promise<{ stdout: string }>);

function rig(platform: NodeJS.Platform, replies: (c: Call, n: number) => Reply = () => ({ stdout: "" })) {
  const calls: Call[] = [];
  const logs: string[] = [];
  const chmod = vi.fn(async (_p: string, _m: number) => {});
  const deps: OsDeps = {
    platform,
    extensionPath: "/ext",
    execFile: async (file, args, o) => {
      const c = { file, args, o };
      calls.push(c);
      const r = replies(c, calls.length - 1);
      if (typeof r === "function") return r(c);
      if (r instanceof Error) throw r;
      return r;
    },
    chmod,
    log: (m) => logs.push(m),
  };
  return { n: new OsNotifier(deps), calls, logs, chmod };
}
const errno = (code: string | number, extra: object = {}) => Object.assign(new Error(String(code)), { code }, extra);
const BIN = "/ext/dist/vendor/mac.noindex/terminal-notifier.app/Contents/MacOS/terminal-notifier";

// A notify-send that stays open until aborted or released.
function waiting(c: Call, release: Promise<string>): Promise<{ stdout: string }> {
  return new Promise((res, rej) => {
    c.o.signal?.addEventListener("abort", () => rej(errno("ABORT_ERR", { name: "AbortError" })));
    void release.then((s) => res({ stdout: s }));
  });
}
const helpWith = "Usage: notify-send [OPTION...]\n  -A, --action=[NAME=]Text";

describe("terminalNotifierArgs", () => {
  it("passes values as separate argv, each title and message after a U+200B", () => {
    for (const b of ["-help", "[x]", "(edited) x", "{a}", "plain"]) {
      const a = terminalNotifierArgs(note({ title: "-t", body: b }));
      expect(a).toEqual(["-title", "\u200b-t", "-message", "\u200b" + b, "-open", "https://jam.example/app/#/chn_1", "-group", "chn_1"]);
    }
    const a = terminalNotifierArgs(note({ body: "$(rm -rf ~)" }));
    expect(a[3]).toBe("\u200b$(rm -rf ~)");
    expect(a).toHaveLength(8);
  });
});

describe("osascriptArgs / notifySendArgs / escapeMarkup", () => {
  it("osascript takes title and body as argv items after --", () => {
    expect(osascriptArgs(note({ title: 'a"b', body: "x" }))).toEqual([
      "-e", "on run argv", "-e", "display notification (item 2 of argv) with title (item 1 of argv)", "-e", "end run", "--", 'a"b', "x",
    ]);
  });
  it("notify-send args", () => {
    expect(notifySendArgs(note({ body: "<b>&</b>" }), true, "/i.svg")).toEqual([
      "--app-name=Jam", "--icon=/i.svg", "--action=default=Open", "--wait", "--", "T", "&lt;b&gt;&amp;&lt;/b&gt;",
    ]);
    expect(notifySendArgs(note(), false, "/i.svg")).toEqual(["--app-name=Jam", "--icon=/i.svg", "--", "T", "B"]);
    expect(escapeMarkup("a & <b>")).toBe("a &amp; &lt;b&gt;");
  });
});

describe("OsNotifier darwin", () => {
  it("runs the vendored binary from the extension path, after chmod", async () => {
    const r = rig("darwin");
    await r.n.show(note());
    expect(r.chmod).toHaveBeenCalledWith(BIN, 0o755);
    expect(r.calls).toHaveLength(1);
    expect(r.calls[0].file).toBe(BIN);
    expect(r.calls[0].o.timeout).toBe(10_000);
    await r.n.show(note());
    expect(r.chmod).toHaveBeenCalledTimes(1);
  });

  it("ignores a chmod failure", async () => {
    const r = rig("darwin");
    r.chmod.mockRejectedValue(new Error("EROFS"));
    await r.n.show(note());
    expect(r.calls[0].file).toBe(BIN);
  });

  it("a spawn failure (EACCES) switches to osascript for the session, and logs once", async () => {
    const r = rig("darwin", (c) => (c.file === BIN ? errno("EACCES") : { stdout: "" }));
    await r.n.show(note({ title: "T1", body: "B1" }));
    await r.n.show(note({ title: "T2", body: "B2" }));
    expect(r.logs).toHaveLength(1);
    expect(r.calls.map((c) => c.file)).toEqual([BIN, "osascript", "osascript"]);
    const last = r.calls[2].args;
    expect(last.slice(last.indexOf("--") + 1)).toEqual(["T2", "B2"]);
  });

  it("death by a signal (Gatekeeper) is a spawn failure too", async () => {
    const r = rig("darwin", (c) => (c.file === BIN ? errno(null as unknown as number, { signal: "SIGKILL", killed: true }) : { stdout: "" }));
    await r.n.show(note());
    await r.n.show(note());
    expect(r.calls.map((c) => c.file)).toEqual([BIN, "osascript", "osascript"]);
  });

  it("a non-zero exit falls back for that note only", async () => {
    const r = rig("darwin", (_c, i) => (i === 0 ? errno(1) : { stdout: "" }));
    await r.n.show(note());
    await r.n.show(note());
    expect(r.calls.map((c) => c.file)).toEqual([BIN, "osascript", BIN]);
    expect(r.logs).toHaveLength(1);
  });

  it("never throws, even when osascript fails too", async () => {
    const r = rig("darwin", () => errno("ENOENT"));
    await expect(r.n.show(note())).resolves.toBeUndefined();
    await expect(r.n.show(note())).resolves.toBeUndefined();
    expect(r.logs.length).toBeLessThanOrEqual(2);
  });
});

describe("OsNotifier linux", () => {
  it("with --action waits and calls onClick on default", async () => {
    const r = rig("linux", (c) => (c.args[0] === "--help" ? { stdout: helpWith } : { stdout: "default\n" }));
    const n = note();
    await r.n.show(n);
    expect(r.calls[0]).toMatchObject({ file: "notify-send", args: ["--help"] });
    expect(r.calls[1].args).toEqual(["--app-name=Jam", "--icon=/ext/media/jam.svg", "--action=default=Open", "--wait", "--", "T", "B"]);
    expect(r.calls[1].o.timeout).toBe(NOTIFY_WAIT_MS);
    expect(r.calls[1].o.signal).toBeInstanceOf(AbortSignal);
    expect(n.onClick).toHaveBeenCalledTimes(1);
  });

  it("closes without a click: no onClick", async () => {
    const r = rig("linux", (c) => ({ stdout: c.args[0] === "--help" ? helpWith : "" }));
    const n = note();
    await r.n.show(n);
    expect(n.onClick).not.toHaveBeenCalled();
    expect(r.logs).toEqual([]);
  });

  it("timeout or abort is a quiet close", async () => {
    const r = rig("linux", (c) =>
      c.args[0] === "--help" ? { stdout: helpWith } : errno(null as unknown as number, { signal: "SIGTERM", killed: true }),
    );
    const n = note();
    await r.n.show(n);
    expect(n.onClick).not.toHaveBeenCalled();
    expect(r.logs).toEqual([]);
  });

  it("keeps one waiting child per group", async () => {
    let n = 0;
    const releases: ((s: string) => void)[] = [];
    const r = rig("linux", (c) => {
      if (c.args[0] === "--help") return { stdout: helpWith };
      n++;
      return (cc) => waiting(cc, new Promise<string>((res) => releases.push(res)));
    });
    const a = note({ group: "chn_a" });
    const a2 = note({ group: "chn_a" });
    const b = note({ group: "chn_b" });
    const pa = r.n.show(a);
    await vi.waitFor(() => expect(n).toBe(1));
    const pb = r.n.show(b);
    await vi.waitFor(() => expect(n).toBe(2));
    expect(r.calls[1].o.signal?.aborted).toBe(false);
    const pa2 = r.n.show(a2);
    await vi.waitFor(() => expect(n).toBe(3));
    expect(r.calls[1].o.signal?.aborted).toBe(true);
    expect(r.calls[2].o.signal?.aborted).toBe(false);
    await pa;
    expect(a.onClick).not.toHaveBeenCalled();
    releases[2]("default");
    releases[1]("");
    await Promise.all([pb, pa2]);
    expect(a2.onClick).toHaveBeenCalledTimes(1);
    expect(r.logs).toEqual([]);
  });

  it("escapes markup in the body only", async () => {
    const r = rig("linux", (c) => ({ stdout: c.args[0] === "--help" ? helpWith : "" }));
    await r.n.show(note({ title: "<i>", body: "<b>&</b>" }));
    const a = r.calls[1].args;
    expect(a.slice(-2)).toEqual(["<i>", "&lt;b&gt;&amp;&lt;/b&gt;"]);
  });

  it("without --action notifies without waiting", async () => {
    const r = rig("linux", (c) => ({ stdout: c.args[0] === "--help" ? "Usage: notify-send" : "" }));
    await r.n.show(note());
    expect(r.calls[1].args).toEqual(["--app-name=Jam", "--icon=/ext/media/jam.svg", "--", "T", "B"]);
    expect(r.calls[1].o.timeout).toBe(10_000);
  });

  it("without notify-send logs once and never retries the probe", async () => {
    const r = rig("linux", () => errno("ENOENT"));
    await r.n.show(note());
    await r.n.show(note());
    expect(r.calls).toHaveLength(1);
    expect(r.logs).toHaveLength(1);
  });

  it("a real notify-send failure is logged once", async () => {
    const r = rig("linux", (c) => (c.args[0] === "--help" ? { stdout: helpWith } : errno(1)));
    await r.n.show(note());
    await r.n.show(note());
    expect(r.logs).toHaveLength(1);
  });

  it("dispose aborts a waiting notify-send", async () => {
    const r = rig("linux", (c) => (c.args[0] === "--help" ? { stdout: helpWith } : (cc) => waiting(cc, new Promise<string>(() => {}))));
    const n = note();
    const p = r.n.show(n);
    await vi.waitFor(() => expect(r.calls).toHaveLength(2));
    r.n.dispose();
    await p;
    expect(r.calls[1].o.signal?.aborted).toBe(true);
    expect(n.onClick).not.toHaveBeenCalled();
    expect(r.logs).toEqual([]);
    await r.n.show(note());
    expect(r.calls).toHaveLength(2);
  });
});

describe("OsNotifier elsewhere", () => {
  it("win32 does nothing", async () => {
    const r = rig("win32");
    await r.n.show(note());
    expect(r.calls).toEqual([]);
  });

  it("show never throws", async () => {
    const r = rig("linux", () => {
      throw new Error("boom");
    });
    await expect(r.n.show(note())).resolves.toBeUndefined();
    const d = rig("darwin");
    d.chmod.mockImplementation(() => {
      throw new Error("sync");
    });
    await expect(d.n.show(note())).resolves.toBeUndefined();
  });
});
