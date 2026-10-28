import { Badge, Button, Group, Loader, NavLink, ScrollArea, Stack, Text } from "@mantine/core";
import { useDisclosure } from "@mantine/hooks";
import type { ReactNode } from "react";
import { RecipientPicker } from "../compose/RecipientPicker";
import { NewSession } from "../compose/NewSession";
import { useBoot } from "../BootContext";
import { useChimes } from "../sounds/chimes";
import { NavLink as RouterLink, useLocation, useParams } from "react-router";
import { useRail } from "./useRail";
import { useAppBadge } from "../push/useAppBadge";
import { Problem } from "../Problem";
import { APP } from "../paths";

export function Rail() {
  const { channel } = useParams();
  const { data, error, isPending, dataUpdatedAt } = useRail(channel);
  useAppBadge(data?.unreadTotal);
  const here = useLocation();
  const [picking, picker] = useDisclosure(false);
  const [starting, starter] = useDisclosure(false);
  const boot = useBoot();
  useChimes(data, dataUpdatedAt, channel);
  // The New message (and New session) buttons head every state of the rail,
  // so a member with no conversations yet can start one.
  const frame = (body: ReactNode) => (
    <>
      <Group gap="xs" m="xs">
        <Button size="xs" variant="light" onClick={picker.open}>New message</Button>
        {boot.newSession && <Button size="xs" variant="light" onClick={starter.open}>New session</Button>}
      </Group>
      <RecipientPicker opened={picking} onClose={picker.close} />
      {boot.newSession && <NewSession opened={starting} onClose={starter.close} />}
      {body}
    </>
  );
  if (isPending) return frame(<Loader m="md" size="sm" />);
  if (error) return frame(<Problem error={error} here={here.pathname + here.search} />);
  if (data.groups.length === 0) return frame(<Text c="dimmed" size="sm" m="md">No conversations yet. Start one with New message.</Text>);
  return frame(
    <ScrollArea>
      <Stack gap="xs" p="xs">
        {data.groups.map((g) => (
          <div key={g.bucket}>
            <Text size="xs" fw={700} c="dimmed" tt="uppercase" px="sm" py={4}>{g.title}</Text>
            {g.rows.map((r) => (
              <NavLink
                key={r.id}
                component={RouterLink}
                to={`${APP}c/${encodeURIComponent(r.id)}`}
                active={r.id === channel}
                label={r.label}
                description={r.mission || undefined}
                rightSection={r.unread > 0 ? <Badge size="sm" circle aria-label={`${r.unread} unread`}>{r.unread}</Badge> : null}
              />
            ))}
          </div>
        ))}
      </Stack>
    </ScrollArea>,
  );
}
