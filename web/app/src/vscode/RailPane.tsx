import { Button, Group, Loader, Modal, Stack, Text } from "@mantine/core";
import { useDisclosure } from "@mantine/hooks";
import { useQuery } from "@tanstack/react-query";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { MemoryRouter } from "react-router";
import { apiGet, type ApiError } from "../api/client";
import type { Bootstrap, ChannelRow } from "../api/types";
import { BootContext } from "../BootContext";
import { NewSession } from "../compose/NewSession";
import { RecipientPicker } from "../compose/RecipientPicker";
import { Composer } from "../composer/Composer";
import { Problem } from "../Problem";
import { useRail } from "../rail/useRail";
import { RailList } from "../rail/RailList";
import type { Bridge } from "./bridge";

// DOUBLE_CLICK_MS is how long a click waits, so a double-click's first click
// doesn't also go.
export const DOUBLE_CLICK_MS = 250;

// RailPane is the VS Code rail: the web rail's rows, where a click goes to
// the conversation (its window if open) and a double-click opens the
// session's studio window, under New message and New session, whose new
// conversation opens in the hub (the go intent). No chimes or app badge.
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
  const go = (r: ChannelRow) => bridge.post({ type: "open", intent: { kind: "go", channel: r.id, session: r.session } });
  const rowProps = (r: ChannelRow) => ({
    component: "button",
    onClick: () => {
      clearTimeout(timer.current);
      timer.current = setTimeout(() => go(r), DOUBLE_CLICK_MS);
    },
    onDoubleClick: () => {
      clearTimeout(timer.current);
      if (r.session && r.ssh) bridge.post({ type: "open", intent: { kind: "session", id: r.session } });
      else go(r);
    },
  });
  const marker = (r: ChannelRow) => (r.session && windows.includes(r.session)
    ? <Text span size="sm" title="Has a window open" aria-label="Has a window open">▣</Text>
    : null);
  if (isPending) return frame(<Loader m="md" size="sm" />);
  if (error) return frame(<Problem error={error} here="" />);
  if (data.groups.length === 0) return frame(<Text c="dimmed" size="sm" m="md">No conversations yet. Start one with New message.</Text>);
  return frame(<RailList groups={data.groups} active={active} rowProps={rowProps} marker={marker} />);
}

