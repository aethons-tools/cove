// The Jams the extension knows: jam.url (the default) and jam.urls, and the
// per-Jam names of their token profiles, hub files and pending requests. Pure.
import { normalizeJamUrl } from "./config";

// jamList is [jam.url, ...jam.urls], normalised, valid ones only, without
// duplicates; bad holds the jam.urls entries that aren't Jam addresses.
export function jamList(url: string, urls: unknown): { list: string[]; bad: string[] } {
  const list: string[] = [];
  const bad: string[] = [];
  const add = (raw: string, report: boolean) => {
    const u = normalizeJamUrl(raw);
    if (u instanceof Error) {
      if (report) bad.push(raw);
    } else if (!list.includes(u)) list.push(u);
  };
  add(url, false);
  if (Array.isArray(urls)) for (const r of urls) add(String(r), true);
  return { list, bad };
}

// jamSlug is url's host as a file-name and profile-name part.
export function jamSlug(url: string): string {
  return new URL(url).host.replace(/[^A-Za-z0-9.-]/g, "-");
}

// appFor is the jam CLI token profile (--app) for url: the default Jam
// shares the CLI's "default"; others get their own.
export const appFor = (url: string, defaultUrl: string | undefined): string => (url === defaultUrl ? "default" : `vscode-${jamSlug(url)}`);

export const hubFileFor = (url: string): string => `Jam Hub (${jamSlug(url)}).code-workspace`;

export const pendingFileFor = (url: string): string => `pending-${jamSlug(url)}.json`;

// hubJamOf is the listed Jam whose hub file is fileName.
export const hubJamOf = (fileName: string, list: string[]): string | undefined => list.find((u) => hubFileFor(u) === fileName);
