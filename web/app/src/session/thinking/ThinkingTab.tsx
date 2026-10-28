import { Loader, NativeSelect, Text, TextInput } from "@mantine/core";
import { useVirtualizer } from "@tanstack/react-virtual";
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { ApiError } from "../../api/client";
import type { ThinkingStream } from "../../api/types";
import { usePaneTab } from "../useSession";
import { clock, num } from "./format";
import { FORMATS, loadPrefs, savePrefs, type Chip, type Format, type ThinkingPrefs } from "./prefs";
import { deriveRows, toolsIn, type Row } from "./rows";
import { userThinkingSource, type ThinkingSource } from "./source";
import { useThinkingFeed, type FeedCallbacks } from "./useThinkingFeed";
import "./thinking.css";

const CHIPS: [Chip, string][] = [["thinking", "Thinking"], ["text", "Text"], ["tools", "Tools"], ["errors", "Errors only"], ["progress", "Progress"]];
const CLIP = 40;

// rawCache keeps fetched raw JSON across scrolls (a virtual row remounts),
// the newest RAW_KEEP entries.
const rawCache = new Map<string, string>();
const RAW_KEEP = 2000;
function remember(k: string, v: string) {
  rawCache.set(k, v);
  if (rawCache.size > RAW_KEEP) rawCache.delete(rawCache.keys().next().value!);
}

// Output is a folded tool result, clipped to CLIP lines until asked.
function Output({ text }: { text: string }) {
  const [all, setAll] = useState(false);
  const ls = text.split("\n");
  if (all || ls.length <= CLIP) return <pre className="te-out">{text}</pre>;
  return (
    <pre className="te-out">{ls.slice(0, CLIP).join("\n") + "\n"}
      <button type="button" className="te-small" onClick={() => setAll(true)}>show all {ls.length} lines</button>
    </pre>
  );
}

// Raw fetches one event's JSON when its line is first shown.
function Raw({ src, row }: { src: ThinkingSource; row: Row }) {
  const k = `${src.key}:${row.key}`;
  const [text, setText] = useState(() => rawCache.get(k) ?? "…");
  useEffect(() => {
    if (rawCache.has(k) || !row.ev) return;
    let live = true;
    src.raw(row.ev.stream, row.ev.seq).then(
      (r) => { const s = r.truncated ? `${r.raw}\n… truncated ${num(r.truncated)} bytes` : r.raw; remember(k, s); if (live) setText(s); },
      (e: unknown) => { if (live) setText(`(raw JSON unavailable: ${e instanceof ApiError ? e.status : "error"})`); });
    return () => { live = false; };
  }, [src, k, row.ev]);
  return <pre className="te-raw">{text}</pre>;
}

function Line({ src, row, format, open, onToggle }: { src: ThinkingSource; row: Row; format: Format; open: boolean; onToggle: () => void }) {
  const e = row.ev!;
  const details = format === "detailed" || (format === "narrative" && open);
  return (
    <div className={`te${row.err ? " err" : ""}${row.empty ? " empty" : ""}`} data-testid="te" data-kind={row.kind} data-seq={e.seq}>
      <div className="te-line" onClick={onToggle}>
        <span className="te-icon">{row.icon}</span>{e.tool && <b className="te-tool">{e.tool}</b>}<span className="te-text">{row.text}</span>
      </div>
      <div className="te-meta">
        <span className="te-time">{clock(e.at)}</span>{row.status && <span className="te-status">{row.status}</span>}
        {e.truncatedBytes > 0 && <span className="te-trunc">✂ {num(e.truncatedBytes)} bytes dropped</span>}
      </div>
      {details && e.detail && <pre className="te-detail">{e.detail}</pre>}
      {details && e.detailTruncated > 0 && <div className="te-note">… {num(e.detailTruncated)} more bytes</div>}
      {details && row.output && <Output text={row.output} />}
      {details && row.outputTruncated > 0 && <div className="te-note">… {num(row.outputTruncated)} more bytes</div>}
      {format === "raw" && <Raw src={src} row={row} />}
    </div>
  );
}

const streamLabel = (firstAt: string, events: number) =>
  `${new Date(firstAt).toLocaleString([], { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", hour12: false })} · ${events} events`;

// ThinkingTab is the session pane's Thinking tab (a lazy chunk): the
// session's events, live, filtered and formatted as the viewer chose.
export default function ThinkingTab({ id, busy, max, backoff }: { id: string; busy: boolean; max?: number; backoff?: number[] }) {
  const pane = usePaneTab(id, "thinking");
  const src = useMemo(() => userThinkingSource(id), [id]);
  if (pane.data?.eventsOff) return <Text size="sm" c="dimmed" p="sm">Session events aren't recorded on this jam.</Text>;
  return <ThinkingView source={src} busy={busy} max={max} backoff={backoff} streams={pane.data?.streams ?? []} />;
}

// ThinkingView renders a source's events. streams, when given, offers the
// stream select (else the source is pinned to one stream by its caller).
export function ThinkingView({ source, busy, max, backoff, streams, onTotals, onStream }: {
  source: ThinkingSource; busy: boolean; max?: number; backoff?: number[]; streams?: ThinkingStream[];
} & FeedCallbacks) {
  const [prefs, setPrefs] = useState(loadPrefs);
  const [q, setQ] = useState("");
  const [open, setOpen] = useState<Set<string>>(() => new Set());
  const list = useRef<HTMLDivElement>(null);
  // follow: the reader is at the end, so new rows keep the end in view. Only
  // the reader turns it off (scrolling up); the list re-pins the end as rows
  // are measured taller than estimated.
  const follow = useRef(true);
  const lastTop = useRef(0);
  // anchor: the row (and its offset in the view) a Load earlier keeps in
  // place while the rows above it arrive and are measured (until: ms).
  const anchor = useRef<{ key: string; offset: number; until: number } | null>(null);
  const t = useThinkingFeed(source, { max, backoff, following: () => follow.current, onTotals, onStream });
  const rows = useMemo(() => deriveRows(t.feed.items, prefs, q), [t.feed.items, prefs, q]);
  const tools = useMemo(() => {
    const x = toolsIn(t.feed.items);
    if (prefs.tool && !x.includes(prefs.tool)) x.push(prefs.tool);
    return x;
  }, [t.feed.items, prefs.tool]);
  // The list: Load earlier, the rows, then the working line and a note.
  const extra = t.feed.more > 0 ? 1 : 0;
  const count = extra + rows.length + (busy ? 1 : 0) + (t.feed.note ? 1 : 0);
  const keyOf = (i: number) => (i < extra ? "more" : i - extra < rows.length ? rows[i - extra]!.key : `tail:${i - extra - rows.length}`);
  // useVirtualizer returns functions the React Compiler can't memoize; this
  // component stays uncompiled (react-hooks' incompatible-library notice).
  // eslint-disable-next-line react-hooks/incompatible-library
  const v = useVirtualizer({ count, getScrollElement: () => list.current, estimateSize: () => 36, overscan: 20,
    initialRect: { width: 600, height: 800 }, getItemKey: keyOf });
  const update = (p: ThinkingPrefs) => { setPrefs(p); savePrefs(p); };

  // Keep the anchored row in place, else follow the end; re-applied on each
  // render (new rows, or rows measured), as sizes settle.
  useLayoutEffect(() => {
    const el = list.current;
    if (!el) return;
    const a = anchor.current;
    if (a && Date.now() > a.until) anchor.current = null;
    if (anchor.current) {
      const i = Array.from({ length: count }, (_, j) => keyOf(j)).indexOf(anchor.current.key);
      const m = i >= 0 ? v.measurementsCache[i] : undefined;
      if (m) el.scrollTop = m.start - anchor.current.offset;
    } else if (follow.current && count > 0) {
      el.scrollTop = el.scrollHeight;
    }
    lastTop.current = el.scrollTop;
  });

  const keepPlace = (fn: () => Promise<void>) => () => {
    const el = list.current;
    const first = el && v.getVirtualItems().find((vi) => vi.index >= extra && vi.end > el.scrollTop);
    if (el && first) anchor.current = { key: String(first.key), offset: first.start - el.scrollTop, until: Date.now() + 1500 };
    follow.current = false;
    void fn().catch(() => { anchor.current = null; });
  };
  // The reader's own scrolling ends an anchor.
  const letGo = () => { anchor.current = null; };
  const toggle = (k: string) => setOpen((s) => {
    const n = new Set(s);
    if (n.has(k)) n.delete(k); else n.add(k);
    return n;
  });
  const item = (i: number) => {
    if (i < extra) return <button type="button" className="te-small te-more" onClick={keepPlace(t.earlier)}>Load earlier</button>;
    const r = rows[i - extra];
    if (!r) {
      if (busy && i - extra === rows.length) return <div className="te-working">working… <Loader type="dots" size="xs" aria-hidden /></div>;
      return <div className="te-gone">{t.feed.note}</div>;
    }
    if (r.marker === "restart") return <div className="te-div">— session restarted —</div>;
    if (r.marker === "skip") {
      return <div className="te-skip">⚠ events {r.skip!.from}-{r.skip!.to} skipped —{" "}
        <button type="button" className="te-small" onClick={keepPlace(() => t.fill(r.key))}>Load earlier</button></div>;
    }
    return <Line src={source} row={r} format={prefs.format} open={open.has(r.key)} onToggle={() => toggle(r.key)} />;
  };
  return (
    <div className="th" data-thinking>
      <div className="th-bar">
        <span role="group" aria-label="Show" style={{ display: "inline-flex", gap: 4, flexWrap: "wrap" }}>
          {CHIPS.map(([k, label]) => (
            <button key={k} type="button" data-f={k} aria-pressed={prefs.f[k]} onClick={() => update({ ...prefs, f: { ...prefs.f, [k]: !prefs.f[k] } })}>{label}</button>
          ))}
        </span>
        <NativeSelect size="xs" aria-label="Tool" value={prefs.tool} onChange={(e) => update({ ...prefs, tool: e.currentTarget.value })}
          data={[{ value: "", label: "All tools" }, ...tools.map((x) => ({ value: x, label: x }))]} />
        <TextInput size="xs" type="search" placeholder="Search" aria-label="Search events" value={q} onChange={(e) => setQ(e.currentTarget.value)}
          style={{ flex: 1, minWidth: 90 }} />
        <NativeSelect size="xs" aria-label="Format" value={prefs.format} onChange={(e) => update({ ...prefs, format: e.currentTarget.value as Format })}
          data={FORMATS.map((f) => ({ value: f, label: f[0]!.toUpperCase() + f.slice(1) }))} />
        {streams && <NativeSelect size="xs" aria-label="Stream" value={t.pinned} onChange={(e) => t.connect(e.currentTarget.value)}
          data={[{ value: "", label: "Current" }, ...streams.map((s) => ({ value: s.id, label: streamLabel(s.firstAt, s.events) }))]} />}
      </div>
      <div ref={list} className="th-list" role="log" aria-live="polite" aria-label="Session events"
        onWheel={letGo} onTouchStart={letGo} onKeyDown={letGo}
        onScroll={(e) => {
          const el = e.currentTarget;
          if (el.scrollHeight - el.scrollTop - el.clientHeight < 16) {
            follow.current = true;
            t.seen();
          } else if (el.scrollTop < lastTop.current - 1 && !anchor.current) {
            follow.current = false; // the reader scrolled up
          }
          lastTop.current = el.scrollTop;
        }}>
        <div style={{ height: v.getTotalSize(), position: "relative" }}>
          {v.getVirtualItems().map((vi) => (
            <div key={vi.key} data-index={vi.index} ref={v.measureElement}
              style={{ position: "absolute", top: 0, left: 0, width: "100%", transform: `translateY(${vi.start}px)` }}>
              {item(vi.index)}
            </div>
          ))}
        </div>
      </div>
      {t.feed.unseen > 0 && (
        <button type="button" className="th-new" aria-label="Show new events"
          onClick={() => {
            if (t.feed.paused) { t.connect(t.pinned); return; }
            follow.current = true;
            anchor.current = null;
            const el = list.current;
            if (el) el.scrollTop = el.scrollHeight;
            t.seen();
          }}>↓ <span>{t.feed.unseen}</span> new</button>
      )}
    </div>
  );
}
