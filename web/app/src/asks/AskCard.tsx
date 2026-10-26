import { Badge, Button, Group, Paper, Stack, Text, Textarea } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { apiPost, ApiError } from "../api/client";
import type { AnswerRequest, AskCard } from "../api/types";

// AskCardView is an ask where it was asked, in its current state: open
// (options to answer, or read-only "for …"), answered, or withdrawn. Its
// comment box is local state, so a live refresh never wipes it.
export function AskCardView({ card, channel, onDiscuss }: { card: AskCard; channel: string; onDiscuss?: (c: AskCard) => void }) {
  const client = useQueryClient();
  const [comment, setComment] = useState("");
  const answer = useMutation<void, ApiError, AnswerRequest>({
    mutationFn: (req) => apiPost(`asks/${encodeURIComponent(card.id)}/answer`, req),
    onSuccess: () => setComment(""),
    onError: (e) => notifications.show({ color: e.code === "ask_changed" || e.code === "ask_closed" ? "yellow" : "red", message: e.message }),
    // Either way the card's state moved on (answered, changed, closed): refetch
    // it, and the rail (its Needs you), as the old route's meChanged did.
    onSettled: () => {
      void client.invalidateQueries({ queryKey: ["conversation", channel] });
      void client.invalidateQueries({ queryKey: ["rail"] });
    },
  });
  const choose = (choice: string) => {
    if (choice === "other" && !comment.trim()) {
      notifications.show({ color: "yellow", message: "Write a comment for Other…" });
      return;
    }
    answer.mutate({ version: card.version, choice, ...(comment.trim() ? { comment: comment.trim() } : {}) });
  };
  return (
    <Paper id={`ask-${card.id}`} withBorder p="sm" my={6} maw="85%" style={{ borderLeft: `3px solid var(--mantine-color-${card.state === "open" ? "yellow" : "gray"}-6)` }}>
      <Group gap={6} mb={6}>
        <Text fw={600}>{card.question}</Text>
        {card.edited && <Badge size="xs" variant="light" color="gray">edited</Badge>}
        {card.for && <Text size="xs" c="dimmed">for {card.for}</Text>}
      </Group>
      {card.state === "answered" && (
        <Text size="sm">✓ {card.answeredBy} chose <b>{card.choice}</b>{card.comment && <> — {card.comment}</>}</Text>
      )}
      {card.state === "withdrawn" && <Text size="sm" c="dimmed">withdrawn{card.reason && `: ${card.reason}`}</Text>}
      {card.state === "open" && card.canAnswer && (
        <Stack gap={6}>
          <Group gap={6}>
            {card.options.map((o) => (
              <Button key={o.id} variant="default" size="xs" loading={answer.isPending && answer.variables?.choice === o.id} disabled={answer.isPending && answer.variables?.choice !== o.id} onClick={() => choose(o.id)}
                styles={{ label: { flexDirection: "column", alignItems: "flex-start" } }} h="auto" py={4}>
                <span>{o.label}</span>
                {o.detail && <Text span size="xs" c="dimmed">{o.detail}</Text>}
              </Button>
            ))}
            <Button variant="subtle" size="xs" loading={answer.isPending && answer.variables?.choice === "other"} disabled={answer.isPending && answer.variables?.choice !== "other"} onClick={() => choose("other")}>Other…</Button>
          </Group>
          <Textarea size="xs" autosize minRows={1} maxRows={4} aria-label={`Comment on ${card.question}`}
            placeholder="Comment (needed for Other…)" value={comment} onChange={(e) => setComment(e.currentTarget.value)} />
        </Stack>
      )}
      {card.state === "open" && !card.canAnswer && (
        <Group gap={6}>{card.options.map((o) => <Badge key={o.id} variant="outline" color="gray">{o.label}</Badge>)}</Group>
      )}
      {card.state === "open" && onDiscuss && (
        <Button variant="subtle" size="compact-xs" mt={6} onClick={() => onDiscuss(card)}>Discuss</Button>
      )}
    </Paper>
  );
}
