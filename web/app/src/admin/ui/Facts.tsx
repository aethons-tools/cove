import { Group, Stack, Text } from "@mantine/core";
import type { ReactNode } from "react";

export type Fact = [label: string, value: ReactNode | null, unset?: string];

// Facts is a label/value list; a null value shows its unset text, dimmed.
export function Facts({ rows }: { rows: Fact[] }) {
  return (
    <Stack gap={6}>
      {rows.map(([label, value, unset]) => (
        <Group key={label} gap="md" wrap="nowrap" align="flex-start">
          <Text size="sm" c="dimmed" w={150} style={{ flexShrink: 0 }}>{label}</Text>
          {value === null ? <Text size="sm" c="dimmed" data-unset="true">{unset ?? "—"}</Text> : <Text size="sm" component="div">{value}</Text>}
        </Group>
      ))}
    </Stack>
  );
}
