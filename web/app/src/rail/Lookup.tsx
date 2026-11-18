import { Badge, CloseButton, Group, Highlight, NavLink, ScrollArea, SegmentedControl, Stack, Text, TextInput } from "@mantine/core";
import { useDebouncedValue } from "@mantine/hooks";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { NavLink as RouterLink, useLocation } from "react-router";
import { apiGet, type ApiError } from "../api/client";
import type { LookupConversation, LookupMatch, LookupResponse, LookupSession } from "../api/types";
import { kindLabel } from "../conversation/ConversationHeader";
import { APP } from "../paths";
import { Problem } from "../Problem";
import { Heading, List } from "./RailSections";

// The rail's lookup of finished conversations and ended sessions
// (docs/modules/ROOT/pages/usage/jam/rail-api.adoc#lookup): a box at the
// top of the rail; while it has text, LookupResults take the sections' place.

// LookupBox is the rail's lookup box; clearing it brings the sections back.
export function LookupBox({ q, onChange }: { q: string; onChange: (q: string) => void }) {
  return (
    <TextInput mx="xs" mt="xs" size="sm" value={q} onChange={(e) => onChange(e.currentTarget.value)}
      placeholder="Look up conversations and sessions" aria-label="Look up conversations and sessions"
      onKeyDown={(e) => e.key === "Escape" && onChange("")}
      rightSection={q ? <CloseButton size="sm" aria-label="Clear" onClick={() => onChange("")} /> : null} />
  );
}

// useLookup is GET api/lookup for q, once typing pauses.
export function useLookup(q: string) {
  const [settled] = useDebouncedValue(q.trim(), 250);
  return useQuery<LookupResponse, ApiError>({
    queryKey: ["lookup", settled],
    queryFn: () => apiGet<LookupResponse>(`lookup?q=${encodeURIComponent(settled)}`),
    enabled: settled !== "",
    placeholderData: keepPreviousData,
  });
}

// whyLabel is how a result says why a conversation finished.
function whyLabel(c: LookupConversation): string {
  if (c.why === "ended") return c.row.kind === "dm" ? "agent ended" : "ended";
  return c.why;
}

// sessionKind is how a result names an ended session's kind.
const sessionKind: Record<string, string> = { standing: "standing session", personal: "personal session", ticket: "ticket session", session: "session" };

const day = (iso: string) => new Date(iso).toLocaleDateString(undefined, { month: "short", day: "numeric" });

// LookupResults are the matches for q, in two groups, with All /
// Conversations / Sessions to narrow them; active is the open conversation,
// activeSession the open session view.
export function LookupResults({ q, active, activeSession }: { q: string; active?: string; activeSession?: string }) {
  const [only, setOnly] = useState("all");
  const { data, error, isPending } = useLookup(q);
  const here = useLocation();
  const words = q.trim();
  let body: ReactNode;
  if (error) body = <Problem error={error} here={here.pathname + here.search} />;
  else if (isPending || !data) body = <Text c="dimmed" size="sm" px="sm">Looking up…</Text>;
  else {
    const convs = only === "sessions" ? [] : data.conversations;
    const sessions = only === "conversations" ? [] : data.sessions;
    body = convs.length + sessions.length === 0
      ? <Text c="dimmed" size="sm" px="sm">Nothing you may read matches “{words}”.</Text>
      : (
        <>
          {convs.length > 0 && (
            <div>
              <Heading>Finished conversations</Heading>
              <List label="Finished conversations">
                {convs.map((c) => (
                  <li key={c.row.id}>
                    <Result to={`${APP}c/${encodeURIComponent(c.row.id)}`} active={c.row.id === active} words={words}
                      name={c.row.label} kind={kindLabel[c.row.kind] ?? c.row.kind} sub={c.title} match={c.match}
                      why={whyLabel(c)} when={c.finished} />
                  </li>
                ))}
              </List>
            </div>
          )}
          {sessions.length > 0 && (
            <div>
              <Heading>Ended sessions</Heading>
              <List label="Ended sessions">
                {sessions.map((s: LookupSession) => (
                  <li key={s.session}>
                    <Result to={`${APP}s/${encodeURIComponent(s.session)}`} active={s.session === activeSession} words={words}
                      name={s.label} kind={sessionKind[s.kind] ?? s.kind} sub={s.mission} match={s.match} why="ended" when={s.ended} />
                  </li>
                ))}
              </List>
            </div>
          )}
        </>
      );
  }
  return (
    <ScrollArea>
      <Stack gap="xs" p="xs">
        <SegmentedControl size="xs" value={only} onChange={setOnly} aria-label="Show"
          data={[{ value: "all", label: "All" }, { value: "conversations", label: "Conversations" }, { value: "sessions", label: "Sessions" }]} />
        {body}
      </Stack>
    </ScrollArea>
  );
}

// Result is one match: its name and kind, its title or mission, the
// matching message, and why and when it finished, with the words looked up
// highlighted.
function Result({ to, active, words, name, kind, sub, match, why, when }: {
  to: string; active: boolean; words: string; name: string; kind: string; sub?: string; match: LookupMatch | null; why: string; when: string;
}) {
  const hl = (text: string) => <Highlight component="span" size="xs" c="dimmed" highlight={words} truncate>{text}</Highlight>;
  return (
    <NavLink component={RouterLink} to={to} active={active} py={4}
      label={
        <Group gap={6} wrap="nowrap">
          <Highlight component="span" size="sm" highlight={words} truncate>{name}</Highlight>
          <Badge size="xs" variant="default" tt="none" radius="sm">{kind}</Badge>
        </Group>
      }
      description={
        <>
          {sub && <div>{hl(sub)}</div>}
          {match && <div>{hl(`${match.from}: ${match.text}`)}</div>}
        </>
      }
      rightSection={
        <Stack gap={0} align="flex-end">
          <Text size="xs" c="dimmed">{why}</Text>
          <Text size="xs" c="dimmed">{day(when)}</Text>
        </Stack>
      } />
  );
}
