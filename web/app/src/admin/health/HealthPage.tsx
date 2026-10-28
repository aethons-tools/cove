import { Anchor, Badge, Code, Group, Loader, Stack, Text, Title } from "@mantine/core";
import { useQuery } from "@tanstack/react-query";
import { Link, useLocation } from "react-router";
import type { ApiError } from "../../api/client";
import { Problem } from "../../Problem";
import type { Health } from "../api/types";
import { adminApi, nodeHref } from "../paths";
import { Section } from "../ui/Section";

const sev: Record<string, string> = { critical: "red", warning: "yellow", info: "gray" };

// HealthPage is the Health tab: the problems Jam has detected in itself,
// each with how to fix it (polled every 10 s), and the last week's resolved.
export function HealthPage() {
  const here = useLocation();
  const { data, error, isPending } = useQuery<Health, ApiError>({ queryKey: ["admin", "health"], queryFn: () => adminApi.get("health"), refetchInterval: 10_000 });
  if (isPending) return <Loader size="sm" />;
  if (error) return <Problem error={error} here={here.pathname} />;
  return (
    <Stack>
      <Group gap="sm" align="baseline" wrap="wrap">
        <Title order={2}>Health</Title>
        <Text size="sm" c="dimmed">Problems Jam has detected in itself — each with how to fix it. Pushed to operators by Alertmanager when configured.</Text>
        <Anchor component={Link} to={nodeHref("health/intercom")} size="sm">Channel log</Anchor>
        {data.silences && <Anchor href={data.silences} size="sm" target="_blank" rel="noopener noreferrer">Silences ↗</Anchor>}
      </Group>
      <Section title="Open" sub={data.open.length ? String(data.open.length) : undefined}>
        {data.open.length === 0 ? <Text size="sm" c="dimmed">Nothing needs attention.</Text> : (
          <Stack gap="sm">
            {data.open.map((c) => (
              <Stack key={c.key} gap={4} id={`cond-${c.key}`}>
                <Group gap={6}>
                  <Badge size="sm" color={sev[c.severity] ?? "gray"}>{c.severity}</Badge>
                  <Text fw={600} size="sm">{c.summary}</Text>
                  <Text size="sm" c="dimmed">for {c.age}</Text>
                </Group>
                {c.fix && <Code block style={{ whiteSpace: "pre-wrap", userSelect: "all" }}>{c.fix}</Code>}
                {c.detail && <details><summary style={{ cursor: "pointer" }}>Detail</summary><Code block style={{ whiteSpace: "pre-wrap" }}>{c.detail}</Code></details>}
                <Text size="xs" c="dimmed" ff="monospace">{c.key}</Text>
              </Stack>
            ))}
          </Stack>
        )}
      </Section>
      <Section title="Resolved (7 days)">
        {data.resolved.length === 0 ? <Text size="sm" c="dimmed">None.</Text> : (
          <Stack gap={4}>
            {data.resolved.map((c, i) => (
              <Group key={`${c.key}/${i}`} gap={6}>
                <Badge size="sm" variant="light" color="gray">{c.severity}</Badge>
                <Text size="sm">{c.summary}</Text>
                <Text size="sm" c="dimmed">— lasted {c.lasted}, resolved {c.age} ago</Text>
              </Group>
            ))}
          </Stack>
        )}
      </Section>
    </Stack>
  );
}
