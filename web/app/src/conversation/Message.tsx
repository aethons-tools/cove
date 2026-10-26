import { Anchor, Group, Image, Paper, Text, UnstyledButton } from "@mantine/core";
import { useRef } from "react";
import type { AskCard, MessageRow } from "../api/types";
import { AskCardView } from "../asks/AskCard";
import { MessageBody } from "./MessageBody";
import { copyText } from "./copy";
import { timeOf } from "./days";

const CARD_NAME: Record<string, string> = { ask: "ask", board: "whiteboard" };

// Message is one row: a system line, a card placeholder, or a bubble.
export function Message({ m, channel, onDiscuss }: { m: MessageRow; channel: string; onDiscuss?: (c: AskCard) => void }) {
  const bodyRef = useRef<HTMLDivElement>(null);
  if (m.system) {
    return <Text data-seq={m.seq} size="xs" c="dimmed" ta="center" my={4}>{m.sysText} <Text span inherit>· {timeOf(m.time)}</Text></Text>;
  }
  if (m.card === "ask" && m.ask) {
    return <div data-seq={m.seq}><AskCardView card={m.ask} channel={channel} onDiscuss={onDiscuss} /></div>;
  }
  if (m.card) {
    return (
      <Paper data-seq={m.seq} withBorder p="xs" my={4} maw="80%">
        <Text size="sm">This {CARD_NAME[m.card] ?? m.card} shows in the current Inbox for now.{" "}
          <Anchor href={`/app/user/?c=${encodeURIComponent(channel)}`}>Open it in the current Inbox</Anchor>
        </Text>
      </Paper>
    );
  }
  const textOf = () => (bodyRef.current?.querySelector(".jam-body") as HTMLElement | null)?.innerText.replace(/\n+$/, "") ?? "";
  return (
    <Paper data-seq={m.seq} p="xs" my={4} maw="80%" ml={m.mine ? "auto" : 0} bg={m.mine ? "var(--mantine-primary-color-light)" : "var(--mantine-color-default-hover)"}>
      <Group justify="space-between" gap="xs" wrap="nowrap">
        <Text size="xs" fw={600}>{m.mine ? "you" : m.from}</Text>
        <Group gap={6} wrap="nowrap">
          <UnstyledButton fz="xs" c="dimmed" title="Copy the message as text" onClick={() => void copyText(textOf())}>Text</UnstyledButton>
          <UnstyledButton fz="xs" c="dimmed" title="Copy the message as markdown, exactly as sent" onClick={() => void copyText(m.raw)}>Markdown</UnstyledButton>
          <Text size="xs" c="dimmed">{timeOf(m.time)}</Text>
        </Group>
      </Group>
      {m.reAsk && <Anchor size="xs" href={`/app/user/?c=${encodeURIComponent(channel)}#ask-${m.reAsk.id}`}>re: {m.reAsk.question}</Anchor>}
      <div ref={bodyRef}>{m.body && <MessageBody html={m.body} plain={m.plain} />}</div>
      {m.files && (
        <Group gap="xs" mt={4}>
          {m.files.map((f) => f.image
            ? <Anchor key={f.id} href={`/app/user/blobs/${f.id}`} target="_blank" rel="noopener"><Image src={`/app/user/blobs/${f.id}`} alt={f.name} h={120} w="auto" radius="sm" loading="lazy" /></Anchor>
            : <Anchor key={f.id} href={`/app/user/blobs/${f.id}`} download size="sm">📄 {f.name} <Text span size="xs" c="dimmed">{f.size}</Text></Anchor>)}
        </Group>
      )}
    </Paper>
  );
}
