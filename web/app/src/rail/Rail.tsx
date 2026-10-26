import { Badge, Loader, NavLink, ScrollArea, Stack, Text } from "@mantine/core";
import { NavLink as RouterLink, useLocation, useParams } from "react-router";
import { useRail } from "./useRail";
import { Problem } from "../Problem";

export function Rail() {
  const { channel } = useParams();
  const { data, error, isPending } = useRail(channel);
  const here = useLocation();
  if (isPending) return <Loader m="md" size="sm" />;
  if (error) return <Problem error={error} here={here.pathname + here.search} />;
  if (data.groups.length === 0) return <Text c="dimmed" size="sm" m="md">No conversations yet. When someone messages you, it shows here.</Text>;
  return (
    <ScrollArea>
      <Stack gap="xs" p="xs">
        {data.groups.map((g) => (
          <div key={g.bucket}>
            <Text size="xs" fw={700} c="dimmed" tt="uppercase" px="sm" py={4}>{g.title}</Text>
            {g.rows.map((r) => (
              <NavLink
                key={r.id}
                component={RouterLink}
                to={`/app/next/c/${encodeURIComponent(r.id)}`}
                active={r.id === channel}
                label={r.label}
                description={r.mission || undefined}
                rightSection={r.unread > 0 ? <Badge size="sm" circle aria-label={`${r.unread} unread`}>{r.unread}</Badge> : null}
              />
            ))}
          </div>
        ))}
      </Stack>
    </ScrollArea>
  );
}
