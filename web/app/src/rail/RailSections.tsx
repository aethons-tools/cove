import { Badge, Group, NavLink, ScrollArea, SegmentedControl, Stack, Text, UnstyledButton } from "@mantine/core";
import { useId, useRef, useState, type DragEvent, type KeyboardEvent, type ReactNode } from "react";
import type { CardState, ChannelRow, RailResponse, WorkRow } from "../api/types";
import { kindLabel } from "../conversation/ConversationHeader";
import { SessionCard } from "../session/card/SessionCard";
import { moveTo, type RailSection } from "./order";
import "./rail.css";

// RailSections is the rail's Me, Work, and Agents sections, and History
// collapsed below them (docs/modules/ROOT/pages/usage/jam/rail-api.adoc).
// rowProps makes a conversation's row (a Me, Work, or History row) a link
// or a button, and sessionProps an Agents card; openSession opens a Work
// row's worker. onReorder gets a section's whole new order, dragged (or
// moved with the grip's arrow keys) while all or only some rows show.
export function RailSections({ rail, active, activeSession, rowProps, sessionProps, openSession, onReorder }: {
  rail: RailResponse;
  active?: string;
  activeSession?: string;
  rowProps: (channel: string) => Record<string, unknown>;
  sessionProps: (session: string) => Record<string, unknown>;
  openSession: (session: string) => void;
  onReorder: (section: RailSection, ids: string[]) => void;
}) {
  const [filter, setFilter] = useState("All");
  const [history, setHistory] = useState(false);
  const me = filter === "Unread" ? rail.me.filter((r) => r.unread > 0 || r.needsYou) : rail.me;
  const meSort = useSortable("me", rail.me.map((r) => r.id), me.map((r) => r.id), onReorder);
  const agentSort = useSortable("agents", rail.agents.map((a) => a.session), rail.agents.map((a) => a.session), onReorder);
  return (
    <ScrollArea>
      <Stack gap="xs" p="xs">
        <Section title="Me" action={
          <SegmentedControl size="xs" data={["All", "Unread"]} value={filter} onChange={setFilter} aria-label="Show" />
        }>
          {me.length === 0 && <Text c="dimmed" size="sm" px="sm">{filter === "Unread" ? "Nothing unread." : "No conversations."}</Text>}
          {me.map((r) => (
            <ChannelItem key={r.id} r={r} active={r.id === active} {...rowProps(r.id)} {...meSort.item(r.id)} grip={meSort.grip(r.id, r.label)} />
          ))}
        </Section>
        {rail.work.length > 0 && (
          <Section title="Work">
            {rail.work.map((w) => <WorkItem key={w.key} w={w} active={w.log === active} openSession={openSession} {...rowProps(w.log)} />)}
          </Section>
        )}
        {rail.agents.length > 0 && (
          <Section title="Agents">
            {rail.agents.map((a) => (
              <NavLink key={a.session} {...sessionProps(a.session)} {...agentSort.item(a.session)} active={a.session === activeSession}
                leftSection={agentSort.grip(a.session, a.card.name)} label={<SessionCard card={a.card} compact />} />
            ))}
          </Section>
        )}
        {rail.history.length > 0 && (
          <div>
            <UnstyledButton aria-expanded={history} onClick={() => setHistory((h) => !h)} px="sm" py={4}>
              <Text size="xs" fw={700} c="dimmed" tt="uppercase"><span aria-hidden>{history ? "▾" : "▸"} </span>History (before the upgrade)</Text>
            </UnstyledButton>
            {history && rail.history.map((r) => <ChannelItem key={r.id} r={r} active={r.id === active} {...rowProps(r.id)} />)}
          </div>
        )}
      </Stack>
    </ScrollArea>
  );
}

function Section({ title, action, children }: { title: string; action?: ReactNode; children: ReactNode }) {
  const id = useId();
  return (
    <section aria-labelledby={id}>
      <Group justify="space-between" px="sm" py={4} wrap="nowrap">
        <Text id={id} size="xs" fw={700} c="dimmed" tt="uppercase">{title}</Text>
        {action}
      </Group>
      {children}
    </section>
  );
}

// stateColor is a card state's pill colour (as the status card's).
const stateColor: Record<CardState, string> = { thinking: "blue", working: "green", waiting: "yellow", idle: "gray", sleeping: "dark" };

const unreadBadge = (n: number | undefined) => (n ? <Badge size="sm" circle aria-label={`${n} unread`}>{n}</Badge> : null);

// ChannelItem is a Me or History row: the label; a dm with an agent's
// state, else the kind; the needs-you dot; and unread.
function ChannelItem({ r, grip, ...rest }: { r: ChannelRow; active: boolean; grip?: ReactNode } & Record<string, unknown>) {
  return (
    <NavLink {...rest} className="rail-item" py={4} leftSection={grip} label={r.label}
      rightSection={
        <Group gap={6} wrap="nowrap">
          {r.needsYou && <span className="rail-needs" role="img" aria-label="Needs you" title="Needs you">●</span>}
          {r.card
            ? <Badge size="xs" variant="outline" tt="none" color={stateColor[r.card.state] ?? "gray"}>{r.card.pill}</Badge>
            : <Badge size="xs" variant="default" tt="none" radius="sm">{kindLabel[r.kind] ?? r.kind}</Badge>}
          {unreadBadge(r.unread)}
        </Group>
      } />
  );
}

// WorkItem is a Work row: key and title, and its worker and the worker's
// state, which opens the worker's session view.
function WorkItem({ w, openSession, ...rest }: { w: WorkRow; active: boolean; openSession: (session: string) => void } & Record<string, unknown>) {
  const open = () => openSession(w.worker.id);
  return (
    <NavLink {...rest} className="rail-item"
      label={<Text size="sm" truncate><b>{w.key}</b> {w.title}</Text>}
      description={
        // A span, not a button: the row around it is itself a link or a button.
        <Text component="span" size="xs" c={stateColor[w.worker.state] ?? "gray"} className="rail-worker"
          role="button" tabIndex={0} aria-label={`Open ${w.worker.label}'s session view`}
          onClick={(e) => { e.preventDefault(); e.stopPropagation(); open(); }}
          onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); e.stopPropagation(); open(); } }}>
          {w.worker.label} · {w.worker.state}
        </Text>
      }
      rightSection={unreadBadge(w.unread)} />
  );
}

// useSortable is a section's drag-and-drop reordering. all is the section's
// ids, shown the ones on screen; a move goes to a shown neighbour's place
// in all, so the hidden rows keep theirs.
function useSortable(section: RailSection, all: string[], shown: string[], onReorder: (s: RailSection, ids: string[]) => void) {
  const dragged = useRef<string | null>(null);
  const [dragging, setDragging] = useState<string | null>(null);
  const [over, setOver] = useState<string | null>(null);
  const move = (id: string, target: string | undefined) => {
    if (target === undefined) return;
    const next = moveTo(all, id, target);
    if (next !== all) onReorder(section, next);
  };
  const item = (id: string) => ({
    draggable: true,
    "data-dragging": dragging === id || undefined,
    "data-over": (over === id && dragging !== id) || undefined,
    onDragStart: (e: DragEvent) => {
      dragged.current = id;
      setDragging(id);
      // Firefox drags only with data set; the section's own drop reads the ref.
      e.dataTransfer?.setData("text/plain", id);
      if (e.dataTransfer) e.dataTransfer.effectAllowed = "move";
    },
    onDragEnd: () => { dragged.current = null; setDragging(null); setOver(null); },
    onDragOver: (e: DragEvent) => {
      if (dragged.current === null) return; // another section's, or not a row
      e.preventDefault();
      setOver(id);
    },
    onDragLeave: () => setOver((o) => (o === id ? null : o)),
    onDrop: (e: DragEvent) => {
      const from = dragged.current;
      if (from === null) return;
      e.preventDefault();
      dragged.current = null;
      setDragging(null);
      setOver(null);
      move(from, id);
    },
  });
  // grip is the row's handle: arrow keys move the row past its shown neighbour.
  const grip = (id: string, label: string) => (
    <span className="rail-grip" role="button" tabIndex={0} aria-label={`Move ${label}`} title="Drag to reorder"
      onClick={(e) => { e.preventDefault(); e.stopPropagation(); }}
      onKeyDown={(e: KeyboardEvent) => {
        const step = e.key === "ArrowUp" ? -1 : e.key === "ArrowDown" ? 1 : 0;
        if (!step) return;
        e.preventDefault();
        e.stopPropagation();
        move(id, shown[shown.indexOf(id) + step]);
      }}>⋮⋮</span>
  );
  return { item, grip };
}
