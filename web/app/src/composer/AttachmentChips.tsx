import { CloseButton, Group, Paper, Progress, Text } from "@mantine/core";

export type Pending = { id: number; name: string; pct: number; ref?: string; error?: string; abort: () => void };

// AttachmentChips lists the pending attachments: name, progress, ✕ (which
// aborts a running upload); a failed one says why and is never sent.
export function AttachmentChips({ items, onRemove }: { items: Pending[]; onRemove: (id: number) => void }) {
  if (items.length === 0) return null;
  return (
    <Group gap={6} mb={4}>
      {items.map((p) => (
        <Paper key={p.id} withBorder px={6} py={2} radius="sm" c={p.error ? "red" : undefined}>
          <Group gap={4} wrap="nowrap">
            <Text size="xs">{p.error ? `${p.name} — ${p.error}` : p.name}</Text>
            {!p.ref && !p.error && <Progress w={60} size="xs" value={p.pct} aria-label={`Uploading ${p.name}`} />}
            <CloseButton size="xs" aria-label={`Remove ${p.name}`} onClick={() => onRemove(p.id)} />
          </Group>
        </Paper>
      ))}
    </Group>
  );
}
