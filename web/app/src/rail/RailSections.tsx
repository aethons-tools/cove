import { Badge, Group, NavLink, ScrollArea, SegmentedControl, Stack, Text, UnstyledButton } from "@mantine/core";
import { useRef, useState, type DragEvent, type KeyboardEvent, type ReactNode } from "react";
import type { AgentItem, CardState, ChannelRow, RailResponse, WorkRow } from "../api/types";
import { kindLabel } from "../conversation/ConversationHeader";
import { SessionCard } from "../session/card/SessionCard";
import { moveTo } from "./order";
import { useRailOrder } from "./useRail";
import "./rail.css";

// RailSections is the rail's Me, Work, and Agents sections and the History
// below them (docs/modules/ROOT/pages/usage/jam/rail-api.adoc#clients),
// shared by the web app (router links) and the VS Code rail (click
// handlers). The caller makes each item open something: rowProps a Me or
// History row, workProps a work row (its unit's page, or its log),
// cardProps an Agents card,
// and onWorker a work row's worker; marker adds a cue to a session's row or
// card. Me rows and Agents cards reorder by drag and drop, or with their
// grip's arrow keys, saving the section's order. active marks the open
// conversation, activeSession the open session, activeWork the open unit.
export function RailSections({ rail, active, activeSession, activeWork, rowProps, workProps, cardProps, onWorker, marker }: {
  rail: RailResponse;
  active?: string;
  activeSession?: string;
  activeWork?: string;
  rowProps: (r: ChannelRow) => Record<string, unknown>;
  workProps: (w: WorkRow) => Record<string, unknown>;
  cardProps: (a: AgentItem) => Record<string, unknown>;
  onWorker: (w: WorkRow) => void;
  marker?: (session: string) => ReactNode;
}) {
  const [filter, setFilter] = useState("all");
  const [historyOpen, setHistoryOpen] = useState(false);
  const save = useRailOrder();
  // Unread keeps the rows with unread or needing you (client-side).
  const me = filter === "unread" ? rail.me.filter((r) => r.unread > 0 || r.needsYou) : rail.me;
  const meSort = useSortable(rail.me.map((r) => r.id), me.map((r) => r.id), (ids) => save.mutate({ section: "me", ids }));
  const agentIds = rail.agents.map((a) => a.session);
  const agentSort = useSortable(agentIds, agentIds, (ids) => save.mutate({ section: "agents", ids }));
  const row = (r: ChannelRow) => (
    <NavLink
      {...rowProps(r)}
      active={r.id === active}
      label={r.label}
      py={4}
      rightSection={
        <Group gap={6} wrap="nowrap">
          {r.needsYou && <span className="rail-needs" role="img" title="Needs you" aria-label="Needs you">●</span>}
          {r.session && marker?.(r.session)}
          {r.card
            ? <Badge size="xs" variant="outline" tt="none" color={stateColor[r.card.state] ?? "gray"}>{r.card.state}</Badge>
            : <Badge size="xs" variant="default" tt="none" radius="sm">{kindLabel[r.kind] ?? r.kind}</Badge>}
          {unreadBadge(r.unread)}
        </Group>
      }
    />
  );
  return (
    <ScrollArea>
      <Stack gap="xs" p="xs">
        <div>
          <Group justify="space-between" wrap="nowrap" pr="sm">
            <Heading>Me</Heading>
            <SegmentedControl size="xs" value={filter} onChange={setFilter} aria-label="Show"
              data={[{ value: "all", label: "All" }, { value: "unread", label: "Unread" }]} />
          </Group>
          <List label="Me">
            {me.map((r) => <li key={r.id} {...meSort.item(r.id)}>{meSort.grip(r.id, row(r))}</li>)}
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
                    active={w.log === active || w.key === activeWork}
                    py={4}
                    label={<Text size="sm" truncate><b>{w.key}</b> {w.title}</Text>}
                    rightSection={unreadBadge(w.unread)}
                  />
                  <UnstyledButton px="sm" pb={4} className="rail-worker" onClick={() => onWorker(w)} aria-label={`Open ${w.worker.label}'s session view`}>
                    <Text span size="xs" c={stateColor[w.worker.state] ?? "dimmed"}>{w.worker.label} · {w.worker.state}</Text>
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
                <li key={a.session} {...agentSort.item(a.session)}>
                  {agentSort.grip(a.session, (
                    <UnstyledButton {...cardProps(a)} w="100%" aria-label={a.card.name} data-active={a.session === activeSession || undefined} className="rail-card">
                      <Group gap={4} wrap="nowrap" align="flex-start">
                        <div style={{ flex: 1, minWidth: 0 }}><SessionCard card={a.card} compact /></div>
                        {marker?.(a.session)}
                      </Group>
                    </UnstyledButton>
                  ))}
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

// stateColor is a card state's colour (as the status card's pill).
const stateColor: Record<CardState, string> = { thinking: "blue", working: "green", waiting: "yellow", idle: "gray", sleeping: "dark" };

const unreadBadge = (n: number | undefined) => (n ? <Badge size="sm" circle aria-label={`${n} unread`}>{n}</Badge> : null);

function Heading({ children }: { children: ReactNode }) {
  return <Text component="h3" size="xs" fw={700} c="dimmed" tt="uppercase" m={0} px="sm" py={4}>{children}</Text>;
}

function List({ label, children }: { label: string; children: ReactNode }) {
  return <ul aria-label={label} className="rail-list">{children}</ul>;
}

// useSortable is a section's reordering. all is the section's ids, shown
// the ones on screen (Me's filter hides some); a drop or an arrow key moves
// an item to a shown item's place in all, so hidden ones keep theirs, and
// hands the new order to onMove.
function useSortable(all: string[], shown: string[], onMove: (ids: string[]) => void) {
  const dragged = useRef<string | undefined>(undefined);
  const [over, setOver] = useState<string>();
  const move = (id: string, target: string | undefined) => {
    if (target === undefined) return;
    const next = moveTo(all, id, target);
    if (next !== all) onMove(next);
  };
  const item = (id: string) => ({
    draggable: true,
    className: "rail-item",
    "data-over": (over === id && dragged.current !== id) || undefined,
    onDragStart: (e: DragEvent) => {
      dragged.current = id;
      // Firefox drags only with data set; the drop reads the ref.
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
      if (from !== undefined) move(from, id);
    },
  });
  // grip puts the item's handle before it: a drag cue, and arrow keys that
  // move the item past its shown neighbour.
  const grip = (id: string, body: ReactNode) => (
    <Group gap={0} wrap="nowrap" align="center">
      <span className="rail-grip" role="button" tabIndex={0} aria-label="Reorder" title="Drag, or press ↑ or ↓, to reorder"
        onKeyDown={(e: KeyboardEvent) => {
          const step = e.key === "ArrowUp" ? -1 : e.key === "ArrowDown" ? 1 : 0;
          if (!step) return;
          e.preventDefault();
          move(id, shown[shown.indexOf(id) + step]);
        }}>⋮⋮</span>
      <div style={{ flex: 1, minWidth: 0 }}>{body}</div>
    </Group>
  );
  return { item, grip };
}
