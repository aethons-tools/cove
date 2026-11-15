import { Badge, Group, NavLink, ScrollArea, SegmentedControl, Stack, Text, UnstyledButton } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useRef, useState, type DragEvent, type ReactNode } from "react";
import { apiPut, type ApiError } from "../api/client";
import type { AgentItem, ChannelRow, RailOrderRequest, RailResponse, WorkRow } from "../api/types";
import { SessionCard } from "../session/card/SessionCard";

// RailSections is the rail's Me, Work, and Agents sections and the History
// below them (docs/modules/ROOT/pages/usage/jam/inbox-rail.adoc). The
// caller makes each item open something: rowProps a Me or History row,
// workProps a work row (its log), cardProps an Agents card, and onWorker a
// work row's worker; marker adds a cue to a session's row or card. Me rows
// and Agents cards drag to reorder, saving the section's order.
export function RailSections({ rail, active, rowProps, workProps, cardProps, onWorker, marker }: {
  rail: RailResponse;
  active?: string;
  rowProps: (r: ChannelRow) => Record<string, unknown>;
  workProps: (w: WorkRow) => Record<string, unknown>;
  cardProps: (a: AgentItem) => Record<string, unknown>;
  onWorker: (w: WorkRow) => void;
  marker?: (session: string) => ReactNode;
}) {
  const [filter, setFilter] = useState("all");
  const [historyOpen, setHistoryOpen] = useState(false);
  const save = useSaveOrder();
  const meIds = rail.me.map((r) => r.id);
  const agentIds = rail.agents.map((a) => a.session);
  const meDrag = useDrag(meIds, (ids) => save.mutate({ section: "me", ids }));
  const agentDrag = useDrag(agentIds, (ids) => save.mutate({ section: "agents", ids }));
  // Unread keeps the rows with unread or needing you (client-side).
  const me = filter === "unread" ? rail.me.filter((r) => r.unread > 0 || r.needsYou) : rail.me;
  const row = (r: ChannelRow) => (
    <NavLink
      {...rowProps(r)}
      active={r.id === active}
      label={r.label}
      rightSection={
        <Group gap={4} wrap="nowrap">
          {r.needsYou && <Text span size="xs" c="orange" title="Needs you" aria-label="Needs you">●</Text>}
          {r.session && marker?.(r.session)}
          <Badge size="xs" variant="outline" color="gray">{r.card ? r.card.state : r.kind}</Badge>
          {r.unread > 0 && <Badge size="sm" circle aria-label={`${r.unread} unread`}>{r.unread}</Badge>}
        </Group>
      }
    />
  );
  return (
    <ScrollArea>
      <Stack gap="xs" p="xs">
        <div>
          <Group justify="space-between" px="sm" py={4}>
            <Heading>Me</Heading>
            <SegmentedControl size="xs" value={filter} onChange={setFilter} aria-label="Show"
              data={[{ value: "all", label: "All" }, { value: "unread", label: "Unread" }]} />
          </Group>
          <List label="Me">
            {me.map((r) => <li key={r.id} {...meDrag(r.id)}>{row(r)}</li>)}
          </List>
          {me.length === 0 && <Text c="dimmed" size="sm" px="sm">{filter === "unread" ? "Nothing unread." : "No conversations."}</Text>}
        </div>
        {rail.work.length > 0 && (
          <div>
            <Heading>Work</Heading>
            <List label="Work">
              {rail.work.map((w) => (
                <li key={w.log}>
                  <NavLink
                    {...workProps(w)}
                    active={w.log === active}
                    label={<><b>{w.key}</b> {w.title}</>}
                    rightSection={w.unread ? <Badge size="sm" circle aria-label={`${w.unread} unread`}>{w.unread}</Badge> : null}
                  />
                  <UnstyledButton px="md" pb={4} onClick={() => onWorker(w)} aria-label={`Open ${w.worker.label}'s session view`}>
                    <Text span size="xs" c="dimmed">{w.worker.label} · {w.worker.state}</Text>
                  </UnstyledButton>
                </li>
              ))}
            </List>
          </div>
        )}
        {rail.agents.length > 0 && (
          <div>
            <Heading>Agents</Heading>
            <List label="Agents">
              {rail.agents.map((a) => (
                <li key={a.session} {...agentDrag(a.session)}>
                  <UnstyledButton {...cardProps(a)} w="100%" aria-label={a.card.name}>
                    <Group gap={4} wrap="nowrap" align="flex-start">
                      <div style={{ flex: 1, minWidth: 0 }}><SessionCard card={a.card} compact /></div>
                      {marker?.(a.session)}
                    </Group>
                  </UnstyledButton>
                </li>
              ))}
            </List>
          </div>
        )}
        {rail.history.length > 0 && (
          <div>
            <Heading>
              <UnstyledButton onClick={() => setHistoryOpen((o) => !o)} aria-expanded={historyOpen}>
                History (before the upgrade)
              </UnstyledButton>
            </Heading>
            {historyOpen && <List label="History">{rail.history.map((r) => <li key={r.id}>{row(r)}</li>)}</List>}
          </div>
        )}
      </Stack>
    </ScrollArea>
  );
}

function Heading({ children }: { children: ReactNode }) {
  return <Text component="h3" size="xs" fw={700} c="dimmed" tt="uppercase" px="sm" py={4} m={0}>{children}</Text>;
}

function List({ label, children }: { label: string; children: ReactNode }) {
  return <ul aria-label={label} style={{ listStyle: "none", margin: 0, padding: 0 }}>{children}</ul>;
}

// moveTo is ids with from moved to to's place.
export function moveTo(ids: string[], from: string, to: string): string[] {
  const at = ids.indexOf(to);
  if (from === to || at < 0 || !ids.includes(from)) return ids;
  const out = ids.filter((id) => id !== from);
  out.splice(at, 0, from);
  return out;
}

// useDrag makes a section's items draggable onto each other: a drop moves
// the dragged item to the target's place and hands the new order to onMove.
function useDrag(ids: string[], onMove: (ids: string[]) => void) {
  const dragged = useRef<string | undefined>(undefined);
  const [over, setOver] = useState<string>();
  return (id: string) => ({
    draggable: true,
    "data-over": over === id || undefined,
    style: over === id ? { boxShadow: "inset 0 2px 0 var(--mantine-primary-color-filled)" } : undefined,
    onDragStart: (e: DragEvent) => {
      dragged.current = id;
      e.dataTransfer?.setData("text/plain", id);
    },
    onDragOver: (e: DragEvent) => {
      if (dragged.current === undefined) return; // not one of this section's
      e.preventDefault();
      setOver(id);
    },
    onDragLeave: () => setOver((o) => (o === id ? undefined : o)),
    onDragEnd: () => {
      dragged.current = undefined;
      setOver(undefined);
    },
    onDrop: (e: DragEvent) => {
      e.preventDefault();
      const from = dragged.current;
      dragged.current = undefined;
      setOver(undefined);
      if (from === undefined) return;
      const next = moveTo(ids, from, id);
      if (next !== ids) onMove(next);
    },
  });
}

// useSaveOrder saves a section's order (PUT api/rail/order), showing it in
// every cached rail at once; a refusal puts the server's order back.
function useSaveOrder() {
  const client = useQueryClient();
  return useMutation<void, ApiError, RailOrderRequest>({
    mutationFn: (o) => apiPut("rail/order", o),
    onMutate: async (o) => {
      await client.cancelQueries({ queryKey: ["rail"] }); // an older rail mustn't land on it
      client.setQueriesData<RailResponse>({ queryKey: ["rail"] }, (r) => {
        if (!r) return r;
        const rank = (id: string) => o.ids.indexOf(id);
        return o.section === "me"
          ? { ...r, me: [...r.me].sort((a, b) => rank(a.id) - rank(b.id)) }
          : { ...r, agents: [...r.agents].sort((a, b) => rank(a.session) - rank(b.session)) };
      });
    },
    onError: (e) => notifications.show({ color: "red", message: `Couldn't save the order: ${e.message}` }),
    onSettled: () => client.invalidateQueries({ queryKey: ["rail"] }),
  });
}
