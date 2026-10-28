import type { TreeNode } from "../api/types";

// ancestors are the tree paths above path: its proper prefixes at a "/"
// boundary below "config" ("config/projects/acme/agents/coder" →
// config/projects, config/projects/acme, config/projects/acme/agents).
export function ancestors(path: string): string[] {
  const s = path.split("/");
  const out: string[] = [];
  for (let i = 2; i < s.length; i++) out.push(s.slice(0, i).join("/"));
  return out;
}

// filterTree keeps the nodes whose label contains q (case-insensitively) —
// with all their children — and the ancestors of any; q "" keeps everything.
export function filterTree(nodes: TreeNode[], q: string): TreeNode[] {
  const needle = q.trim().toLowerCase();
  if (needle === "") return nodes;
  const walk = (ns: TreeNode[]): TreeNode[] => ns.flatMap((n) => {
    if (n.label.toLowerCase().includes(needle)) return [n];
    const children = walk(n.children);
    return children.length > 0 ? [{ ...n, children }] : [];
  });
  return walk(nodes);
}

const KEY = "jam-admin-tree";

// loadOpen is the remembered set of open branches (by path).
export function loadOpen(): Set<string> {
  try {
    const v: unknown = JSON.parse(localStorage.getItem(KEY) ?? "[]");
    return new Set(Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : []);
  } catch {
    return new Set();
  }
}

export function saveOpen(open: Set<string>): void {
  try { localStorage.setItem(KEY, JSON.stringify([...open])); } catch { /* storage off: not remembered */ }
}
