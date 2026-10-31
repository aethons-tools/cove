import { Badge, Group, NavLink, ScrollArea, Stack, Text } from "@mantine/core";
import type { ReactNode } from "react";
import type { ChannelRow, RailGroup } from "../api/types";

// RailList is the rail's groups and rows, shared by the web app (router
// links) and the VS Code rail (click handlers): rowProps makes a row a link
// or a button, and marker adds a cue before its unread badge.
export function RailList({ groups, active, rowProps, marker }: {
  groups: RailGroup[];
  active?: string;
  rowProps: (r: ChannelRow) => Record<string, unknown>;
  marker?: (r: ChannelRow) => ReactNode;
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
                  label={r.label}
                  description={r.mission || undefined}
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
