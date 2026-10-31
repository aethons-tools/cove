import type { ReactNode } from "react";
import type { CardState, SessionCard as Card } from "../../api/types";
import { useNow, useStateHistory, WINDOW_MS, type Point } from "./history";
import "./card.css";

// SessionCard is a session's status card (docs/superpowers/specs/
// 2026-10-30-session-status-card-design.md): the agent icon with its context
// bar, name, state pill and count icons; the mission and focus; and a
// sparkline of its recent states. Compact is the rail's size.

// level is a state's height on the sparkline, 0 (bottom) to 4.
const level: Record<string, number> = { thinking: 4, working: 3, waiting: 2, idle: 1, sleeping: 0 };

// contextColor shades the context bar from green (low) through yellow to red.
export function contextColor(pct: number): string {
  const hue = Math.max(0, 120 - Math.max(0, pct - 30) * (120 / 60));
  return `hsl(${Math.round(hue)} 70% 45%)`;
}

export function SessionCard({ card, compact = false }: { card: Card; compact?: boolean }) {
  const points = useStateHistory(card);
  const now = useNow();
  const { used, window } = card.context;
  const pct = window > 0 ? Math.min(100, Math.round((used * 100) / window)) : 0;
  return (
    <div className="sc" data-compact={compact || undefined}>
      <div className="sc-top">
        <div className="sc-icon" aria-hidden>
          {window > 0 && (
            <div className="sc-ctx" data-hot={pct >= 85 || undefined} title={`context ${pct}% full`}>
              <div className="sc-ctx-fill" style={{ height: `${pct}%`, background: contextColor(pct) }} />
            </div>
          )}
        </div>
        <div className="sc-main">
          <div className="sc-name" title={card.name}>{card.name}</div>
          <span className="sc-pill" data-state={card.state} title={card.tool ? `${card.pill}: ${card.tool}` : card.pill}>{card.pill}</span>
          <Counts c={card.counts} />
        </div>
      </div>
      {card.mission && <div className="sc-mission" title={card.mission}>{card.mission}</div>}
      {card.focus && <div className="sc-focus" title={card.focus}>{card.focus}</div>}
      <Sparkline points={points} now={Math.max(now, points.at(-1)!.at)} />
    </div>
  );
}

function Counts({ c }: { c: Card["counts"] }) {
  // [count, kind, one, many, icon]
  const items: [number, string, string, string, ReactNode][] = [
    [c.asks, "ask", "open ask", "open asks", <path key="a" d="M2 3.5h12v7H7l-3 3v-3H2zM8 5v2.5M8 9h.01" />],
    [c.alarms, "alarm", "alarm", "alarms", <path key="a" d="M8 4a5 5 0 1 0 0 10A5 5 0 0 0 8 4zM8 6.5V9l1.5 1.5M2.5 3.5l2-1.5M13.5 3.5l-2-1.5" />],
    [c.previews, "preview", "preview", "previews", <path key="p" d="M1 8s2.5-5 7-5 7 5 7 5-2.5 5-7 5-7-5-7-5zM8 6a2 2 0 1 0 0 4 2 2 0 0 0 0-4z" />],
    [c.processes, "process", "kept process", "kept processes", <path key="k" d="M2 2.5h12v11H2zM4 6l2 2-2 2M7.5 10.5h4" />],
    [c.subagents, "subagent", "subagent", "subagents", <rect key="s" x="2.5" y="2.5" width="11" height="11" rx="3" />],
  ];
  const shown = items.filter(([n]) => n > 0);
  if (shown.length === 0) return null;
  return (
    <div className="sc-counts">
      {shown.map(([n, kind, one, many, icon]) => (
        <span key={kind} className="sc-count" data-kind={kind} title={`${n} ${n === 1 ? one : many}`}>
          <svg viewBox="0 0 16 16" aria-hidden>{icon}</svg>
          {n}
        </span>
      ))}
    </div>
  );
}

const W = 400;
const H = 40;
const y = (s: CardState) => 4 + (4 - (level[s] ?? 1)) * ((H - 8) / 4);

// Sparkline is points as a step line over the last WINDOW_MS, each run in its
// state's colour, with a dashed mark at each init.
export function Sparkline({ points, now }: { points: Point[]; now: number }) {
  const from = now - WINDOW_MS;
  const x = (at: number) => Math.max(0, Math.min(W, ((at - from) / WINDOW_MS) * W));
  const runs = points.map((p, i) => {
    const x1 = x(p.at), x2 = x(points[i + 1]?.at ?? now);
    return { p, x1, x2 };
  });
  const last = points.at(-1);
  return (
    <svg className="sc-spark" viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" role="img"
      aria-label={last ? `recent states, now ${last.state}` : "recent states"}>
      {runs.map(({ p, x1, x2 }, i) => (
        <g key={i}>
          {i > 0 && <line className="sc-step" x1={x1} x2={x1} y1={y(points[i - 1]!.state)} y2={y(p.state)} />}
          <line className="sc-run" data-state={p.state} x1={x1} x2={x2} y1={y(p.state)} y2={y(p.state)} />
          {p.mark && <line className="sc-mark" x1={x1} x2={x1} y1={0} y2={H} />}
        </g>
      ))}
    </svg>
  );
}
