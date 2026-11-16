import { Button, Group, Loader, Modal, Stack, Text } from "@mantine/core";
import { useDisclosure } from "@mantine/hooks";
import { useQuery } from "@tanstack/react-query";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { MemoryRouter } from "react-router";
import { apiGet, type ApiError } from "../api/client";
import { notifications } from "@mantine/notifications";
import type { AgentItem, Bootstrap, ChannelRow, SessionHead, WorkRow } from "../api/types";
import { BootContext } from "../BootContext";
import { NewSession } from "../compose/NewSession";
import { RecipientPicker } from "../compose/RecipientPicker";
import { Composer } from "../composer/Composer";
import { Problem } from "../Problem";
import { useRail } from "../rail/useRail";
import { RailSections } from "../rail/RailSections";
import type { Bridge } from "./bridge";

// DOUBLE_CLICK_MS is how long a click waits, so a double-click's first click
// doesn't also go.
export const DOUBLE_CLICK_MS = 250;

// RailPane is the VS Code rail: the rail's sections, where a click goes to
// the conversation's tab (even when its session has a window open) and a
// double-click on a Me row or an Agents card opens the session's studio
// window or brings it to the front, under New message and New session, whose
// new conversation opens in the hub (the go intent). A card or a work row's
// worker opens the session's view: its conversation's tab. No chimes or app
// badge.
export function RailPane({ bridge, windows, active }: { bridge: Bridge; windows: string[]; active?: string }) {
  const { data, error, isPending } = useRail();
  const boot = useQuery<Bootstrap, ApiError>({ queryKey: ["bootstrap"], queryFn: () => apiGet<Bootstrap>("bootstrap"), staleTime: Infinity });
  const [picking, picker] = useDisclosure(false);
  const [starting, starter] = useDisclosure(false);
  const [to, setTo] = useState<{ to: string; label: string }>();
  const goChannel = (channel: string, session: string) => bridge.post({ type: "open", intent: { kind: "go", channel, session } });
  const frame = (body: ReactNode) => (
    <>
      {boot.data && (
        // The pickers' default navigation needs a router; here they call back instead.
        <MemoryRouter>
        <BootContext.Provider value={boot.data}>
          <Group gap="xs" m="xs">
            <Button size="xs" variant="light" onClick={picker.open}>New message</Button>
            {boot.data.newSession && <Button size="xs" variant="light" onClick={starter.open}>New session</Button>}
          </Group>
          {/* returnFocus off: the composer, not the button behind it, takes the focus */}
          <RecipientPicker opened={picking} onClose={picker.close} onPick={setTo} returnFocus={false} />
          {boot.data.newSession && <NewSession opened={starting} onClose={starter.close} onStarted={(s) => goChannel(s.channel, s.id)} />}
          {/* Full screen: a sidebar is too narrow for the composer's row in a dialog. */}
          <Modal opened={to !== undefined} onClose={() => setTo(undefined)} title="New message" fullScreen>
            {to && (
              <Stack gap="xs">
                <Text size="xs" c="dimmed">to {to.label}</Text>
                <Composer key={to.to} to={to.to} label={to.label} attachMax={boot.data.attachMax} autoFocus
                  onSent={(m) => { setTo(undefined); goChannel(m.channel, ""); }} />
              </Stack>
            )}
          </Modal>
        </BootContext.Provider>
        </MemoryRouter>
      )}
      {body}
    </>
  );
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);
  // clicks runs single after DOUBLE_CLICK_MS unless a double-click comes, which runs double.
  const clicks = (single: () => void, double: () => void = single) => ({
    component: "button",
    onClick: () => {
      clearTimeout(timer.current);
      timer.current = setTimeout(single, DOUBLE_CLICK_MS);
    },
    onDoubleClick: () => {
      clearTimeout(timer.current);
      double();
    },
  });
  const openWindow = (session: string) => bridge.post({ type: "open", intent: { kind: "session", id: session } });
  // goSession goes to a session's view: its conversation for the viewer.
  const goSession = (session: string) => void apiGet<SessionHead>(`sessions/${encodeURIComponent(session)}`).then(
    (h) => h.channel ? goChannel(h.channel, session) : notifications.show({ message: "That session has no conversation you can open." }),
    (e: ApiError) => notifications.show({ color: "red", message: e.message }),
  );
  const rowProps = (r: ChannelRow) => {
    const go = () => goChannel(r.id, r.session);
    return clicks(go, r.session && r.ssh ? () => openWindow(r.session) : go);
  };
  const workProps = (w: WorkRow) => clicks(() => goChannel(w.log, w.worker.id));
  const cardProps = (a: AgentItem) => {
    const go = () => goSession(a.session);
    return clicks(go, a.ssh ? () => openWindow(a.session) : go);
  };
  const marker = (session: string) => (windows.includes(session)
    ? <Text span size="sm" title="Has a window open" aria-label="Has a window open">▣</Text>
    : null);
  if (isPending) return frame(<Loader m="md" size="sm" />);
  if (error) return frame(<Problem error={error} here="" />);
  if (data.me.length + data.work.length + data.agents.length + data.history.length === 0) {
    return frame(<Text c="dimmed" size="sm" m="md">No conversations yet. Start one with New message.</Text>);
  }
  return frame(<RailSections rail={data} active={active} rowProps={rowProps} workProps={workProps} cardProps={cardProps} marker={marker}
    onWorker={(w) => { clearTimeout(timer.current); goSession(w.worker.id); }} />);
}
