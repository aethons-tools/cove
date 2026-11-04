import { describe, expect, it, vi } from "vitest";
import { runBuild, type BuildApi } from "../src/kits/build";
import { ApiError } from "../src/api";
import type { KitDraftBuild } from "../src/admin/api";

const b = (o: Partial<KitDraftBuild>): KitDraftBuild => ({ kit: "web", manager: "m", state: "running", current: true, log: "", log_from: 0, log_end: 0, ...o });

function setup(statuses: KitDraftBuild[], start: () => Promise<KitDraftBuild> = async () => b({ id: "kdb_1" })) {
  const api = {
    startBuild: vi.fn(start),
    buildStatus: vi.fn(async (_k: string, _since: number) => statuses.shift() ?? b({ state: "succeeded" })),
    cancelBuild: vi.fn(async () => b({ state: "failed", error: "cancelled" })),
  } satisfies BuildApi;
  const lines: string[] = [];
  const out = { append: (s: string) => void lines.push(s), appendLine: (s: string) => void lines.push(`${s}\n`), show: vi.fn() };
  let cancelled = false;
  const cancel = { get isCancellationRequested() { return cancelled; }, cancel: () => void (cancelled = true) };
  return { api, out, lines, cancel };
}

describe("runBuild", () => {
  it("streams each log chunk once, polling from the last end", async () => {
    const t = setup([b({ log: "#1 a\n", log_end: 5 }), b({ log: "#2 b\n", log_from: 5, log_end: 10 }), b({ state: "succeeded", log: "built\n", log_from: 10, log_end: 16 })]);
    const v = await runBuild(t.api, "web", '"e"', t.out, t.cancel, async () => undefined);
    expect(v.state).toBe("succeeded");
    expect(t.api.startBuild).toHaveBeenCalledWith("web", '"e"');
    expect(t.api.buildStatus.mock.calls.map((c) => c[1])).toEqual([0, 5, 10]);
    expect(t.lines.join("")).toContain("#1 a\n#2 b\nbuilt\n");
  });
  it("notes dropped output", async () => {
    const t = setup([b({ state: "failed", error: "boom\nmore", log: "tail\n", log_from: 900, log_end: 905, log_skipped: true })]);
    const v = await runBuild(t.api, "web", '"e"', t.out, t.cancel, async () => undefined);
    expect(v.state).toBe("failed");
    expect(t.lines.join("")).toContain("… earlier output dropped");
  });
  it("cancels the build when asked", async () => {
    const t = setup([b({}), b({})]);
    const sleep = vi.fn(async () => t.cancel.cancel());
    const v = await runBuild(t.api, "web", '"e"', t.out, t.cancel, sleep);
    expect(t.api.cancelBuild).toHaveBeenCalledWith("web");
    expect(v.error).toBe("cancelled");
  });
  it("passes on the Jam's refusal (no manager can build)", async () => {
    const t = setup([], async () => {
      throw new ApiError("test builds need a connected Studio Manager", 503, "unavailable");
    });
    await expect(runBuild(t.api, "web", '"e"', t.out, t.cancel, async () => undefined)).rejects.toThrow(/Studio Manager/);
  });
});
