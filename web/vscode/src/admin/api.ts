// The Jam admin API client (docs/usage/jam/app-tokens.md#bearer-scope): an
// operator app token with admin access, on one config branch. Kit drafts'
// files (docs/usage/jam/kits-drafts.md#file-api), their builds and the
// branch list.
import type {
  ConfigBranches,
  ConfigBranchResult,
  DraftFiles,
  DraftResult,
  KitsResponse,
} from "../../../app/src/admin/api/types.js" with { "resolution-mode": "import" };
import { ApiError, SignedOutError } from "../api";

// NoAdminError: the token may not use the admin API (a member, or an
// operator token from before admin access): sign in again.
export class NoAdminError extends Error {
  constructor() {
    super("This sign-in can't edit config: sign in to Jam again as an operator.");
    this.name = "NoAdminError";
  }
}

// KitDraftBuild is jam.KitDraftBuild: a draft's latest test build.
export type KitDraftBuild = {
  id?: string;
  kit: string;
  manager: string;
  branch?: string;
  state: "running" | "succeeded" | "failed" | "unknown";
  tag?: string;
  digest?: string;
  error?: string;
  current: boolean;
  log: string;
  log_from: number;
  log_end: number;
  log_skipped?: boolean;
};

export type WriteCond = { sha256: string } | "create";

const seg = encodeURIComponent;
const filePath = (p: string) => p.split("/").map(seg).join("/");

export class AdminApi {
  constructor(
    private base: string,
    private token: string,
    private doFetch: typeof globalThis.fetch,
    private branch: () => string,
  ) {}

  kits(): Promise<KitsResponse> {
    return this.json("GET", "kits");
  }
  branches(): Promise<ConfigBranches> {
    return this.json("GET", "config/branches", undefined, { live: true });
  }
  createBranch(name: string): Promise<ConfigBranchResult> {
    return this.json("POST", "config/branches", { name }, { live: true });
  }
  openDraft(kit: string, from: number): Promise<DraftResult> {
    return this.json("POST", `kits/${seg(kit)}/draft`, { from });
  }
  files(kit: string): Promise<DraftFiles> {
    return this.json("GET", `kits/${seg(kit)}/draft/files`);
  }
  async readFile(kit: string, path: string): Promise<{ bytes: Uint8Array; sha256: string }> {
    const res = await this.send("GET", `kits/${seg(kit)}/draft/files/${filePath(path)}`);
    return { bytes: new Uint8Array(await res.arrayBuffer()), sha256: (res.headers.get("ETag") ?? "").replace(/"/g, "") };
  }
  writeFile(kit: string, path: string, bytes: Uint8Array, cond: WriteCond, exec?: boolean): Promise<DraftFiles> {
    const q = exec === undefined ? "" : `?exec=${exec ? 1 : 0}`;
    const h: Record<string, string> = { "Content-Type": "application/octet-stream" };
    if (cond === "create") h["If-None-Match"] = "*";
    else h["If-Match"] = `"${cond.sha256}"`;
    return this.json("PUT", `kits/${seg(kit)}/draft/files/${filePath(path)}${q}`, bytes, { headers: h });
  }
  deleteFile(kit: string, path: string, sha256: string): Promise<DraftFiles> {
    return this.json("DELETE", `kits/${seg(kit)}/draft/files/${filePath(path)}`, undefined, { headers: { "If-Match": `"${sha256}"` } });
  }
  renameFile(kit: string, from: string, to: string, sha256: string): Promise<DraftFiles> {
    return this.json("POST", `kits/${seg(kit)}/draft/files-rename`, { from, to, sha256 });
  }
  publish(kit: string, etag: string, head: string): Promise<DraftResult> {
    return this.json("POST", `kits/${seg(kit)}/draft/publish`, { etag }, { headers: { "X-Jam-Config-Head": head } });
  }
  discard(kit: string, etag: string): Promise<DraftResult> {
    return this.json("POST", `kits/${seg(kit)}/draft/discard`, { etag });
  }
  startBuild(kit: string, etag: string): Promise<KitDraftBuild> {
    return this.json("POST", `kits/${seg(kit)}/draft/build`, { etag });
  }
  buildStatus(kit: string, since: number): Promise<KitDraftBuild> {
    return this.json("GET", `kits/${seg(kit)}/draft/build?since=${since}`);
  }
  cancelBuild(kit: string): Promise<KitDraftBuild> {
    return this.json("POST", `kits/${seg(kit)}/draft/build/cancel`);
  }
  // head is the branch's head after the last call (X-Jam-Config-Head), "" before any.
  head = "";

  private async json<T>(method: string, path: string, body?: unknown, o: { headers?: Record<string, string>; live?: boolean } = {}): Promise<T> {
    const res = await this.send(method, path, body, o);
    return (await res.json()) as T;
  }

  private async send(method: string, path: string, body?: unknown, o: { headers?: Record<string, string>; live?: boolean } = {}): Promise<Response> {
    const headers: Record<string, string> = { Authorization: `Bearer ${this.token}`, Accept: "application/json", ...o.headers };
    const branch = this.branch();
    if (!o.live && branch) headers["X-Jam-Config-Branch"] = branch;
    let payload: RequestInit["body"];
    if (body instanceof Uint8Array) payload = body as unknown as RequestInit["body"];
    else if (body !== undefined) {
      headers["Content-Type"] = "application/json";
      payload = JSON.stringify(body);
    }
    const res = await this.doFetch(`${this.base}/app/admin/api/${path}`, { method, headers, redirect: "error", body: payload });
    const head = res.headers.get("X-Jam-Config-Head");
    if (head) this.head = head;
    if (res.status === 401) throw new SignedOutError();
    if (!res.ok) {
      const text = (await res.text()).trim();
      let msg = text;
      let code = "";
      try {
        const j = JSON.parse(text) as { error?: unknown; code?: unknown };
        if (typeof j.error === "string") msg = j.error;
        if (typeof j.code === "string") code = j.code;
      } catch {
        // a text body is the message
      }
      if (res.status === 403 && code === "no_admin") throw new NoAdminError();
      throw new ApiError(msg || `HTTP ${res.status}`, res.status, code);
    }
    return res;
  }
}
