// A small stand-in for the `vscode` module (vitest aliases "vscode" here):
// each API the extension touches is a vi.fn whose calls tests inspect.
// mock.reset() clears the calls and the fake state between tests.
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { vi } from "vitest";
import type * as vs from "vscode";

const disposable = () => ({ dispose: () => {} });

type Answer = string | undefined;

// mock is the fake state the stand-in reads and records.
export const mock = {
  config: {} as Record<string, unknown>, // "<section>.<key>" → value (global)
  answers: [] as Answer[], // queued replies to show*Message, in order
  pick: undefined as unknown, // the next showQuickPick reply
  input: undefined as string | undefined, // the next showInputBox reply
  commands: new Map<string, (...a: unknown[]) => unknown>(),
  uriHandler: undefined as undefined | { handleUri(u: unknown): unknown },
  viewProviders: new Map<string, vs.WebviewViewProvider>(),
  context: new Map<string, unknown>(), // setContext keys
  windowState: new Set<(s: { focused: boolean }) => void>(),
  panels: [] as ReturnType<typeof fakeWebviewPanel>[], // createWebviewPanel's results
  panelSerializers: new Map<string, { deserializeWebviewPanel(p: unknown, s: unknown): unknown }>(),
  configListeners: new Set<(e: { affectsConfiguration(s: string): boolean }) => void>(),
  output: [] as string[], // lines appended to any output channel
  contexts: [] as { subscriptions: { dispose(): unknown }[] }[], // fakeContexts since the last reset
  // changeConfig fires onDidChangeConfiguration for the given keys.
  changeConfig(...keys: string[]) {
    const e = { affectsConfiguration: (s: string) => keys.some((k) => k === s || k.startsWith(`${s}.`)) };
    for (const cb of [...mock.configListeners]) cb(e);
  },
  reset() {
    // The previous test's extension stops: its permanent stream no longer runs.
    for (const c of mock.contexts.splice(0)) for (const d of c.subscriptions.splice(0)) d.dispose();
    mock.configListeners.clear();
    mock.output = [];
    mock.panels = [];
    mock.panelSerializers.clear();
    mock.config = {};
    mock.answers = [];
    mock.pick = undefined;
    mock.input = undefined;
    mock.commands.clear();
    mock.uriHandler = undefined;
    mock.viewProviders.clear();
    mock.context.clear();
    mock.windowState.clear();
    window.treeViews.length = 0;
    window.state.focused = true;
    workspace.workspaceFolders = undefined;
    workspace.workspaceFile = undefined;
    env.uriScheme = "vscode";
    for (const f of fns) f.mockClear();
  },
};

const message = () => vi.fn(async (..._a: unknown[]) => mock.answers.shift());

export const window = {
  showInformationMessage: message(),
  showWarningMessage: message(),
  showErrorMessage: message(),
  showQuickPick: vi.fn(async (..._a: unknown[]) => mock.pick),
  showInputBox: vi.fn(async (..._a: unknown[]) => mock.input),
  withProgress: vi.fn(async (_o: unknown, task: (p: unknown, t: unknown) => Promise<unknown>) =>
    task({ report: () => {} }, { isCancellationRequested: false, onCancellationRequested: () => disposable() })),
  registerUriHandler: vi.fn((h: { handleUri(u: unknown): unknown }) => {
    mock.uriHandler = h;
    return disposable();
  }),
  registerWebviewViewProvider: vi.fn((id: string, p: vs.WebviewViewProvider) => {
    mock.viewProviders.set(id, p);
    return disposable();
  }),
  createWebviewPanel: vi.fn((viewType: string, title: string, column: unknown, options: unknown) => {
    const p = fakeWebviewPanel(viewType, title, column, options);
    mock.panels.push(p);
    return p;
  }),
  registerWebviewPanelSerializer: vi.fn((id: string, s: { deserializeWebviewPanel(p: unknown, s: unknown): unknown }) => {
    mock.panelSerializers.set(id, s);
    return disposable();
  }),
  createOutputChannel: vi.fn((name: string) => ({
    name,
    appendLine: (l: string) => void mock.output.push(l),
    append: (l: string) => void mock.output.push(l),
    show: () => {},
    clear: () => {},
    dispose: () => {},
  })),
  treeViews: [] as { id: string; badge: unknown; options: { treeDataProvider: unknown } }[],
  createTreeView: vi.fn((id: string, options: { treeDataProvider: unknown }) => {
    const v = { id, options, badge: undefined as unknown, dispose: () => {} };
    window.treeViews.push(v);
    return v;
  }),
  createStatusBarItem: vi.fn((_align?: unknown, _priority?: number) => {
    const item = {
      text: "",
      command: undefined as unknown,
      tooltip: undefined as unknown,
      shown: false,
      show: () => void (item.shown = true),
      hide: () => void (item.shown = false),
      dispose: () => void (item.shown = false),
    };
    return item;
  }),
  state: { focused: true },
  onDidChangeWindowState: vi.fn((cb: (s: { focused: boolean }) => void) => {
    mock.windowState.add(cb);
    return { dispose: () => void mock.windowState.delete(cb) };
  }),
};

export const commands = {
  registerCommand: vi.fn((id: string, cb: (...a: unknown[]) => unknown) => {
    mock.commands.set(id, cb);
    return disposable();
  }),
  executeCommand: vi.fn(async (...a: unknown[]) => {
    if (a[0] === "setContext") mock.context.set(a[1] as string, a[2]);
    return undefined;
  }),
};

export const env = {
  uriScheme: "vscode",
  openExternal: vi.fn(async (_u: unknown) => true),
  asExternalUri: vi.fn(async (u: unknown) => u),
};

export class ThemeIcon {
  constructor(public id: string) {}
}
export enum TreeItemCollapsibleState { None = 0, Collapsed = 1, Expanded = 2 }
export class TreeItem {
  description?: string;
  tooltip?: string;
  iconPath?: unknown;
  command?: { command: string; title: string; arguments?: unknown[] };
  constructor(public label: string, public collapsibleState?: number) {}
}
export class EventEmitter<T> {
  private ls = new Set<(e: T) => void>();
  event = (cb: (e: T) => void) => {
    this.ls.add(cb);
    return { dispose: () => void this.ls.delete(cb) };
  };
  fire(e: T): void {
    for (const cb of [...this.ls]) cb(e);
  }
}

export enum ConfigurationTarget { Global = 1, Workspace = 2, WorkspaceFolder = 3 }
export enum ViewColumn { Active = -1, Beside = -2, One = 1, Two = 2 }
export enum StatusBarAlignment { Left = 1, Right = 2 }
export enum ProgressLocation { SourceControl = 1, Window = 10, Notification = 15 }

export const workspace = {
  workspaceFolders: undefined as undefined | { uri: { scheme: string; authority: string } }[],
  workspaceFile: undefined as undefined | { scheme: string; authority: string; path: string },
  getConfiguration: vi.fn((section: string) => ({
    get: (key: string) => mock.config[`${section}.${key}`],
    inspect: (key: string) => ({ key: `${section}.${key}`, globalValue: mock.config[`${section}.${key}`] }),
    update: vi.fn(async (key: string, v: unknown) => { mock.config[`${section}.${key}`] = v; }),
  })),
  onDidChangeConfiguration: vi.fn((cb: (e: { affectsConfiguration(s: string): boolean }) => void) => {
    mock.configListeners.add(cb);
    return { dispose: () => void mock.configListeners.delete(cb) };
  }),
};

export const extensions = { getExtension: vi.fn((_id: string) => undefined as unknown) };

type FakeUri = { scheme: string; authority: string; path: string; query: string; fsPath: string; toString(): string };

// Uri.parse keeps the parts the extension reads.
export const Uri = {
  parse: (s: string): FakeUri => {
    const u = new URL(s);
    const path = decodeURIComponent(u.pathname);
    return { scheme: u.protocol.slice(0, -1), authority: u.host, path: u.pathname, query: u.search.slice(1), fsPath: path, toString: () => s };
  },
  file: (fsPath: string): FakeUri => Uri.parse(`file://${fsPath.split("/").map(encodeURIComponent).join("/")}`),
  joinPath: (base: FakeUri, ...segs: string[]): FakeUri => {
    const path = [base.path.replace(/\/+$/, ""), ...segs].join("/");
    return Uri.parse(`${base.scheme}://${base.authority}${path}`);
  },
};

// fakeWebviewView is a WebviewView whose webview records what it is sent;
// receive, setVisible and dispose drive it like VS Code would.
export function fakeWebviewView() {
  const onMessage = new Set<(m: unknown) => void>();
  const onVisible = new Set<() => void>();
  const onDispose = new Set<() => void>();
  const sub = <T>(set: Set<T>) => (cb: T) => {
    set.add(cb);
    return { dispose: () => void set.delete(cb) };
  };
  const posted: unknown[] = [];
  const view = {
    viewType: "jam.conversation",
    visible: true,
    description: undefined as string | undefined,
    title: "Conversation",
    webview: {
      options: {} as vs.WebviewOptions,
      html: "",
      cspSource: "https://x.vscode-cdn.net",
      postMessage: vi.fn(async (m: unknown) => {
        posted.push(m);
        return true;
      }),
      onDidReceiveMessage: sub(onMessage),
      asWebviewUri: (u: FakeUri) => Uri.parse(`https://x.vscode-cdn.net/${u.scheme}${u.path}`),
    },
    onDidChangeVisibility: sub(onVisible),
    onDidDispose: sub(onDispose),
    show: () => {},
    posted,
    receive: (m: unknown) => [...onMessage].forEach((cb) => cb(m)),
    setVisible: (v: boolean) => {
      view.visible = v;
      [...onVisible].forEach((cb) => cb());
    },
    dispose: () => [...onDispose].forEach((cb) => cb()),
    listeners: () => onMessage.size + onVisible.size + onDispose.size,
  };
  return view;
}

// fakeWebviewPanel is a WebviewPanel over fakeWebviewView's webview.
export function fakeWebviewPanel(viewType: string, title: string, column: unknown, options: unknown) {
  const v = fakeWebviewView();
  const onViewState = new Set<() => void>();
  const panel = Object.assign(v, {
    active: false,
    onDidChangeViewState: (cb: () => void) => {
      onViewState.add(cb);
      return { dispose: () => void onViewState.delete(cb) };
    },
    // setActive makes the panel the active (and visible) editor, as a tab switch does.
    setActive: (a: boolean) => {
      panel.active = a;
      if (a) panel.visible = true;
      [...onViewState].forEach((cb) => cb());
    },
    viewType,
    title,
    column,
    createOptions: options,
    iconPath: undefined as unknown,
    revealed: [] as unknown[],
    reveal: vi.fn((col?: unknown) => void panel.revealed.push(col)),
  });
  return panel;
}

const fns = [
  window.showInformationMessage, window.showWarningMessage, window.showErrorMessage, window.showQuickPick, window.showInputBox,
  window.withProgress, window.createOutputChannel, window.registerUriHandler, window.createWebviewPanel, window.registerWebviewPanelSerializer, window.registerWebviewViewProvider, window.onDidChangeWindowState, window.createTreeView, window.createStatusBarItem,
  commands.registerCommand, commands.executeCommand,
  env.openExternal, env.asExternalUri, workspace.getConfiguration, workspace.onDidChangeConfiguration, extensions.getExtension,
];

// fakeContext is an ExtensionContext with in-memory secrets and globalState,
// and global storage in a temp dir (or the given one, shared like two windows').
export function fakeContext(globalStorage?: string): vs.ExtensionContext & { secretValues: Map<string, string>; state: Map<string, unknown> } {
  const storage = globalStorage ?? mkdtempSync(join(tmpdir(), "jamstore-"));
  const secretValues = new Map<string, string>();
  const state = new Map<string, unknown>();
  const workspaceValues = new Map<string, unknown>();
  const ctx = {
    subscriptions: [] as { dispose(): unknown }[],
    extensionUri: Uri.parse("file:///ext"),
    extensionPath: "/ext",
    globalStorageUri: Uri.parse(`file://${storage}`),
    secretValues,
    state,
    secrets: {
      get: async (k: string) => secretValues.get(k),
      store: async (k: string, v: string) => { secretValues.set(k, v); },
      delete: async (k: string) => { secretValues.delete(k); },
      onDidChange: () => disposable(),
    },
    globalState: {
      get: (k: string) => state.get(k),
      update: async (k: string, v: unknown) => {
        if (v === undefined) state.delete(k);
        else state.set(k, v);
      },
      keys: () => [...state.keys()],
    },
    workspaceState: {
      get: (k: string) => workspaceValues.get(k),
      update: async (k: string, v: unknown) => {
        if (v === undefined) workspaceValues.delete(k);
        else workspaceValues.set(k, v);
      },
      keys: () => [...workspaceValues.keys()],
    },
  };
  mock.contexts.push(ctx);
  return ctx as unknown as vs.ExtensionContext & { secretValues: Map<string, string>; state: Map<string, unknown> };
}
