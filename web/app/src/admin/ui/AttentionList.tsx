import { Anchor, Badge, Group, Stack, Text } from "@mantine/core";
import { Link } from "react-router";
import type { AttnRow } from "../api/types";
import { nodeHref } from "../paths";
import { Section } from "./Section";

const chip: Record<string, string> = { broken: "broken", stale: "out of date", alert: "warning", config: "config" };

// AttentionList is a Needs attention card: each item's why, linked to where
// it's fixed, with its kind.
export function AttentionList({ items }: { items: AttnRow[] }) {
  return (
    <Section title="Needs attention" sub={items.length ? `${items.length} item(s)` : undefined}>
      {items.length === 0 ? <Text size="sm" c="dimmed">Nothing needs attention.</Text> : (
        <Stack gap={4}>
          {items.map((it, i) => (
            <Group key={i} gap={6} wrap="nowrap">
              <Text span c={it.kind === "broken" ? "red" : "yellow"} aria-hidden>⚠</Text>
              <Anchor component={Link} to={nodeHref(it.path)} size="sm">{it.why}</Anchor>
              <Badge size="xs" variant="light" color="gray">{chip[it.kind] ?? it.kind}</Badge>
            </Group>
          ))}
        </Stack>
      )}
    </Section>
  );
}
