// A hub's session tab (docs/modules/ROOT/pages/usage/jam/vscode-hub.adoc#session-tabs):
// the session view without its pane, inside Pane's shell.
import { useCallback, useMemo, type JSX } from "react";
import { Loader } from "@mantine/core";
import { useQuery } from "@tanstack/react-query";
import { createMemoryRouter, RouterProvider } from "react-router";
import { apiGet, type ApiError } from "../api/client";
import type { Bootstrap } from "../api/types";
import { BootContext, useBoot } from "../BootContext";
import { APP } from "../paths";
import { Problem } from "../Problem";
import { useFocusReport } from "../push/useFocusReport";
import { SessionPage } from "../session/SessionPage";
import { useActivity } from "../session/useActivity";
import type { Bridge } from "./bridge";
import { linkAction } from "./links";
import { NavBridge } from "./NavBridge";
import { scrollToAnchor } from "./Pane";

// SessionTab is session's view in a memory router kept on its page. Its
// links are any other pane's: none is its own conversation.
export function SessionTab({ bridge, session }: { bridge: Bridge; session: string }): JSX.Element {
  const home = `${APP}s/${encodeURIComponent(session)}`;
  const boot = useQuery<Bootstrap, ApiError>({ queryKey: ["bootstrap"], queryFn: () => apiGet<Bootstrap>("bootstrap"), staleTime: Infinity });
  const onLeave = useCallback((path: string) => {
    const act = linkAction(path, "");
    if ("intent" in act) bridge.post({ type: "open", intent: act.intent });
    else if ("scrollTo" in act) scrollToAnchor(document, act.scrollTo);
  }, [bridge]);
  const router = useMemo(() => createMemoryRouter([{
    path: "*",
    element: <><NavBridge home={home} onLeave={onLeave} /><DMFocus session={session} /><SessionPage id={session} withPane={false} /></>,
  }], { initialEntries: [home] }), [home, onLeave, session]);
  if (boot.isPending) return <Loader m="md" size="sm" />;
  if (!boot.data) return <Problem error={boot.error!} here={home} />;
  return (
    <BootContext.Provider value={boot.data}>
      <RouterProvider router={router} />
    </BootContext.Provider>
  );
}

// DMFocus tells the extension the conversation the tab shows: the viewer's
// dm with the session, the one its composer posts into (none before that dm
// exists, or once the session has ended). It reads SessionPage's activity.
function DMFocus({ session }: { session: string }): null {
  const boot = useBoot();
  const a = useActivity(session).data?.pages[0];
  useFocusReport(a?.sendTo && a.dmChannel ? a.dmChannel : null, boot.pushFocus);
  return null;
}
