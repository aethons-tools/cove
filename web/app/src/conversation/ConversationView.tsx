import { Loader, Stack } from "@mantine/core";
import { useQueryClient } from "@tanstack/react-query";
import { useEffect } from "react";
import { apiPost } from "../api/client";
import { Problem } from "../Problem";
import { ConversationHeader } from "./ConversationHeader";
import { MessageList } from "./MessageList";
import { PresenceStrip } from "./PresenceStrip";
import { messagesOf, useConversation } from "./useConversation";
import { useLocation } from "react-router";

// ConversationView is one open conversation: header, messages, presence. It
// marks the conversation read up to its newest message while the page is
// visible (History has no cursor: never).
export function ConversationView({ id }: { id: string }) {
  const q = useConversation(id);
  const client = useQueryClient();
  const here = useLocation();
  const head = q.data?.pages[0];
  const lastSeq = head?.lastSeq ?? 0;
  const canRead = !!head?.sendTo;

  useEffect(() => {
    if (!canRead || lastSeq <= 0) return;
    const send = () => {
      if (document.visibilityState !== "visible") return;
      void apiPost(`conversations/${encodeURIComponent(id)}/read`, { seq: lastSeq })
        .then(() => client.invalidateQueries({ queryKey: ["rail"] }))
        .catch(() => {}); // a failed mark-read is retried on the next change
    };
    send();
    document.addEventListener("visibilitychange", send);
    return () => document.removeEventListener("visibilitychange", send);
  }, [id, lastSeq, canRead, client]);

  if (q.isPending) return <Loader m="md" size="sm" />;
  if (q.error) return <Problem error={q.error} here={here.pathname + here.search} />;
  return (
    <Stack gap={0} h="calc(100dvh - var(--app-shell-header-height) - 2 * var(--app-shell-padding))">
      <ConversationHeader c={q.data.pages[0]!} />
      <MessageList messages={messagesOf(q.data.pages)} channel={id} earlier={q.hasNextPage}
        loadingEarlier={q.isFetchingNextPage} onEarlier={() => void q.fetchNextPage()} />
      <PresenceStrip channel={id} />
    </Stack>
  );
}
