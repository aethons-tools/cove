import { useCallback, useEffect, useMemo, useState, type JSX } from "react";
import { Button, Loader, MantineProvider, Stack, Text } from "@mantine/core";
import { Notifications } from "@mantine/notifications";
import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { createMemoryRouter, RouterProvider } from "react-router";
import { apiGet, ApiError, shouldRetry } from "../api/client";
import type { Bootstrap } from "../api/types";
import type { PaneState } from "../bridge/protocol";
import { BootContext } from "../BootContext";
import { ConversationView } from "../conversation/ConversationView";
import { HostContext } from "../host";
import { useLiveEvents } from "../live/useLiveEvents";
import { APP } from "../paths";
import { Problem } from "../Problem";
import { theme } from "../theme";
import type { Bridge } from "./bridge";
import { linkAction } from "./links";
import { NavBridge } from "./NavBridge";
import { useVscodeScheme } from "./scheme";

// Pane is the Conversation pane: the focused session's conversation, or why
// there is none. gen counts sign-ins, so each starts with fresh queries.
export function Pane({ bridge }: { bridge: Bridge }): JSX.Element {
  const [view, setView] = useState<{ s: PaneState; gen: number }>();
  useEffect(() => bridge.onState((s) => setView((v) => ({ s, gen: (v?.gen ?? 0) + (s.signedIn && !v?.s.signedIn ? 1 : 0) }))), [bridge]);
  const scheme = useVscodeScheme();
  const signIn = useCallback(() => bridge.post({ type: "signIn" }), [bridge]);
  const focus = () => bridge.post({ type: "open", intent: { kind: "focusSession" } });
  const retry = () => bridge.post({ type: "ready", pane: "conversation" });
  return (
    <MantineProvider forceColorScheme={scheme} theme={theme}>
      <Notifications />
      {!view ? null
        : !view.s.signedIn ? <Copy text="Sign in to Jam to see this conversation." action="Sign in" onClick={signIn} />
        : view.s.error !== undefined ? <Copy text={`Couldn't load the focused session: ${view.s.error}`} action="Try again" onClick={retry} />
        : !view.s.target ? <Copy text="This window isn't on a Jam studio. Focus a session to see its conversation." action="Focus a session…" onClick={focus} />
        : "gone" in view.s.target ? <Copy text="That session has ended, or you can't see it." action="Focus a session…" onClick={focus} />
        : <Conversation key={`${view.gen}:${view.s.target.channel}`} bridge={bridge} channel={view.s.target.channel} signIn={signIn} />}
    </MantineProvider>
  );
}

function Copy({ text, action, onClick }: { text: string; action: string; onClick: () => void }) {
  return (
    <Stack align="flex-start" gap="sm" p="md">
      <Text size="sm">{text}</Text>
      <Button size="xs" onClick={onClick}>{action}</Button>
    </Stack>
  );
}

// Conversation is one channel's view for one sign-in, with its own queries.
function Conversation({ bridge, channel, signIn }: { bridge: Bridge; channel: string; signIn: () => void }) {
  const [client] = useState(() => new QueryClient({ defaultOptions: { queries: { staleTime: 10_000, retry: shouldRetry } } }));
  const host = useMemo(() => ({ signIn }), [signIn]);
  return (
    <QueryClientProvider client={client}>
      <HostContext.Provider value={host}>
        <Live />
        <Boot bridge={bridge} channel={channel} />
      </HostContext.Provider>
    </QueryClientProvider>
  );
}

function Live() {
  useLiveEvents();
  return null;
}

function Boot({ bridge, channel }: { bridge: Bridge; channel: string }) {
  const home = `${APP}c/${encodeURIComponent(channel)}`;
  const boot = useQuery<Bootstrap, ApiError>({ queryKey: ["bootstrap"], queryFn: () => apiGet<Bootstrap>("bootstrap"), staleTime: Infinity });
  const onLeave = useCallback((path: string) => {
    const act = linkAction(path, channel);
    if ("intent" in act) bridge.post({ type: "open", intent: act.intent });
    else if ("scrollTo" in act) document.getElementById(act.scrollTo)?.scrollIntoView({ behavior: "smooth", block: "center" });
  }, [bridge, channel]);
  const router = useMemo(() => createMemoryRouter([{
    path: "*",
    element: <><NavBridge home={home} onLeave={onLeave} /><ConversationView key={channel} id={channel} withSession={false} /></>,
  }], { initialEntries: [home] }), [home, channel, onLeave]);
  if (boot.isPending) return <Loader m="md" size="sm" />;
  if (!boot.data) return <Problem error={boot.error!} here={home} />;
  return (
    <BootContext.Provider value={boot.data}>
      <RouterProvider router={router} />
    </BootContext.Provider>
  );
}
