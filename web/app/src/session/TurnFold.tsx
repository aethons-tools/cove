import { Text, UnstyledButton } from "@mantine/core";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { apiGet, type ApiError } from "../api/client";
import type { ThinkingPage, TurnFold as Fold } from "../api/types";
import { clock } from "./thinking/format";
import { defaultPrefs } from "./thinking/prefs";
import { deriveRows } from "./thinking/rows";
import "./thinking/thinking.css";

// TurnFold is one turn's thinking in the activity: a line, collapsed, that
// opens to the turn's events (GET api/sessions/{id}/thinking between its
// seqs). It is never part of a search.
export function TurnFold({ id, f }: { id: string; f: Fold }) {
  const [open, setOpen] = useState(false);
  const label = `Thinking · turn ${f.turn} · ${clock(f.start)}`;
  return (
    <div>
      <UnstyledButton aria-expanded={open} aria-label={label} onClick={() => setOpen(!open)} fz="xs" c="dimmed" my={2}>
        {open ? "▾" : "▸"} 💭 {label} · {f.events} events
      </UnstyledButton>
      {open && <TurnEvents id={id} f={f} />}
    </div>
  );
}

function TurnEvents({ id, f }: { id: string; f: Fold }) {
  const q = useQuery<ThinkingPage, ApiError>({
    queryKey: ["turn", id, f.stream, f.turn, f.after, f.before],
    queryFn: () => apiGet<ThinkingPage>(`sessions/${encodeURIComponent(id)}/thinking?stream=${encodeURIComponent(f.stream)}&after=${f.after}&before=${f.before}`),
    staleTime: Infinity,
  });
  if (q.isPending) return <Text size="xs" c="dimmed" ml="md">Loading…</Text>;
  if (!q.data) return <Text size="xs" c="red" ml="md">Couldn't load this turn's thinking ({q.error!.status}).</Text>;
  const rows = deriveRows(q.data.events.map((ev) => ({ t: "ev" as const, ev })), defaultPrefs(), "");
  return (
    <div className="te-fold" style={{ marginLeft: 16, borderLeft: "2px solid var(--mantine-color-default-border)", paddingLeft: 8 }}>
      {q.data.before > 0 && <Text size="xs" c="dimmed">… earlier events of this turn are in the Thinking tab</Text>}
      {rows.map((r) => (
        <div key={r.key} className={`te${r.err ? " err" : ""}`} data-kind={r.kind}>
          <div className="te-line"><span className="te-icon">{r.icon}</span>{r.ev?.tool && <b className="te-tool">{r.ev.tool}</b>}<span className="te-text">{r.text}</span></div>
          {r.status && <div className="te-meta"><span className="te-status">{r.status}</span></div>}
        </div>
      ))}
    </div>
  );
}
