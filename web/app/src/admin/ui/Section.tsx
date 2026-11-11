import { Card, Group, Text, Title } from "@mantine/core";
import type { ReactNode } from "react";

// Section is one titled card of an admin page; footer, when set, is a bar
// along its bottom (a card's Save, say).
export function Section({ title, sub, actions, footer, children }: { title: string; sub?: ReactNode; actions?: ReactNode; footer?: ReactNode; children?: ReactNode }) {
  return (
    <Card withBorder padding="md" radius="md">
      <Card.Section withBorder inheritPadding py="xs">
        <Group justify="space-between" wrap="wrap" gap="xs">
          <Group gap="xs" align="baseline" wrap="wrap">
            <Title order={4}>{title}</Title>
            {sub && <Text size="sm" c="dimmed">{sub}</Text>}
          </Group>
          {actions}
        </Group>
      </Card.Section>
      {children !== undefined && <Card.Section inheritPadding py="sm">{children}</Card.Section>}
      {footer !== undefined && <Card.Section withBorder inheritPadding py="sm" bg="var(--mantine-color-default-hover)">{footer}</Card.Section>}
    </Card>
  );
}
