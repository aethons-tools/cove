import { useSyncExternalStore } from "react";

// The config branch the admin console edits (docs/usage/jam/config-branches.md):
// "" = the followed branch, read-only. It is kept in localStorage, so it
// survives a reload, and sent on every admin API request (the server serves
// only config paths from the branch).
const KEY = "jam.admin.configBranch";
const listeners = new Set<() => void>();

export function getBranch(): string {
  try {
    return localStorage.getItem(KEY) ?? "";
  } catch {
    return "";
  }
}

export function setBranch(name: string): void {
  try {
    if (name) localStorage.setItem(KEY, name);
    else localStorage.removeItem(KEY);
  } catch {
    // private mode: the context lasts the page
  }
  for (const l of [...listeners]) l();
}

function subscribe(l: () => void): () => void {
  listeners.add(l);
  return () => listeners.delete(l);
}

// useBranch is the current branch, following setBranch.
export function useBranch(): string {
  return useSyncExternalStore(subscribe, getBranch);
}

// branchHeaders is the header naming the branch, when one is open.
export function branchHeaders(): Record<string, string> {
  const b = getBranch();
  return b ? { "X-Jam-Config-Branch": b } : {};
}
