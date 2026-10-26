import { Button, Center, Divider, ScrollArea, Text } from "@mantine/core";
import { Fragment, useEffect, useLayoutEffect, useRef } from "react";
import type { MessageRow } from "../api/types";
import { Message } from "./Message";
import { dayLabel, sameDay } from "./days";

// MessageList shows the messages oldest-first with day dividers. It opens
// at the newest, stays pinned there while new ones arrive (unless the reader
// scrolled up), and keeps position when older ones load above.
export function MessageList({ messages, channel, earlier, loadingEarlier, onEarlier }: {
  messages: MessageRow[]; channel: string; earlier: boolean; loadingEarlier: boolean; onEarlier: () => void;
}) {
  const viewport = useRef<HTMLDivElement>(null);
  const pinned = useRef(true);
  const prev = useRef<{ first?: number; height: number }>({ height: 0 });

  useLayoutEffect(() => {
    const el = viewport.current;
    if (!el) return;
    const first = messages[0]?.seq;
    if (prev.current.first !== undefined && first !== undefined && first < prev.current.first) {
      el.scrollTop += el.scrollHeight - prev.current.height; // older loaded above: keep the view
    } else if (pinned.current) {
      el.scrollTop = el.scrollHeight;
    }
    prev.current = { first, height: el.scrollHeight };
  }, [messages]);

  useEffect(() => { pinned.current = true; }, [channel]);

  if (messages.length === 0) return <Center flex={1}><Text c="dimmed">No messages yet.</Text></Center>;
  return (
    <ScrollArea flex={1} viewportRef={viewport} onScrollPositionChange={() => {
      const el = viewport.current;
      if (el) pinned.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
    }}>
      {earlier && <Center my="xs"><Button variant="subtle" size="xs" loading={loadingEarlier} onClick={onEarlier}>Load earlier</Button></Center>}
      {messages.map((m, i) => (
        <Fragment key={m.seq}>
          {(i === 0 || !sameDay(messages[i - 1]!.time, m.time)) && <Divider my="xs" label={dayLabel(m.time)} labelPosition="center" />}
          <Message m={m} channel={channel} />
        </Fragment>
      ))}
    </ScrollArea>
  );
}
