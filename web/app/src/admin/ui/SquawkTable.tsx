import { Anchor, Badge, Text } from "@mantine/core";
import { Link } from "react-router";
import { MessageBody } from "../../conversation/MessageBody";
import type { SquawkRow } from "../api/types";
import { nodeHref } from "../paths";
import { DataTable } from "./DataTable";

// SquawkTable is a list of logged messages, newest first. Bodies go through
// MessageBody, the app's one HTML site (squawkrender's sanitized output).
export function SquawkTable({ rows, filtered = false }: { rows: SquawkRow[]; filtered?: boolean }) {
  return (
    <DataTable rows={rows} rowKey={(r) => `${r.at}/${r.fromId}/${r.channelId}/${r.body.length}/${rows.indexOf(r)}`}
      empty={filtered ? "No squawks match." : "No squawks logged yet."} columns={[
        { key: "t", label: "Time", render: (r) => <Text size="sm" component="time">{r.at}</Text> },
        { key: "f", label: "From", render: (r) => r.agentId
          ? <Anchor component={Link} to={nodeHref("agents/" + encodeURIComponent(r.agentId))} ff="monospace" size="sm" title={r.fromId}>{r.from}</Anchor>
          : <Text size="sm" ff="monospace" title={r.fromId}>{r.from}</Text> },
        { key: "c", label: "Channel", render: (r) => r.channelId
          ? <Anchor component={Link} to={nodeHref("health/intercom?participant=" + encodeURIComponent(r.channelId))} ff="monospace" size="sm" title={r.channelId}>{r.channel}</Anchor>
          : <Text size="sm" ff="monospace">{r.channel}</Text> },
        { key: "p", label: "Project", render: (r) => r.project },
        { key: "b", label: "Body", render: (r) => <div style={{ maxWidth: 560 }}><MessageBody html={r.body} plain={r.plain} />{r.plain && <Badge size="xs" variant="light" color="gray">text/plain</Badge>}</div> },
      ]} />
  );
}
