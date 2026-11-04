// A kit draft's test build (docs/usage/jam/kits-drafts.md#test-build), run
// from VS Code: start it, stream its log into an output channel, and end
// with its outcome. Cancelling stops the build on the Jam.
import type { AdminApi, KitDraftBuild } from "../admin/api";

export type BuildApi = Pick<AdminApi, "startBuild" | "buildStatus" | "cancelBuild">;
export type BuildOut = { append(s: string): void; appendLine(s: string): void };
export type Cancel = { readonly isCancellationRequested: boolean };

// POLL_MS is how often a running build's status is read.
export const POLL_MS = 1000;

const wait = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));

export async function runBuild(api: BuildApi, kit: string, etag: string, out: BuildOut, cancel: Cancel, sleep: () => Promise<void> = () => wait(POLL_MS)): Promise<KitDraftBuild> {
  out.appendLine(`Building kit ${kit}'s draft…`);
  await api.startBuild(kit, etag);
  let since = 0;
  for (;;) {
    const v = await api.buildStatus(kit, since);
    if (v.log_skipped) out.appendLine("… earlier output dropped");
    if (v.log) out.append(v.log);
    since = v.log_end;
    if (v.state !== "running") return v;
    await sleep();
    if (cancel.isCancellationRequested) {
      const c = await api.cancelBuild(kit);
      out.appendLine("Cancelled.");
      return c;
    }
  }
}
