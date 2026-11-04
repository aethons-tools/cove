// The Kits view (docs/usage/jam/vscode-kits.md): in the hub, an
// operator's config branch and its kits. A kit opens as a jamkit: folder of
// its draft; Publish commits the draft to the branch, Discard drops it.
import * as vscode from "vscode";
import type { KitRow } from "../../../app/src/admin/api/types.js" with { "resolution-mode": "import" };
import { type AdminApi, NoAdminError } from "../admin/api";
import type { BranchState } from "../admin/branch";
import type { Session } from "../auth";
import { jamSlug } from "../jams";
import type { MementoLike } from "./fs";
import { KIT_SCHEME, type KitRef, kitKey, kitUri, parseKitUri } from "./uri";

export type KitsApi = Pick<AdminApi, "head" | "branches" | "createBranch" | "kits" | "openDraft" | "files" | "publish" | "discard">;

export type KitsDeps = {
  jamUrl: () => string | undefined;
  session: (url: string) => Promise<Session | undefined>;
  branches: BranchState;
  makeApi: (url: string, token: string, branch: () => string) => KitsApi;
  openExternal: (url: string) => Promise<boolean>;
  // state keeps the kits open under the Jam's root folder.
  state?: MementoLike;
  // onOpenChanged: the open kits changed (the root folder's listing).
  onOpenChanged?: (jam: string) => void;
};

const OPEN_KEY = "jam.kits.open";

export type KitNode =
  | { kind: "signIn" }
  | { kind: "message"; text: string }
  | { kind: "branch"; name: string; exists: boolean }
  | { kind: "kit"; row: KitRow };

export class KitsController implements vscode.TreeDataProvider<KitNode> {
  private changed = new vscode.EventEmitter<KitNode | undefined>();
  readonly onDidChangeTreeData = this.changed.event;
  private rows = new Map<string, KitRow>();
  private gone = new Set<string>();
  private branchName = "";
  private head = "";
  private apiCache: { key: string; api: KitsApi } | undefined;

  constructor(private d: KitsDeps) {}


  refresh(): void {
    this.changed.fire(undefined);
  }

  // markGone flags a kit whose draft the Jam no longer has.
  markGone(ref: KitRef): void {
    this.gone.add(kitKey(ref));
    this.refresh();
  }

  async getChildren(n?: KitNode): Promise<KitNode[]> {
    if (n) return [];
    const api = await this.api();
    if (!api) return [{ kind: "signIn" }];
    try {
      const bs = await api.branches();
      const name = this.branchName || bs.defaultName;
      this.branchName = name;
      const b = bs.branches.find((x) => x.name === name);
      if (!b) return [{ kind: "branch", name, exists: false }];
      this.head = b.head;
      const kits = await api.kits();
      this.rows = new Map(kits.kits.map((k) => [k.name, k]));
      return [{ kind: "branch", name, exists: true }, ...[...kits.kits].sort((a, b) => a.name.localeCompare(b.name)).map((row) => ({ kind: "kit" as const, row }))];
    } catch (e) {
      if (e instanceof NoAdminError) return [{ kind: "signIn" }];
      return [{ kind: "message", text: e instanceof Error ? e.message : String(e) }];
    }
  }

  getTreeItem(n: KitNode): vscode.TreeItem {
    switch (n.kind) {
      case "signIn": {
        const t = new vscode.TreeItem("Kit editing needs an operator sign-in — sign in again");
        t.command = { command: "jam.signIn", title: "Sign In" };
        return t;
      }
      case "message":
        return new vscode.TreeItem(n.text);
      case "branch": {
        const t = new vscode.TreeItem(n.exists ? `Branch: ${n.name}` : `Create branch ${n.name}`);
        t.iconPath = new vscode.ThemeIcon("git-branch");
        t.command = n.exists ? { command: "jam.kits.switchBranch", title: "Switch Branch" } : { command: "jam.kits.createBranch", title: "Create Branch" };
        t.tooltip = n.exists ? "The config branch your kit edits go to: click to switch" : "Kits are edited on a branch of the config repo";
        return t;
      }
      case "kit": {
        const r = n.row;
        const t = new vscode.TreeItem(r.name);
        const ref = this.ref(r.name);
        if (r.tracked) {
          t.description = "from its git repo";
          t.tooltip = "This kit lives in its own git repo: edit it there.";
          t.contextValue = "jamKitTracked";
          return t;
        }
        t.description = ref && this.gone.has(kitKey(ref)) ? "draft gone — reopen" : r.draft ? "draft" : `v${r.current}`;
        t.contextValue = r.draft ? "jamKitDraft" : "jamKit";
        t.iconPath = new vscode.ThemeIcon("package");
        t.command = { command: "jam.kits.open", title: "Open Kit", arguments: [r.name] };
        return t;
      }
    }
  }

  async createBranch(): Promise<void> {
    const api = await this.need();
    const url = this.d.jamUrl()!;
    const r = await api.createBranch(this.branchName);
    await this.d.branches.set(url, r.branch.name);
    this.branchName = r.branch.name;
    this.refresh();
  }

  async switchBranch(): Promise<void> {
    const api = await this.need();
    const url = this.d.jamUrl()!;
    const bs = await api.branches();
    const create = "$(add) Create a branch…";
    const pick = await vscode.window.showQuickPick([...bs.branches.filter((b) => !b.followed).map((b) => b.name), create], {
      placeHolder: `Edit kits on which branch? (now ${this.branchName})`,
    });
    if (!pick) return;
    let name = pick;
    if (pick === create) {
      const v = await vscode.window.showInputBox({ prompt: "New branch name", value: bs.defaultName });
      if (!v) return;
      name = (await api.createBranch(v.trim())).branch.name;
    }
    await this.d.branches.set(url, name);
    this.branchName = name;
    this.refresh();
  }

  // openKits are the kits shown under the Jam's root folder.
  openKits(): KitRef[] {
    return this.d.state?.get<KitRef[]>(OPEN_KEY) ?? [];
  }

  private async setOpen(ref: KitRef, open: boolean): Promise<void> {
    const rest = this.openKits().filter((k) => kitKey(k) !== kitKey(ref));
    await this.d.state?.update(OPEN_KEY, open ? [...rest, ref] : rest);
    this.d.onOpenChanged?.(ref.jam);
  }

  // open opens a kit's draft (opening one from its current version when it
  // has none, or it is gone) under the Jam's root folder, which joins the
  // workspace once and stays: VS Code restarts every extension when the
  // first folder changes.
  async open(arg: unknown): Promise<void> {
    const ref = this.refOf(arg);
    const api = await this.apiOn(ref);
    const row = ref.branch === this.branchName ? this.rows.get(ref.kit) : undefined;
    if (row?.tracked) throw new Error(`Kit ${ref.kit} lives in its own git repo: edit it there.`);
    if (!row?.draft || this.gone.has(kitKey(ref))) {
      await api.openDraft(ref.kit, 0).catch((e: unknown) => {
        if (!(e instanceof Error && /already has a draft/.test(e.message))) throw e;
      });
      this.gone.delete(kitKey(ref));
      if (row) this.rows.set(ref.kit, { ...row, draft: true });
    }
    await this.setOpen(ref, true);
    const root = vscode.Uri.from({ scheme: KIT_SCHEME, authority: ref.jam, path: "/" });
    const folders = vscode.workspace.workspaceFolders ?? [];
    if (!folders.some((f) => String(f.uri) === String(root))) {
      vscode.workspace.updateWorkspaceFolders(folders.length, 0, { uri: root, name: `Jam kits (${ref.jam})` });
    }
    await vscode.commands.executeCommand("revealInExplorer", kitUri(ref.jam, ref.branch, ref.kit));
    this.refresh();
  }

  // saveDirty saves ref's unsaved editors first, if the operator agrees;
  // false when they don't.
  async saveDirty(ref: KitRef, verb: string): Promise<boolean> {
    const dirty = vscode.workspace.textDocuments.filter((d) => {
      const p = d.isDirty ? parseKitUri(d.uri) : undefined;
      return p !== undefined && kitKey(p) === kitKey(ref);
    });
    if (dirty.length === 0) return true;
    const a = await vscode.window.showWarningMessage(`Kit ${ref.kit} has unsaved files. Save them and ${verb.toLowerCase()}?`, { modal: true }, `Save and ${verb}`);
    if (a !== `Save and ${verb}`) return false;
    for (const d of dirty) if (!(await d.save())) throw new Error(`${parseKitUri(d.uri)?.path} could not be saved.`);
    return true;
  }

  async publish(arg: unknown): Promise<void> {
    const ref = this.refOf(arg);
    const api = await this.apiOn(ref);
    if (!(await this.saveDirty(ref, "Publish"))) return;
    const ok = await vscode.window.showWarningMessage(`Publish kit ${ref.kit}? Its draft is committed to branch ${ref.branch}.`, { modal: true }, "Publish");
    if (ok !== "Publish") return;
    const f = await api.files(ref.kit);
    const r = await api.publish(ref.kit, f.etag, api.head || this.head);
    await this.setOpen(ref, false);
    this.refresh();
    void vscode.window.showInformationMessage(r.message || `Published ${ref.kit}.`);
  }

  async discard(arg: unknown): Promise<void> {
    const ref = this.refOf(arg);
    const api = await this.apiOn(ref);
    const ok = await vscode.window.showWarningMessage(
      `Discard the draft of kit ${ref.kit} on branch ${ref.branch}? Its unpublished changes are lost.`,
      { modal: true },
      "Discard",
    );
    if (ok !== "Discard") return;
    const f = await api.files(ref.kit);
    await api.discard(ref.kit, f.etag);
    await this.setOpen(ref, false);
    this.refresh();
  }

  async openInConsole(arg?: unknown): Promise<void> {
    const url = this.d.jamUrl();
    if (!url) return;
    const ref = arg === undefined ? undefined : this.refOf(arg);
    const page = ref ? `config/kits/${encodeURIComponent(ref.kit)}` : "config";
    await this.d.openExternal(`${url}/app/admin/${page}?branch=${encodeURIComponent(ref?.branch ?? this.branchName)}`);
  }

  // kitOf is the kit a command was run on: a view item, a kit name, or a
  // jamkit: URI (the explorer).
  // With no argument (the command palette), the active editor's kit.
  kitOf(arg: unknown): string | undefined {
    if (arg === undefined) arg = vscode.window.activeTextEditor?.document.uri;
    if (typeof arg === "string") return arg;
    if (arg && typeof arg === "object" && "row" in arg) return (arg as { row: KitRow }).row.name;
    if (arg instanceof Object && "scheme" in arg) return parseKitUri(arg as vscode.Uri)?.kit;
    return undefined;
  }

  // refOf is the kit a command was run on, on its own branch: a jamkit: URI
  // names its branch; a view item or a name is on the view's.
  refOf(arg: unknown): KitRef {
    if (arg === undefined) arg = vscode.window.activeTextEditor?.document.uri;
    if (arg instanceof Object && "scheme" in arg) {
      const p = parseKitUri(arg as vscode.Uri);
      if (p) return { jam: p.jam, branch: p.branch, kit: p.kit };
    }
    const kit = this.kitOf(arg);
    const ref = kit === undefined ? undefined : this.ref(kit);
    if (!ref) throw new Error("Pick a kit in the Kits view.");
    return ref;
  }

  private ref(kit: string): KitRef | undefined {
    const url = this.d.jamUrl();
    return url ? { jam: jamSlug(url), branch: this.branchName, kit } : undefined;
  }

  private async need(): Promise<KitsApi> {
    const api = await this.api();
    if (!api) throw new NoAdminError();
    return api;
  }

  // apiOn is the admin client on ref's branch (ref must be this hub's Jam).
  private async apiOn(ref: KitRef): Promise<KitsApi> {
    const api = await this.need();
    const url = this.d.jamUrl()!;
    if (jamSlug(url) !== ref.jam) throw new Error(`Kit ${ref.kit} belongs to another Jam: use its hub.`);
    if (ref.branch === this.branchName) return api;
    const s = await this.d.session(url);
    return this.d.makeApi(url, s!.token, () => ref.branch);
  }

  // api is the admin client for this window's Jam, on the chosen branch;
  // undefined without a sign-in that may use the admin API.
  private async api(): Promise<KitsApi | undefined> {
    const url = this.d.jamUrl();
    if (!url) return undefined;
    const s = await this.d.session(url);
    if (!s || s.admin !== true) return undefined;
    if (!this.branchName) this.branchName = this.d.branches.get(url);
    const key = `${url}\n${s.token}`;
    if (this.apiCache?.key !== key) this.apiCache = { key, api: this.d.makeApi(url, s.token, () => this.branchName) };
    return this.apiCache.api;
  }
}
