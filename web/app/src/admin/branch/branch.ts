import { useSyncExternalStore } from "react";

// The config branch the admin console edits (docs/usage/jam/config-branches.md):
// "" = the followed branch, read-only. Per tab (sessionStorage: it survives
// a reload, and another tab's switch never changes where this one saves),
// and sent on every admin API request (the server serves only config paths
// from the branch). head is the branch head this tab last read, from the
// server's X-Jam-Config-Head: a write names it, so one made on a page read
// before another tab's save is refused as stale.
const KEY = "jam.admin.configBranch";
const listeners = new Set<() => void>();
let head = "";

export function getBranch(): string {
  try {
    return sessionStorage.getItem(KEY) ?? "";
  } catch {
    return "";
  }
}

function notify() {
  for (const l of [...listeners]) l();
}

export function setBranch(name: string): void {
  try {
    if (name) sessionStorage.setItem(KEY, name);
    else sessionStorage.removeItem(KEY);
  } catch {
    // private mode: the context lasts the page
  }
  head = "";
  notify();
}

// getHead is the head this tab last read on its branch ("" = none yet).
export function getHead(): string {
  return head;
}

// sawHead records a branch answer's head.
export function sawHead(h: string | undefined): void {
  if (h && h !== head && getBranch()) {
    head = h;
    notify();
  }
}

function subscribe(l: () => void): () => void {
  listeners.add(l);
  return () => listeners.delete(l);
}

// useBranch is the current branch, following setBranch.
export function useBranch(): string {
  return useSyncExternalStore(subscribe, getBranch);
}

// useConfigEditable is whether this tab may edit the config: only on a
// branch. On the followed branch a page shows its config read-only (the
// server refuses the write), keeping only its live-state controls.
export function useConfigEditable(): boolean {
  return useBranch() !== "";
}

// useHead is the head this tab last read.
export function useHead(): string {
  return useSyncExternalStore(subscribe, getHead);
}

// branchHeaders names the branch, and the head read, when a branch is open.
export function branchHeaders(): Record<string, string> {
  const b = getBranch();
  if (!b) return {};
  return head ? { "X-Jam-Config-Branch": b, "X-Jam-Config-Head": head } : { "X-Jam-Config-Branch": b };
}
