import { Button, Group, Loader, Text } from "@mantine/core";
import { useDisclosure } from "@mantine/hooks";
import { useState, type ReactNode } from "react";
import { RecipientPicker } from "../compose/RecipientPicker";
import { NewSession } from "../compose/NewSession";
import { useBoot } from "../BootContext";
import { useChimes } from "../sounds/chimes";
import { NavLink as RouterLink, useLocation, useNavigate, useParams } from "react-router";
import { useRail } from "./useRail";
import { RailSections } from "./RailSections";
import { LookupBox, LookupResults } from "./Lookup";
import { useAppBadge } from "../push/useAppBadge";
import { Problem } from "../Problem";
import { APP, workPath } from "../paths";

export function Rail() {
  const { channel, session, key } = useParams();
  const { data, error, isPending, dataUpdatedAt } = useRail(channel);
  useAppBadge(data?.unreadTotal);
  const here = useLocation();
  const [picking, picker] = useDisclosure(false);
  const [starting, starter] = useDisclosure(false);
  const boot = useBoot();
  const nav = useNavigate();
  const [q, setQ] = useState("");
  useChimes(data, dataUpdatedAt, channel);
  // The lookup box and the New message (and New session) buttons head every
  // state of the rail, so a member with no conversations yet can start one.
  // While the box has text, its results take the sections' place.
  const frame = (body: ReactNode) => (
    <>
      <LookupBox q={q} onChange={setQ} />
      <Group gap="xs" m="xs">
        <Button size="xs" variant="light" onClick={picker.open}>New message</Button>
        {boot.newSession && <Button size="xs" variant="light" onClick={starter.open}>New session</Button>}
      </Group>
      <RecipientPicker opened={picking} onClose={picker.close} />
      {boot.newSession && <NewSession opened={starting} onClose={starter.close} />}
      {q.trim() ? <LookupResults q={q} active={channel} activeSession={session} /> : body}
    </>
  );
  if (isPending) return frame(<Loader m="md" size="sm" />);
  if (error) return frame(<Problem error={error} here={here.pathname + here.search} />);
  if (data.me.length + data.work.length + data.agents.length + data.history.length === 0) {
    return frame(<Text c="dimmed" size="sm" m="md">No conversations yet. Start one with New message.</Text>);
  }
  return frame(
    <RailSections rail={data} active={channel} activeSession={session} activeWork={key}
      rowProps={(r) => ({ component: RouterLink, to: `${APP}c/${encodeURIComponent(r.id)}` })}
      workProps={(w) => ({ component: RouterLink, to: workPath(w.key) })}
      cardProps={(a) => ({ component: RouterLink, to: `${APP}s/${encodeURIComponent(a.session)}` })}
      onWorker={(w) => nav(`${APP}s/${encodeURIComponent(w.worker.id)}`)} />,
  );
}
