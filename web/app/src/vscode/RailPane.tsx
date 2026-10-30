import { Loader, Text } from "@mantine/core";
import { useEffect, useRef } from "react";
import type { ChannelRow } from "../api/types";
import { Problem } from "../Problem";
import { useRail } from "../rail/useRail";
import { RailList } from "../rail/RailList";
import type { Bridge } from "./bridge";

// DOUBLE_CLICK_MS is how long a click waits, so a double-click's first click
// doesn't also go.
export const DOUBLE_CLICK_MS = 250;

// RailPane is the VS Code rail: the web rail's rows, where a click goes to
// the conversation (its window if open) and a double-click opens the
// session's studio window. No New message, chimes or app badge.
export function RailPane({ bridge, windows, active }: { bridge: Bridge; windows: string[]; active?: string }) {
  const { data, error, isPending } = useRail();
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
  if (isPending) return <Loader m="md" size="sm" />;
  if (error) return <Problem error={error} here="" />;
  if (data.groups.length === 0) return <Text c="dimmed" size="sm" m="md">No conversations yet.</Text>;
  return <RailList groups={data.groups} active={active} rowProps={rowProps} marker={marker} />;
}

