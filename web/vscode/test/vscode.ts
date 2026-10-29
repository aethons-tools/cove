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
  reset() {
    mock.config = {};
    mock.answers = [];
    mock.pick = undefined;
    mock.commands.clear();
    mock.uriHandler = undefined;
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
};

export const commands = {
  registerCommand: vi.fn((id: string, cb: (...a: unknown[]) => unknown) => {
    mock.commands.set(id, cb);
    return disposable();
  }),
  executeCommand: vi.fn(async (..._a: unknown[]) => undefined),
};

export const env = {
  uriScheme: "vscode",
  openExternal: vi.fn(async (_u: unknown) => true),
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

// Uri.parse keeps the parts the extension reads.
export const Uri = {
  parse: (s: string) => {
    const u = new URL(s);
    return { scheme: u.protocol.slice(0, -1), authority: u.host, path: u.pathname, query: u.search.slice(1), toString: () => s };
  },
};

const fns = [
  window.showInformationMessage, window.showWarningMessage, window.showErrorMessage, window.showQuickPick,
  window.withProgress, window.registerUriHandler, commands.registerCommand, commands.executeCommand,
  env.openExternal, workspace.getConfiguration, workspace.onDidChangeConfiguration, extensions.getExtension,
];

// fakeContext is an ExtensionContext with in-memory secrets and globalState.
export function fakeContext(): vs.ExtensionContext & { secretValues: Map<string, string>; state: Map<string, unknown> } {
  const secretValues = new Map<string, string>();
  const state = new Map<string, unknown>();
  const ctx = {
    subscriptions: [] as { dispose(): unknown }[],
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
  };
  return ctx as unknown as vs.ExtensionContext & { secretValues: Map<string, string>; state: Map<string, unknown> };
}
