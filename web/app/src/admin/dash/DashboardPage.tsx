import { Alert, Badge, Card, Loader, Progress, SimpleGrid, Stack, Text, Title } from "@mantine/core";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { Link, useLocation } from "react-router";
import type { ApiError } from "../../api/client";
import { Problem } from "../../Problem";
import type { ContextPanel as Panel, ContextSave, Dashboard, ManagerRow } from "../api/types";
import { adminApi, nodeHref } from "../paths";
import { AttentionList } from "../ui/AttentionList";
import { ContextPanel } from "../ui/ContextPanel";
import { CovesTable } from "../ui/CovesTable";
import { DataTable } from "../ui/DataTable";
import { Section } from "../ui/Section";
import { useAdminMutation } from "../ui/useAdminMutation";
import { OperatorNotes } from "../ui/OperatorNotes";

const dashKey = ["admin", "dashboard"];

// Tile is a summary count, linking its list when it has one.
function Tile({ n, label, to, color }: { n: number; label: string; to?: string; color?: string }) {
  const inner = <><Text fz={26} fw={600} lh={1.1} c={color}>{n}</Text><Text size="xs" c="dimmed" fw={500}>{label}</Text></>;
  const props = { withBorder: true, padding: "sm", radius: "md" } as const;
  return to
    ? <Card {...props} component={Link} to={nodeHref(to)} style={{ textDecoration: "none" }}>{inner}</Card>
    : <Card {...props}>{inner}</Card>;
}

// DashboardPage is the admin area's root: the fleet at a glance, what needs
// attention, the Jam-wide agent context, the Studio Managers and the
// running studios (polled every 3 s).
export function DashboardPage() {
  const here = useLocation();
  const client = useQueryClient();
  const { data, error, isPending } = useQuery<Dashboard, ApiError>({ queryKey: dashKey, queryFn: () => adminApi.get("dashboard"), refetchInterval: 3000 });
  const setPanel = (p: Panel) => client.setQueryData<Dashboard>(dashKey, (d) => d && { ...d, jamContext: p });
  // A write refetches the dashboard too, cancelling a poll already in flight
  // (which could answer with the old context).
  const save = useAdminMutation((body: ContextSave) => adminApi.post<Panel>("jam/context", body), { invalidate: [dashKey], onSuccess: setPanel });
  const clear = useAdminMutation(() => adminApi.del<Panel>("jam/context"), { invalidate: [dashKey], onSuccess: setPanel });
  if (isPending) return <Loader size="sm" />;
  // A failed refresh keeps the page (and the editor's unsaved text).
  if (!data) return <Problem error={error} here={here.pathname} />;
  const s = data.stats;
  const tiles: [number, string, string | undefined, string?][] = [
    [s.running, "Running", "agents?status=running", "green"],
    [s.waiting, "Waiting", "agents?status=waiting", s.waiting ? "yellow" : undefined],
    [s.settingUp, "Setting up", "agents?status=setting-up"],
    [s.attention, "Lost / terminating", "agents?status=attention", s.attention ? "red" : undefined],
    [s.idled, "Idled", "agents?status=idled"],
    [s.projects, "Projects", undefined],
    [s.agents, "Agents", "agents"],
    [s.users, "Users", "config/users"],
    [s.specs, "Specs", "config/kits"],
  ];
  return (
    <Stack>
      <Title order={2}>Dashboard</Title>
      {error && <Alert color="yellow">Refresh failed ({error.message}) — showing the last answer; retrying.</Alert>}
      <SimpleGrid cols={{ base: 2, sm: 3, md: 5, lg: 9 }}>
        {tiles.map(([n, label, to, color]) => <Tile key={label} n={n} label={label} to={to} color={color} />)}
      </SimpleGrid>
      <AttentionList items={data.attention} />
      <OperatorNotes notes={data.jamNotes} where="jam.yml" />
      <ContextPanel key={data.jamContext.yaml} panel={data.jamContext} onSave={(body) => save.mutateAsync(body)} onClear={() => clear.mutateAsync(undefined)} />
      {data.hasManagers && (
        <Section title="Managers">
          <DataTable rows={data.managers} rowKey={(m) => m.name} empty="No managers." columns={[
            { key: "n", label: "Manager", render: (m) => <Text fw={600} ff="monospace" size="sm">{m.name}</Text> },
            { key: "s", label: "Status", render: (m) => <Badge size="sm" variant="light" color={m.connected ? "green" : "red"}>{m.connected ? "connected" : "disconnected"}</Badge> },
            { key: "v", label: "Version", render: (m) => dash(m.version && <Text span ff="monospace" size="sm">{m.version}</Text>) },
            { key: "c", label: "Studios", render: (m) => m.capacity ? `${m.studios}/${m.capacity}` : m.studios },
            { key: "l", label: "Last seen", render: (m) => dash(m.lastSeen) },
            { key: "x", label: "Strays", render: (m) => m.strays.length ? <Text span size="sm" title={m.strays.join(", ")}>{m.strays.length}</Text> : <Text span c="dimmed" size="sm">0</Text> },
            { key: "mem", label: "Memory", render: (m) => usage(m, "memory", m.host?.mem, m.host?.memPct) },
            { key: "disk", label: "Disk", render: (m) => usage(m, "disk", m.host?.disk, m.host?.diskPct) },
            { key: "r", label: "Reclaimable", render: (m) => dash(m.host?.reclaimable && <Text span size="sm" title={m.host.reclaimableDetail}>{m.host.reclaimable}</Text>) },
            { key: "lc", label: "Last cleanup", render: (m) => dash(m.host?.lastCleanup && <Text span size="sm">{m.host.lastCleanup}</Text>) },
            { key: "a", label: "Activity", render: (m) => dash(m.activities.length > 0 && (
              <Stack gap={2}>{m.activities.map((a, i) => <Text key={i} span size="sm">{a}</Text>)}</Stack>
            )) },
          ]} />
        </Section>
      )}
      <Section title="Running agents">
        <CovesTable rows={data.coves} />
      </Section>
    </Stack>
  );
}

const dash = (v: ReactNode) => v || <Text span c="dimmed">—</Text>;

// usage is a manager's used/total text over a bar: yellow from 90%, red
// from 95% (Jam's manager.disk thresholds). The title says when it was
// measured, as the report is kept while the manager is disconnected.
function usage(m: ManagerRow, what: string, text: string | undefined, pct = 0) {
  if (!text) return dash(null);
  const color = pct >= 95 ? "red" : pct >= 90 ? "yellow" : "blue";
  return (
    <Stack gap={2} title={m.host?.at ? `measured ${m.host.at}` : undefined}>
      <Text span size="sm" ff="monospace">{text}</Text>
      <Progress size="sm" w={120} value={pct} color={color} aria-label={`${m.name} ${what} ${pct}% used`} />
    </Stack>
  );
}
