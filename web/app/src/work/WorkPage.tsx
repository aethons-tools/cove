import { Anchor, Badge, Group, Loader, Stack, Tabs, Text, Title } from "@mantine/core";
import { useMediaQuery } from "@mantine/hooks";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useLocation } from "react-router";
import { apiGet, POLL_MS, type ApiError } from "../api/client";
import type { Conversation, WorkUnit } from "../api/types";
import { ConversationActions } from "../conversation/ConversationHeader";
import { ConversationView } from "../conversation/ConversationView";
import { Problem } from "../Problem";
import { useRail } from "../rail/useRail";
import { NotFound } from "../routes/NotFound";
import { NARROW } from "../session/layout";
import { WorkPanel } from "./WorkPanel";

// useWork is GET api/work/{key}: the unit of work's record.
export const useWork = (key: string) => useQuery<WorkUnit, ApiError>({
  queryKey: ["work", key],
  queryFn: () => apiGet<WorkUnit>(`work/${encodeURIComponent(key)}`),
  refetchInterval: POLL_MS,
});

// WorkPage is a unit of work's page (/app/user/w/<key>,
// manual concepts/work.adoc#_seeing_the_work): its header, its work log with
// a composer posting to it, and the panel of its agents and pull requests,
// which Hide folds away. On a narrow screen the log and the panel are two
// tabs. A unit the viewer may not read is not found.
export function WorkPage({ unitKey }: { unitKey: string }) {
  const q = useWork(unitKey);
  const rail = useRail();
  const here = useLocation();
  const narrow = useMediaQuery(NARROW) ?? false;
  const [tab, setTab] = useState<string | null>("log");
  const [open, setOpen] = useState(true);
  if (q.isPending) return <Loader m="md" size="sm" />;
  if (!q.data) return q.error?.status === 404 ? <NotFound /> : <Problem error={q.error!} here={here.pathname + here.search} />;
  const u = q.data;
  // The current worker's status is the rail's (a unit's record has none).
  const row = rail.data?.work.find((w) => w.key === u.key);
  const panel = <WorkPanel unit={u} status={row?.worker} />;
  return (
    <ConversationView id={u.log} withSession={false} frame={(c, log) => (
      <Stack gap={0} h="calc(100dvh - var(--app-shell-header-height, 0px) - 2 * var(--app-shell-padding, 0px))">
        <WorkHeader u={u} c={c} />
        {narrow
          ? (
            <Tabs value={tab} onChange={setTab} flex={1} mih={0} display="flex" style={{ flexDirection: "column" }}>
              <Tabs.List grow>
                <Tabs.Tab value="log">Log</Tabs.Tab>
                <Tabs.Tab value="panel">Agents &amp; pull requests</Tabs.Tab>
              </Tabs.List>
              {tab === "log"
                ? <Stack gap={0} flex={1} mih={0}>{log}</Stack>
                : <Stack component="aside" aria-label="Agents and pull requests" gap={0} p="sm" style={{ overflow: "auto" }}>{panel}</Stack>}
            </Tabs>
          )
          : (
            <Group gap={0} flex={1} mih={0} wrap="nowrap" align="stretch">
              <Stack gap={0} flex={1} miw={0}>{log}</Stack>
              <Stack component="aside" aria-label="Agents and pull requests" gap={0} px="sm" py={6} w={open ? 300 : 40}
                style={{ flex: "none", overflow: "auto", borderLeft: "1px solid var(--mantine-color-default-border)" }}>
                {open
                  ? <>
                      <Anchor component="button" size="sm" c="dimmed" ta="left" onClick={() => setOpen(false)} title="Hide the panel"><span aria-hidden>⟩ </span>Hide</Anchor>
                      {panel}
                    </>
                  : <Anchor component="button" size="sm" c="dimmed" onClick={() => setOpen(true)} aria-label="Show the panel" title="Show the panel">⟨</Anchor>}
              </Stack>
            </Group>
          )}
      </Stack>
    )} />
  );
}

// STATE_COLOR is a unit state's chip colour.
const STATE_COLOR: Record<string, string> = { "in-progress": "green", "in-review": "blue", "needs-input": "orange", blocked: "red", done: "violet" };

// trackerName is the tracker an issue URL is on, for the header's link.
export function trackerName(url: string): string {
  try {
    const host = new URL(url).hostname;
    if (host.endsWith("linear.app")) return "Linear";
    if (host.endsWith("github.com")) return "GitHub";
    return host;
  } catch {
    return "Tracker";
  }
}

// WorkHeader is the page's header: the ticket's key and title, linked to the
// issue, its state, and the log's actions (Join, Leave, Call in).
function WorkHeader({ u, c }: { u: WorkUnit; c: Conversation }) {
  const issue = (text: string, fw?: number) => (u.url
    ? <Anchor href={u.url} target="_blank" rel="noreferrer" c="inherit" underline="hover" fw={fw} title="Open the issue on the tracker">{text}</Anchor>
    : <Text span fw={fw} inherit>{text}</Text>);
  return (
    <Group component="header" aria-label="Unit of work" justify="space-between" gap="sm" px="sm" py="xs" wrap="wrap"
      style={{ borderBottom: "1px solid var(--mantine-color-default-border)" }}>
      <Title order={2} size="h4" fw={400} style={{ flex: 1, minWidth: 0 }}>
        {issue(u.key, 700)}{u.title && <> {issue(u.title)}</>}
      </Title>
      <Group gap="xs" wrap="nowrap">
        {u.state && <Badge variant="light" tt="none" color={STATE_COLOR[u.state] ?? "gray"}>{u.state.replace("-", " ")}</Badge>}
        {u.url && <Anchor href={u.url} target="_blank" rel="noreferrer" size="xs" c="dimmed">{trackerName(u.url)} ↗</Anchor>}
        <ConversationActions c={c} />
      </Group>
    </Group>
  );
}
