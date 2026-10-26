import { Badge, Group, Paper, Stack, Text, UnstyledButton } from "@mantine/core";
import type { AskCard } from "../api/types";

// OpenAsks lists the conversation's open asks above the stream, so piled-up
// asks can be answered in any order; each jumps to its card.
export function OpenAsks({ asks, onJump }: { asks: AskCard[]; onJump: (id: string) => void }) {
  if (asks.length === 0) return null;
  return (
    <Paper withBorder radius={0} px="sm" py={6} style={{ borderLeft: 0, borderRight: 0, borderTop: 0 }}>
      <Group gap={6} mb={4}><Text size="xs" fw={700}>Open asks</Text><Badge size="xs" circle>{asks.length}</Badge></Group>
      <Stack gap={2}>
        {asks.map((a) => (
          <Group key={a.id} gap={6} wrap="nowrap">
            <UnstyledButton fz="sm" c="var(--mantine-color-anchor)" onClick={() => onJump(a.id)}>{a.question}</UnstyledButton>
            {a.for && <Text size="xs" c="dimmed">for {a.for}</Text>}
          </Group>
        ))}
      </Stack>
    </Paper>
  );
}
