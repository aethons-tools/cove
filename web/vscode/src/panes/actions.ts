// Performs the actions that intentAction produced from a pane's open intents.
import type { IntentAction } from "../bridge/intents";

export type ActionDeps = {
  openExternal: (url: string) => unknown;
  executeCommand: (c: string, ...a: unknown[]) => Thenable<unknown>;
  asExternalUri: (url: string) => Promise<string>;
  openBoard: (id: string) => void;
  openSession: (id: string) => unknown;
  // goTo is a rail row's click: the session's window if open, else the conversation.
  goTo: (channel: string, session: string) => unknown;
  scrollConversation: (anchor: string) => void;
  error: (message: string) => void;
};

const message = (e: unknown): string => (e instanceof Error ? e.message : String(e));

export async function perform(a: IntentAction, d: ActionDeps): Promise<void> {
  try {
    if ("openExternal" in a) await d.openExternal(a.openExternal);
    else if ("openBoard" in a) d.openBoard(a.openBoard);
    else if ("openSession" in a) await d.openSession(a.openSession);
    else if ("goTo" in a) await d.goTo(a.goTo.channel, a.goTo.session);
    else if ("revealConversation" in a) {
      await d.executeCommand("jam.conversation.focus");
      if (a.revealConversation) d.scrollConversation(a.revealConversation);
    } else await forward(a, d);
  } catch (e) {
    d.error(message(e));
  }
}

// forward opens the window's own studio port through VS Code's tunnel; when
// that is unavailable (asExternalUri throws), the preview's web URL opens in
// the system browser. An unchanged localhost URI counts as forwarded (VS Code reuses a free local port).
async function forward(a: Extract<IntentAction, { forwardPort: number }>, d: ActionDeps): Promise<void> {
  const local = `http://localhost:${a.forwardPort}/`;
  let u: string;
  try {
    u = await d.asExternalUri(local);
  } catch (e) {
    d.error(`Couldn't forward port ${a.forwardPort}: ${message(e)}`);
    await d.openExternal(a.url);
    return;
  }
  if (a.inBrowser) await d.openExternal(u);
  else await d.executeCommand("simpleBrowser.show", u);
}
