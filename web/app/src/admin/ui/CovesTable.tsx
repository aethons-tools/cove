import { Anchor, Badge, Group, Text } from "@mantine/core";
import { Link } from "react-router";
import type { CoveRow } from "../api/types";
import { nodeHref } from "../paths";
import { AttnFlag } from "./AttnFlag";
import { DataTable } from "./DataTable";
import { StatusBadge } from "./StatusBadge";

const enc = encodeURIComponent;
const dash = (s: string) => s || <Text span c="dimmed">—</Text>;

// staleBadge is a connector or image state: stale amber, error red.
export function StateCell({ v, title }: { v: string; title?: string }) {
  if (v === "stale") return <Badge size="sm" color="yellow" variant="light" title={title}>stale</Badge>;
  if (v === "error") return <Badge size="sm" color="red" variant="light">error</Badge>;
  if (v === "ok") return <>ok</>;
  return <Text span c="dimmed" size="sm">{v || "—"}</Text>;
}

// CovesTable is the running-studio table: each links its agent (the Agents
// tab) and its live session timeline.
export function CovesTable({ rows }: { rows: CoveRow[] }) {
  return (
    <DataTable rows={rows} rowKey={(c) => c.id} empty="No agents running." columns={[
      { key: "a", label: "Agent", render: (c) => (
        <Group gap={6} wrap="nowrap">
          <Anchor component={Link} to={nodeHref("agents/" + enc(c.id))} fw={600} ff="monospace" size="sm">{c.name || c.id}</Anchor>
          {c.name && <Text size="xs" c="dimmed" ff="monospace">{c.id}</Text>}
          <AttnFlag flag={c.attn} />
          <Anchor component={Link} to={nodeHref(`agents/${enc(c.id)}/session`)} size="xs" title="Live session timeline">timeline</Anchor>
        </Group>
      ) },
      { key: "s", label: "Status", render: (c) => <StatusBadge status={c.status} /> },
      { key: "c", label: "Connector", render: (c) => <StateCell v={c.connector} /> },
      { key: "i", label: "Image", render: (c) => <StateCell v={c.image} title="raised on an older image than its role would run now" /> },
      { key: "p", label: "Project", render: (c) => <Anchor component={Link} to={nodeHref("config/projects/" + enc(c.project))} size="sm">{c.project}</Anchor> },
      { key: "r", label: "Role", render: (c) => <Anchor component={Link} to={nodeHref(`config/projects/${enc(c.project)}/agents/${enc(c.role)}`)} size="sm">{c.role}</Anchor> },
      { key: "m", label: "Manager", render: (c) => dash(c.manager) },
      { key: "u", label: "Unit", render: (c) => dash(c.unit) },
      { key: "l", label: "Lease", render: (c) => dash(c.leaseHolder) },
      { key: "ra", label: "Raised", render: (c) => c.raisedAt },
      { key: "ls", label: "Last seen", render: (c) => dash(c.lastSeen) },
    ]} />
  );
}
