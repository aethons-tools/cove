import { Anchor, Button, Center, Divider, ScrollArea, Text } from "@mantine/core";
import { Fragment, useEffect, useLayoutEffect, useRef } from "react";
import { Link } from "react-router";
import type { ActivityRow } from "../api/types";
import { Message } from "../conversation/Message";
import { dayLabel, sameDay } from "../conversation/days";
import { APP } from "../paths";
import { TurnFold } from "./TurnFold";
import type { Entry } from "./useActivity";

// ActivityList is a session's activity, oldest first, with day dividers:
// each message under the conversation it's in (a link to it), and each
// turn's thinking folded between them. Like MessageList, it opens at the
// newest, stays pinned there while new entries arrive (unless the reader
// scrolled up), and keeps its place when older ones load above.
export function ActivityList({ id, entries, earlier, loadingEarlier, onEarlier, stickToEnd }: {
  id: string; entries: Entry[]; earlier: boolean; loadingEarlier: boolean; onEarlier: () => void; stickToEnd?: number;
}) {
  const viewport = useRef<HTMLDivElement>(null);
  const pinned = useRef(true);
  const prev = useRef<{ first?: string; height: number }>({ height: 0 });
  const empty = !entries.some((e) => e.t === "msg");
  // Each entry's previous message, for the day dividers.
  const prevMsg = entries.reduce<{ out: (ActivityRow | undefined)[]; last?: ActivityRow }>((acc, e) => {
    acc.out.push(acc.last);
    return e.t === "msg" ? { out: acc.out, last: e.m } : acc;
  }, { out: [] }).out;

  useLayoutEffect(() => {
    const el = viewport.current;
    if (!el) return;
    const first = entries[0]?.key;
    if (prev.current.first !== undefined && first !== undefined && first !== prev.current.first) {
      el.scrollTop += el.scrollHeight - prev.current.height; // older loaded above: keep the view
    } else if (pinned.current) {
      el.scrollTop = el.scrollHeight;
    }
    prev.current = { first, height: el.scrollHeight };
  }, [entries]);

  useEffect(() => { pinned.current = true; }, [id]);
  useEffect(() => { pinned.current = true; }, [stickToEnd]); // a send ends at the bottom

  return (
    <ScrollArea flex={1} viewportRef={viewport} viewportProps={{ tabIndex: -1, style: { outline: "none" } }} onScrollPositionChange={() => {
      const el = viewport.current;
      if (el) pinned.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
    }}>
      <div role="log" aria-label="Activity" style={{ padding: "0 8px" }}>
        {earlier && <Center my="xs"><Button variant="subtle" size="xs" loading={loadingEarlier} onClick={onEarlier}>Load earlier</Button></Center>}
        {empty && !earlier && <Center my="md"><Text c="dimmed">No messages yet.</Text></Center>}
        {entries.map((e, i) => {
          if (e.t === "turn") return <div key={e.key} data-activity={e.key}><TurnFold id={id} f={e.f} /></div>;
          const m = e.m, before = prevMsg[i];
          return (
            <Fragment key={e.key}>
              {(!before || !sameDay(before.time, m.time)) && <Divider my="xs" label={dayLabel(m.time)} labelPosition="center" />}
              <div data-activity={e.key}>
                <Text size="xs" c="dimmed" ta={m.mine ? "right" : "left"} mt={6}>
                  in <Anchor component={Link} to={`${APP}c/${encodeURIComponent(m.conversation.id)}`} size="xs">{m.conversation.label}</Anchor>
                </Text>
                <Message m={m} channel={m.conversation.id} />
              </div>
            </Fragment>
          );
        })}
      </div>
    </ScrollArea>
  );
}
