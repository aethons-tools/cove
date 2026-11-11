// The jamkit: file system: a kit's draft on a config branch as a folder
// (docs/modules/ROOT/pages/usage/jam/vscode-kits.adoc). Reads and writes go to the
// draft's file API (docs/usage/jam/kits-drafts.md#file-api); each write is
// conditional on the content this file system last read for that file, so a
// change made elsewhere (the console, another window) is never overwritten:
// the save fails and VS Code's own compare resolves it.
import * as vscode from "vscode";
import type { DraftFiles } from "../../../app/src/admin/api/types.js" with { "resolution-mode": "import" };
import type { AdminApi } from "../admin/api";
import { ApiError } from "../api";
import { KIT_SCHEME, type KitPath, type KitRef, kitKey, kitUri, parseKitUri } from "./uri";

export type KitApi = Pick<AdminApi, "files" | "readFile" | "writeFile" | "deleteFile" | "renameFile">;

// LISTING_TTL_MS is how long a draft's listing answers stats and directory
// reads before it is fetched again (VS Code stats often).
const LISTING_TTL_MS = 2000;

type Listing = { files: DraftFiles; at: number };

export interface MementoLike {
  get<T>(key: string): T | undefined;
  update(key: string, value: unknown): Thenable<void> | Promise<void>;
}

export type KitFsOptions = {
  // state keeps what was read (and each file's mtime) across reloads.
  state?: MementoLike;
  // openKits are the kits shown under the Jam's root folder.
  openKits?: () => KitRef[];
  now?: () => number;
};

const SHAS_KEY = "jam.kitFs.read";
const TIMES_KEY = "jam.kitFs.mtimes";

// COMPARE_HINT is how to get past a refused save: compare re-reads the
// file from the Jam, after which a save goes through.
const COMPARE_HINT = "Run 'File: Compare Active File with Saved' to see the Jam's version, then save again";

export class KitFileSystem implements vscode.FileSystemProvider {
  private changed = new vscode.EventEmitter<vscode.FileChangeEvent[]>();
  readonly onDidChangeFile = this.changed.event;
  private problems = new vscode.EventEmitter<{ ref: KitRef; files: DraftFiles }>();
  // onProblems fires with each fresh listing of a kit's draft.
  readonly onProblems = this.problems.event;
  private gone = new vscode.EventEmitter<KitRef>();
  // onDraftGone fires when the Jam says the kit has no draft.
  readonly onDraftGone = this.gone.event;

  private listings = new Map<string, Listing>();
  private dirs = new Map<string, Set<string>>(); // directories made here, not yet holding a file
  // shas is each file's content as last read here (a save's precondition);
  // times its mtime per content. Both persist in o.state.
  private shas: Map<string, string>;
  private times: Map<string, { sha: string; t: number }>;
  private now: () => number;

  constructor(
    private apiFor: (jam: string, branch: string) => KitApi | undefined | Promise<KitApi | undefined>,
    private o: KitFsOptions = {},
  ) {
    this.now = o.now ?? Date.now;
    this.shas = new Map(Object.entries(o.state?.get<Record<string, string>>(SHAS_KEY) ?? {}));
    this.times = new Map(Object.entries(o.state?.get<Record<string, { sha: string; t: number }>>(TIMES_KEY) ?? {}));
  }

  private setSha(uri: vscode.Uri, sha: string | undefined): void {
    if (sha === undefined) this.shas.delete(uri.toString());
    else this.shas.set(uri.toString(), sha);
    void this.o.state?.update(SHAS_KEY, Object.fromEntries(this.shas));
  }

  // mtime is uri's stat time: it moves forward whenever its content does.
  private mtime(uri: vscode.Uri, sha: string): number {
    const k = uri.toString();
    const have = this.times.get(k);
    if (have?.sha === sha) return have.t;
    const t = Math.max(this.now(), (have?.t ?? 0) + 1);
    this.times.set(k, { sha, t });
    void this.o.state?.update(TIMES_KEY, Object.fromEntries(this.times));
    return t;
  }

  watch(): vscode.Disposable {
    return new vscode.Disposable(() => undefined);
  }

  // rootChanged tells VS Code the Jam's root folder lists other kits now.
  rootChanged(jam: string): void {
    this.changed.fire([{ type: vscode.FileChangeType.Changed, uri: vscode.Uri.from({ scheme: KIT_SCHEME, authority: jam, path: "/" }) }]);
  }

  // refresh drops root's listing so the next read fetches it.
  refresh(root: vscode.Uri): void {
    const p = parseKitUri(root);
    if (p) this.listings.delete(kitKey(p));
  }

  // listing is the kit's draft listing, fetched again past LISTING_TTL_MS.
  async listing(ref: KitRef, fresh = false): Promise<DraftFiles> {
    const key = kitKey(ref);
    const have = this.listings.get(key);
    if (have && !fresh && this.now() - have.at < LISTING_TTL_MS) return have.files;
    const files = await this.call(ref, undefined, (api) => api.files(ref.kit));
    this.store(ref, files);
    return files;
  }

  private store(ref: KitRef, files: DraftFiles): void {
    this.listings.set(kitKey(ref), { files, at: this.now() });
    const dirs = this.dirs.get(kitKey(ref));
    if (dirs) for (const d of [...dirs]) if (files.files.some((f) => f.path.startsWith(`${d}/`))) dirs.delete(d);
    this.problems.fire({ ref, files });
  }

  async stat(uri: vscode.Uri): Promise<vscode.FileStat> {
    if (this.rootChildren(uri) !== undefined) return { type: vscode.FileType.Directory, ctime: 0, mtime: 0, size: 0 };
    const p = this.parse(uri);
    if (p.path === "") return { type: vscode.FileType.Directory, ctime: 0, mtime: 0, size: 0 };
    const l = await this.listing(p);
    const f = l.files.find((x) => x.path === p.path);
    if (f) return { type: vscode.FileType.File, ctime: 0, mtime: this.mtime(uri, f.sha256), size: f.size };
    if (this.isDir(p, l)) return { type: vscode.FileType.Directory, ctime: 0, mtime: 0, size: 0 };
    throw vscode.FileSystemError.FileNotFound(uri);
  }

  async readDirectory(uri: vscode.Uri): Promise<[string, vscode.FileType][]> {
    const top = this.rootChildren(uri);
    if (top !== undefined) return top.map((n) => [n, vscode.FileType.Directory]);
    const p = this.parse(uri);
    const l = await this.listing(p);
    const prefix = p.path === "" ? "" : `${p.path}/`;
    const out = new Map<string, vscode.FileType>();
    const paths = [...l.files.map((f) => f.path), ...[...(this.dirs.get(kitKey(p)) ?? [])].map((d) => `${d}/`)];
    for (const path of paths) {
      if (!path.startsWith(prefix)) continue;
      const rest = path.slice(prefix.length);
      const [head, ...more] = rest.split("/");
      if (!head) continue;
      out.set(head, more.length > 0 ? vscode.FileType.Directory : vscode.FileType.File);
    }
    return [...out.entries()].sort(([a], [b]) => a.localeCompare(b));
  }

  async createDirectory(uri: vscode.Uri): Promise<void> {
    const p = this.parse(uri);
    const key = kitKey(p);
    if (!this.dirs.has(key)) this.dirs.set(key, new Set());
    this.dirs.get(key)!.add(p.path);
    this.changed.fire([{ type: vscode.FileChangeType.Created, uri }]);
  }

  async readFile(uri: vscode.Uri): Promise<Uint8Array> {
    const p = this.parse(uri);
    const r = await this.call(p, uri, (api) => api.readFile(p.kit, p.path));
    this.setSha(uri, r.sha256);
    return r.bytes;
  }

  async writeFile(uri: vscode.Uri, content: Uint8Array, o: { create: boolean; overwrite: boolean }): Promise<void> {
    const p = this.parse(uri);
    const l = await this.listing(p, true);
    const cur = l.files.find((f) => f.path === p.path);
    if (!cur && !o.create) throw vscode.FileSystemError.FileNotFound(uri);
    if (cur && !o.overwrite) throw vscode.FileSystemError.FileExists(uri);
    const read = this.shas.get(uri.toString());
    if (cur && read === undefined) {
      throw vscode.FileSystemError.Unavailable(`${p.path}: Jam can't tell whether it changed since you opened it. ${COMPARE_HINT}.`);
    }
    const cond = cur ? { sha256: read! } : ("create" as const);
    const files = await this.call(p, uri, (api) => api.writeFile(p.kit, p.path, content, cond));
    const now = files.files.find((f) => f.path === p.path);
    if (now) this.setSha(uri, now.sha256);
    this.store(p, files);
    this.changed.fire([{ type: cur ? vscode.FileChangeType.Changed : vscode.FileChangeType.Created, uri }]);
  }

  async delete(uri: vscode.Uri, _o: { recursive: boolean }): Promise<void> {
    const p = this.parse(uri);
    const l = await this.listing(p, true);
    const under = l.files.filter((f) => f.path === p.path || f.path.startsWith(`${p.path}/`));
    const dirs = this.dirs.get(kitKey(p));
    const hadDir = dirs?.delete(p.path) ?? false;
    if (under.length === 0 && !hadDir) throw vscode.FileSystemError.FileNotFound(uri);
    let files = l;
    for (const f of under) {
      const fu = kitUri(p.jam, p.branch, p.kit, f.path);
      files = await this.call(p, fu, (api) => api.deleteFile(p.kit, f.path, this.shas.get(fu.toString()) ?? f.sha256));
      this.setSha(fu, undefined);
    }
    if (under.length > 0) this.store(p, files);
    this.changed.fire([{ type: vscode.FileChangeType.Deleted, uri }]);
  }

  async rename(from: vscode.Uri, to: vscode.Uri, o: { overwrite: boolean }): Promise<void> {
    const a = this.parse(from);
    const b = this.parse(to);
    if (kitKey(a) !== kitKey(b)) throw vscode.FileSystemError.NoPermissions("Files move only within one kit's draft.");
    const l = await this.listing(a, true);
    if (!o.overwrite && l.files.some((f) => f.path === b.path)) throw vscode.FileSystemError.FileExists(to);
    const moves = l.files.filter((f) => f.path === a.path || f.path.startsWith(`${a.path}/`));
    if (moves.length === 0) throw vscode.FileSystemError.FileNotFound(from);
    let files = l;
    for (const f of moves) {
      const dest = b.path + f.path.slice(a.path.length);
      const fu = kitUri(a.jam, a.branch, a.kit, f.path);
      files = await this.call(a, fu, (api) => api.renameFile(a.kit, f.path, dest, this.shas.get(fu.toString()) ?? f.sha256));
      this.setSha(fu, undefined);
    }
    this.store(a, files);
    this.changed.fire([
      { type: vscode.FileChangeType.Deleted, uri: from },
      { type: vscode.FileChangeType.Created, uri: to },
    ]);
  }

  // toggleExec flips file uri's executable bit (VS Code has none) and
  // returns the new one.
  async toggleExec(uri: vscode.Uri): Promise<boolean> {
    const p = this.parse(uri);
    const l = await this.listing(p, true);
    const cur = l.files.find((f) => f.path === p.path);
    if (!cur) throw vscode.FileSystemError.FileNotFound(uri);
    const r = await this.call(p, uri, (api) => api.readFile(p.kit, p.path));
    const files = await this.call(p, uri, (api) => api.writeFile(p.kit, p.path, r.bytes, { sha256: r.sha256 }, !cur.exec));
    const now = files.files.find((f) => f.path === p.path);
    if (now) this.setSha(uri, now.sha256);
    this.store(p, files);
    return !cur.exec;
  }

  // rootChildren lists the Jam's root folder (its open kits' branches) or a
  // branch directory (its open kits); undefined below a kit.
  private rootChildren(uri: vscode.Uri): string[] | undefined {
    if (uri.scheme !== KIT_SCHEME) return undefined;
    const segs = uri.path.split("/").filter((x) => x !== "");
    if (segs.length >= 2) return undefined;
    const open = (this.o.openKits?.() ?? []).filter((k) => k.jam === uri.authority);
    const names = segs.length === 0 ? open.map((k) => k.branch.replaceAll("/", "~")) : open.filter((k) => k.branch.replaceAll("/", "~") === segs[0]).map((k) => k.kit);
    return [...new Set(names)].sort();
  }

  private isDir(p: KitPath, l: DraftFiles): boolean {
    return l.files.some((f) => f.path.startsWith(`${p.path}/`)) || (this.dirs.get(kitKey(p))?.has(p.path) ?? false);
  }

  private parse(uri: vscode.Uri): KitPath {
    const p = parseKitUri(uri);
    if (!p) throw vscode.FileSystemError.FileNotFound(uri);
    return p;
  }

  // call runs fn on ref's admin API, turning the Jam's refusals into file
  // system errors (uri: the file it is about).
  private async call<T>(ref: KitRef, uri: vscode.Uri | undefined, fn: (api: KitApi) => Promise<T>): Promise<T> {
    const api = await this.apiFor(ref.jam, ref.branch);
    if (!api) throw vscode.FileSystemError.Unavailable("Sign in to this Jam to edit its kits.");
    try {
      return await fn(api);
    } catch (e) {
      if (!(e instanceof ApiError)) throw vscode.FileSystemError.Unavailable(e instanceof Error ? e.message : String(e));
      if (e.code === "no_draft") {
        this.listings.delete(kitKey(ref));
        this.gone.fire({ jam: ref.jam, branch: ref.branch, kit: ref.kit });
        throw vscode.FileSystemError.Unavailable(`Kit ${ref.kit}: the draft is gone (discarded, or the Jam restarted). Your unsaved edits are still open: reopen the kit from the Kits view, then save again.`);
      }
      if (e.status === 412) {
        this.listings.delete(kitKey(ref));
        if (uri) this.changed.fire([{ type: vscode.FileChangeType.Changed, uri }]);
        throw vscode.FileSystemError.Unavailable(`${uri ? parseKitUri(uri)?.path : "The file"} changed on the Jam since you opened it. ${COMPARE_HINT}.`);
      }
      if (e.status === 404) throw vscode.FileSystemError.FileNotFound(uri);
      if (e.status === 409) throw vscode.FileSystemError.FileExists(e.message);
      throw vscode.FileSystemError.Unavailable(e.message);
    }
  }
}


