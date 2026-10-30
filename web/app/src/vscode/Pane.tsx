import { useCallback, useEffect, useMemo, useState, type JSX, type ReactNode } from "react";
import { Button, Loader, MantineProvider, Stack, Text } from "@mantine/core";
import { Notifications } from "@mantine/notifications";
import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { createMemoryRouter, RouterProvider } from "react-router";
import { apiGet, ApiError, shouldRetry } from "../api/client";
import type { Bootstrap } from "../api/types";
import type { PaneName, PaneState, SessionPaneName } from "../bridge/protocol";
import { BootContext } from "../BootContext";
import { HostContext } from "../host";
import { useLiveEvents } from "../live/useLiveEvents";
import { APP } from "../paths";
import { Problem } from "../Problem";
import { theme } from "../theme";
import { BoardTab } from "./BoardTab";
import type { Bridge } from "./bridge";
import { interceptLinks, linkAction, type LinkAction } from "./links";
import { NavBridge } from "./NavBridge";
import { SessionBody, SessionGate } from "./panes";
import { useVscodeScheme } from "./scheme";
import { RailPane } from "./RailPane";

// NOUN is what each session pane shows, in its copy.
export const NOUN: Record<SessionPaneName, string> = {
  conversation: "conversation", thinking: "thinking", previews: "previews", status: "status", artifacts: "artifacts",
};

// copyFor is a pane's copy for signed out, no session and a session gone.
export function copyFor(name: PaneName): { signedOut: string; none: string; gone: string } {
  const noun = name === "board" ? "whiteboard" : name === "rail" ? "conversations" : NOUN[name];
  return {
    signedOut: name === "board" ? "Sign in to Jam to see this whiteboard."
      : name === "rail" ? "Sign in to Jam to see your conversations."
      : name === "conversation" ? "Sign in to Jam to see this conversation."
      : `Sign in to Jam to see this session's ${noun}.`,
    none: "This window isn't a Jam studio window.",
    gone: "That session has ended, or you can't see it.",
  };
}

// paneLinkAction is a link's action in the pane named name: only
// Conversation shows asks, so elsewhere a scroll to one reveals it there.
export function paneLinkAction(act: LinkAction, name: PaneName): LinkAction {
  return name !== "conversation" && "scrollTo" in act ? { intent: { kind: "reveal", anchor: act.scrollTo } } : act;
}

export const SCROLL_RETRY_MS = 200;
export const SCROLL_TRIES = 10;

// scrollToAnchor scrolls the element with id anchor into view, retrying
// while it isn't there yet (the conversation may still be loading).
export function scrollToAnchor(doc: Document, anchor: string, tries = SCROLL_TRIES): void {
  const el = doc.getElementById(anchor);
  if (el) el.scrollIntoView({ behavior: "smooth", block: "center" });
  else if (tries > 0) setTimeout(() => scrollToAnchor(doc, anchor, tries - 1), SCROLL_RETRY_MS);
}

// wirePane is main.tsx's document wiring for the pane named name: link
// clicks become intents (or scrolls), and the host's scrollTo scrolls. It
// returns the uninstall.
export function wirePane(b: Bridge, doc: Document, name: PaneName): () => void {
  let channel = "";
  const offState = b.onState((s) => { channel = s.target && "channel" in s.target ? s.target.channel : ""; });
  const offLinks = interceptLinks(doc, () => channel, (intent) => b.post({ type: "open", intent }), (act) => paneLinkAction(act, name));
  const offScroll = b.onScrollTo((anchor) => scrollToAnchor(doc, anchor));
  return () => { offState(); offLinks(); offScroll(); };
}

// Pane is one Jam view (or a board tab): the focused session's pane named
// name, or why there is none. gen counts sign-ins, so each starts with
// fresh queries.
export function Pane({ bridge, name, board }: { bridge: Bridge; name: PaneName; board?: string }): JSX.Element {
  const [view, setView] = useState<{ s: PaneState; gen: number }>();
  useEffect(() => bridge.onState((s) => setView((v) => ({ s, gen: (v?.gen ?? 0) + (s.signedIn && !v?.s.signedIn ? 1 : 0) }))), [bridge]);
  const scheme = useVscodeScheme();
  const signIn = useCallback(() => bridge.post({ type: "signIn" }), [bridge]);
  const retry = () => bridge.post({ type: "ready", pane: name });
  const copy = copyFor(name);
  let body: ReactNode = null;
  if (!view) body = null;
  else if (!view.s.signedIn) body = <Copy text={copy.signedOut} action="Sign in" onClick={signIn} />;
  else if (name === "rail") {
    const active = view.s.target && "channel" in view.s.target ? view.s.target.channel : undefined;
    body = <Shell key={view.gen} signIn={signIn}><RailPane bridge={bridge} windows={view.s.windows ?? []} active={active} /></Shell>;
  } else if (name === "board") {
    body = <Shell key={view.gen} signIn={signIn}><BoardTab bridge={bridge} id={board ?? ""} /></Shell>;
  } else if (view.s.error !== undefined) body = <Copy text={`Couldn't load the focused session: ${view.s.error}`} action="Try again" onClick={retry} />;
  else if (!view.s.target) body = <Copy text={copy.none} />;
  else if ("gone" in view.s.target) body = <Copy text={copy.gone} />;
  else {
    const { session, channel } = view.s.target;
    const boot = <Boot bridge={bridge} name={name} session={session} channel={channel} />;
    body = (
      <Shell key={`${view.gen}:${session}:${channel}`} signIn={signIn}>
        {name === "conversation" ? boot : <SessionGate session={session} name={name}>{boot}</SessionGate>}
      </Shell>
    );
  }
  return (
    <MantineProvider forceColorScheme={scheme} theme={theme}>
      <Notifications />
      {body}
    </MantineProvider>
  );
}

export function Copy({ text, action, onClick }: { text: string; action?: string; onClick?: () => void }) {
  return (
    <Stack align="flex-start" gap="sm" p="md">
      <Text size="sm">{text}</Text>
      {action && <Button size="xs" onClick={onClick}>{action}</Button>}
    </Stack>
  );
}

// Shell is one sign-in's (and target's) queries, host and live events.
function Shell({ signIn, children }: { signIn: () => void; children: ReactNode }) {
  const [client] = useState(() => new QueryClient({ defaultOptions: { queries: { staleTime: 10_000, retry: shouldRetry } } }));
  const host = useMemo(() => ({ signIn }), [signIn]);
  return (
    <QueryClientProvider client={client}>
      <HostContext.Provider value={host}>
        <Live />
        {children}
      </HostContext.Provider>
    </QueryClientProvider>
  );
}

function Live() {
  useLiveEvents();
  return null;
}

// Boot is a session pane once the bootstrap is in: its body in a memory
// router kept on the session's home channel.
function Boot({ bridge, name, session, channel }: { bridge: Bridge; name: SessionPaneName; session: string; channel: string }) {
  const home = `${APP}c/${encodeURIComponent(channel)}`;
  const boot = useQuery<Bootstrap, ApiError>({ queryKey: ["bootstrap"], queryFn: () => apiGet<Bootstrap>("bootstrap"), staleTime: Infinity });
  const onLeave = useCallback((path: string) => {
    const act = paneLinkAction(linkAction(path, channel), name);
    if ("intent" in act) bridge.post({ type: "open", intent: act.intent });
    else if ("scrollTo" in act) scrollToAnchor(document, act.scrollTo);
  }, [bridge, channel, name]);
  const router = useMemo(() => createMemoryRouter([{
    path: "*",
    element: <><NavBridge home={home} onLeave={onLeave} /><SessionBody bridge={bridge} name={name} session={session} channel={channel} /></>,
  }], { initialEntries: [home] }), [home, onLeave, bridge, name, session, channel]);
  if (boot.isPending) return <Loader m="md" size="sm" />;
  if (!boot.data) return <Problem error={boot.error!} here={home} />;
  return (
    <BootContext.Provider value={boot.data}>
      <RouterProvider router={router} />
    </BootContext.Provider>
  );
}
