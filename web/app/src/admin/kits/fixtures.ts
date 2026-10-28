import type { KitDetail, KitView } from "../api/types";

export const kitView = (v: Partial<KitView> = {}): KitView => ({
  n: 2, parseErr: "", rawShown: "", masked: false, hidden: false, pushText: "kind: studio\nprompt: two\n", yaml: "kind: studio\nprompt: two\n",
  base: "blessed default", image: "", egress: ["github.com"], ceiling: ["github.com"], excluded: [], buildArgs: [], secrets: [], mcpServers: [], notes: [],
  prompt: "two", digest: "abc123def456", ...v,
});

export const kit = (k: Partial<KitDetail> = {}): KitDetail => ({
  name: "web", managed: null, current: 2,
  versions: [{ n: 1, prev: 0, current: false, digest: "aaa", pushed: "2026-10-27 10:00", source: null }, { n: 2, prev: 1, current: true, digest: "abc123def456", pushed: "2026-10-28 10:00", source: null }],
  view: kitView(), diff: null, uses: [{ project: "acme", role: "coder", implicit: false }], chain: [], chainErr: "", effective: null, usedByKits: [],
  isDefault: false, source: null, setWith: "", canSource: true, draft: null,
  files: { n: 2, size: "1 KiB", rows: [{ path: "kit.yml", label: "kit.yml", mode: "0644", size: 30 }, { path: "a b.txt", label: "a b.txt", mode: "0644", size: 5 }], hidden: 0 },
  filesDiff: null, file: null, attn: null, ...k,
});
