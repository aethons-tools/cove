// A small stand-in for the `vscode` module (vitest aliases "vscode" here):
// each API the extension touches is a vi.fn whose calls tests inspect.
// mock.reset() clears the calls and the fake state between tests.
import { vi } from "vitest";
import type * as vs from "vscode";

const disposable = () => ({ dispose: () => {} });

type Answer = string | undefined;

// mock is the fake state the stand-in reads and records.
export const mock = {
  config: {} as Record<string, unknown>, // "<section>.<key>" → value (global)
  answers: [] as Answer[], // queued replies to show*Message, in order
  pick: undefined as unknown, // the next showQuickPick reply
  commands: new Map<string, (...a: unknown[]) => unknown>(),
  uriHandler: undefined as undefined | { handleUri(u: unknown): unknown },
  viewProviders: new Map<string, vs.WebviewViewProvider>(),
  context: new Map<string, unknown>(), // setContext keys
  windowState: new Set<(s: { focused: boolean }) => void>(),
  reset() {
    mock.config = {};
    mock.answers = [];
    mock.pick = undefined;
    mock.commands.clear();
    mock.uriHandler = undefined;
    mock.viewProviders.clear();
    mock.context.clear();
    mock.windowState.clear();
    window.state.focused = true;
    workspace.workspaceFolders = undefined;
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

export enum ConfigurationTarget { Global = 1, Workspace = 2, WorkspaceFolder = 3 }
export enum ProgressLocation { SourceControl = 1, Window = 10, Notification = 15 }

export const workspace = {
  workspaceFolders: undefined as undefined | { uri: { scheme: string; authority: string } }[],
  getConfiguration: vi.fn((section: string) => ({
    get: (key: string) => mock.config[`${section}.${key}`],
    inspect: (key: string) => ({ key: `${section}.${key}`, globalValue: mock.config[`${section}.${key}`] }),
    update: vi.fn(async (key: string, v: unknown) => { mock.config[`${section}.${key}`] = v; }),
  })),
  onDidChangeConfiguration: vi.fn(() => disposable()),
};

export const extensions = { getExtension: vi.fn((_id: string) => undefined as unknown) };

type FakeUri = { scheme: string; authority: string; path: string; query: string; toString(): string };

// Uri.parse keeps the parts the extension reads.
export const Uri = {
  parse: (s: string): FakeUri => {
    const u = new URL(s);
    return { scheme: u.protocol.slice(0, -1), authority: u.host, path: u.pathname, query: u.search.slice(1), toString: () => s };
  },
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

const fns = [
  window.showInformationMessage, window.showWarningMessage, window.showErrorMessage, window.showQuickPick,
  window.withProgress, window.registerUriHandler, window.registerWebviewViewProvider, window.onDidChangeWindowState,
  commands.registerCommand, commands.executeCommand,
  env.openExternal, env.asExternalUri, workspace.getConfiguration, workspace.onDidChangeConfiguration, extensions.getExtension,
];

// fakeContext is an ExtensionContext with in-memory secrets and globalState.
export function fakeContext(): vs.ExtensionContext & { secretValues: Map<string, string>; state: Map<string, unknown> } {
  const secretValues = new Map<string, string>();
  const state = new Map<string, unknown>();
  const workspaceValues = new Map<string, unknown>();
  const ctx = {
    subscriptions: [] as { dispose(): unknown }[],
    extensionUri: Uri.parse("file:///ext"),
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
      update: async (k: string, v: unknown) => { state.set(k, v); },
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
  return ctx as unknown as vs.ExtensionContext & { secretValues: Map<string, string>; state: Map<string, unknown> };
}
