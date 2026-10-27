import { Badge, Button, Group, Select, Text, Title } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { apiPost, ApiError } from "../api/client";
import type { Conversation } from "../api/types";
import { useBoot } from "../BootContext";
import { NewBoard } from "../boards/NewBoard";

// ConversationHeader: title, project · kind, Needs you, and the membership
// controls (Join / Leave / Call in), which refetch the conversation and rail.
export function ConversationHeader({ c }: { c: Conversation }) {
  const client = useQueryClient();
  const boot = useBoot();
  const [who, setWho] = useState<string | null>(null);
  const act = useMutation<void, ApiError, { path: string; body: unknown }>({
    mutationFn: ({ path, body }) => apiPost(`conversations/${encodeURIComponent(c.channelId)}/${path}`, body),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ["conversation", c.channelId] });
      void client.invalidateQueries({ queryKey: ["rail"] });
    },
    onError: (e) => notifications.show({ color: "red", message: e.message }),
  });
  return (
    <Group justify="space-between" px="sm" py="xs" wrap="nowrap" style={{ borderBottom: "1px solid var(--mantine-color-default-border)" }}>
      <div>
        <Title order={2} size="h4">{c.label}</Title>
        <Text size="xs" c="dimmed">{c.project}{c.kind && ` · ${c.kind === "session" ? "agent" : c.kind}`}</Text>
      </div>
      <Group gap="xs" wrap="nowrap">
        {c.needsYou && <Badge color="yellow">Needs you</Badge>}
        {c.newBoard && boot.boards && <NewBoard channel={c.channelId} />}
        {c.callIn.length > 0 && (
          <Group gap={4} wrap="nowrap">
            <Select size="xs" placeholder="Call in…" aria-label="Who to call in" value={who} onChange={setWho}
              data={c.callIn.map((o) => ({ value: o.to, label: `${o.label} (${o.kind})` }))} />
            <Button size="xs" variant="default" disabled={!who} loading={act.isPending}
              onClick={() => who && act.mutate({ path: "call-in", body: { who } }, { onSuccess: () => setWho(null) })}>Call in</Button>
          </Group>
        )}
        {c.canJoin && <Button size="xs" loading={act.isPending} onClick={() => act.mutate({ path: "join", body: {} })}>Join</Button>}
        {c.canLeave && <Button size="xs" variant="default" loading={act.isPending} onClick={() => act.mutate({ path: "leave", body: {} })}>Leave</Button>}
      </Group>
    </Group>
  );
}
