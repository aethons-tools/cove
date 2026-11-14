import { Badge, Group, NavLink, ScrollArea, Stack, Text } from "@mantine/core";
import type { ReactNode } from "react";
import type { ChannelRow, RailGroup } from "../api/types";
import { SessionCard } from "../session/card/SessionCard";

// RailList is the rail's groups and rows, shared by the web app (router
// links) and the VS Code rail (click handlers): rowProps makes a row a link
// or a button, marker adds a cue before its unread badge, and openSession
// opens a status card's session view (the conversation it names) from its
// name.
export function RailList({ groups, active, rowProps, marker, openSession }: {
  groups: RailGroup[];
  active?: string;
  rowProps: (r: ChannelRow) => Record<string, unknown>;
  marker?: (r: ChannelRow) => ReactNode;
  openSession?: (channel: string, session: string) => void;
}) {
  return (
    <ScrollArea>
      <Stack gap="xs" p="xs">
        {groups.map((g) => (
          <div key={g.bucket}>
            <Text size="xs" fw={700} c="dimmed" tt="uppercase" px="sm" py={4}>{g.title}</Text>
            {g.rows.map((r) => {
              const mark = marker?.(r);
              const unread = r.unread > 0 ? <Badge size="sm" circle aria-label={`${r.unread} unread`}>{r.unread}</Badge> : null;
              return (
                <NavLink
                  key={r.id}
                  {...rowProps(r)}
                  active={r.id === active}
                  // A session channel's or an agent DM's row is the session's status card.
                  label={r.card
                    ? <SessionCard card={r.card} compact onName={openSession && r.sessionView ? () => openSession(r.sessionView, r.card!.session) : undefined} />
                    : r.label}
                  description={r.card ? undefined : r.mission || undefined}
                  rightSection={mark || unread ? <Group gap={4} wrap="nowrap">{mark}{unread}</Group> : null}
                />
              );
            })}
          </div>
        ))}
      </Stack>
    </ScrollArea>
  );
}
