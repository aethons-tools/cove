import type { DraftPage, DraftView, KitDetail, KitView } from "../api/types";

export const kitView = (v: Partial<KitView> = {}): KitView => ({
  n: 2, parseErr: "", rawShown: "", masked: false, hidden: false, pushText: "kind: studio\nprompt: two\n", yaml: "kind: studio\nprompt: two\n",
  base: "blessed default", image: "", egress: ["github.com"], ceiling: ["github.com"], excluded: [], buildArgs: [], secrets: [], mcpServers: [], notes: [],
  prompt: "two", digest: "abc123def456", ...v,
});

export const kit = (k: Partial<KitDetail> = {}): KitDetail => ({
  name: "web", current: 2,
  versions: [{ n: 1, prev: 0, current: false, digest: "aaa", pushed: "2026-10-27 10:00", source: null }, { n: 2, prev: 1, current: true, digest: "abc123def456", pushed: "2026-10-28 10:00", source: null }],
  view: kitView(), diff: null, uses: [{ project: "acme", role: "coder", implicit: false }], chain: [], chainErr: "", effective: null, usedByKits: [],
  isDefault: false, source: null, setWith: "", canSource: true, draft: null,
  files: { n: 2, size: "1 KiB", rows: [{ path: "kit.yml", label: "kit.yml", mode: "0644", size: 30 }, { path: "a b.txt", label: "a b.txt", mode: "0644", size: 5 }], hidden: 0 },
  filesDiff: null, file: null, attn: null, ...k,
});


export const draftView = (d: Partial<DraftView> = {}): DraftView => ({
  base: 2, current: 2, superseded: false, etag: "e1", actor: "local", updated: "2026-10-28 10:00",
  maxFile: "4 MiB", problems: [], size: "1 KiB",
  tree: [
    { depth: 0, name: "docs", dir: true, path: "", label: "docs", mode: "", size: 0, change: "" },
    { depth: 1, name: "a.md", dir: false, path: "docs/a.md", label: "docs/a.md", mode: "0644", size: 4, change: "modified" },
    { depth: 0, name: "kit.yml", dir: false, path: "kit.yml", label: "kit.yml", mode: "0644", size: 14, change: "" },
  ],
  removed: [{ depth: 0, name: "gone.txt", dir: false, path: "gone.txt", label: "gone.txt", mode: "0644", size: 4, change: "removed" }],
  editor: null, fileMissing: false,
  changes: { from: 2, to: 0, rows: [{ change: "modified", path: "docs/a.md", label: "docs/a.md", mode: "0644", size: "4 B → 6 B" }], more: 0, unchanged: 1 },
  yamlDiff: null, fileDiff: null, ...d,
});

export const draftPage = (p: Partial<DraftPage> = {}): DraftPage => ({ name: "web", current: 2, versions: [1, 2], draft: draftView(), published: null, ...p });
