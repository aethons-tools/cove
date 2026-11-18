import { Anchor, Badge, Group, Text } from "@mantine/core";
import type { ReactNode } from "react";
import { Link } from "react-router";
import type { CardState, UnitWorker, WorkUnit, WorkWorker } from "../api/types";

// when is a worker's start or stop: a local date and time of day.
export const when = (iso: string) =>
  new Date(iso).toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });

// STATUS_COLOR is a worker's status colour (as the rail's).
const STATUS_COLOR: Record<CardState, string> = { thinking: "blue", working: "green", waiting: "yellow", idle: "gray", sleeping: "dark" };
// PR_COLOR is a pull request state's chip colour.
const PR_COLOR: Record<string, string> = { open: "green", merged: "violet", closed: "red" };

// WorkPanel is a unit of work's Agents and Pull requests. Agents lists the
// current worker first, with its status (status: the rail's, when the rail
// has the unit), then the past workers, newest first; each links to its
// session view when the viewer may open it.
export function WorkPanel({ unit: u, status }: { unit: WorkUnit; status?: WorkWorker }) {
  const workers = [...u.workers.filter((w) => w.current), ...u.workers.filter((w) => !w.current).reverse()];
  return (
    <>
      <Heading>Agents</Heading>
      {workers.length === 0
        ? <Text size="sm" c="dimmed">No agent has worked on it yet.</Text>
        : (
          <List label="Agents">
            {workers.map((w) => (
              <Item key={`${w.session}-${w.started ?? ""}`} side={w.current && status?.id === w.session
                && <Badge size="xs" variant="outline" tt="none" color={STATUS_COLOR[status.state] ?? "gray"}>{status.state}</Badge>}>
                {w.view
                  ? <Anchor component={Link} to={w.view} size="sm" title="Open its session view">{w.label}</Anchor>
                  : <Text span size="sm" c="dimmed">{w.label}</Text>}
                {dates(w) && <Text size="xs" c="dimmed">{dates(w)}</Text>}
              </Item>
            ))}
          </List>
        )}
      <Heading>Pull requests</Heading>
      {u.pullRequests.length === 0
        ? <Text size="sm" c="dimmed">No pull requests yet.</Text>
        : (
          <List label="Pull requests">
            {u.pullRequests.map((p) => (
              <Item key={p.url} side={<Badge size="xs" variant="outline" tt="none" color={PR_COLOR[p.state] ?? "gray"}>{p.state}</Badge>}>
                <Text size="sm">
                  <Anchor href={p.url} target="_blank" rel="noreferrer" inherit>{p.number ? `#${p.number}` : p.url}</Anchor>
                  {p.title && ` ${p.title}`}
                </Text>
              </Item>
            ))}
          </List>
        )}
      <Text size="xs" c="dimmed" mt="sm">Reviews, merges, and closes also post to the log.</Text>
    </>
  );
}

// dates is when a worker worked, as far as Jam knows; "" = not known.
function dates(w: UnitWorker): string {
  if (w.current) return w.started ? `Worker now, since ${when(w.started)}` : "Worker now";
  if (w.started && w.stopped) return `${when(w.started)} to ${when(w.stopped)}`;
  if (w.started) return `From ${when(w.started)}`;
  if (w.stopped) return `Until ${when(w.stopped)}`;
  return "";
}

function Heading({ children }: { children: ReactNode }) {
  return <Text component="h3" size="xs" fw={700} c="dimmed" tt="uppercase" m={0} mt="sm" mb={4}>{children}</Text>;
}

function List({ label, children }: { label: string; children: ReactNode }) {
  return <ul aria-label={label} style={{ listStyle: "none", margin: 0, padding: 0 }}>{children}</ul>;
}

function Item({ side, children }: { side?: ReactNode; children: ReactNode }) {
  return (
    <li>
      <Group gap="xs" wrap="nowrap" align="center" py={4}>
        <div style={{ flex: 1, minWidth: 0 }}>{children}</div>
        {side}
      </Group>
    </li>
  );
}
