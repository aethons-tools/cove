import { Loader, Stack, Text } from "@mantine/core";
import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { apiPost } from "../api/client";
import { Problem } from "../Problem";
import { ConversationHeader } from "./ConversationHeader";
import { MessageList } from "./MessageList";
import { PresenceStrip } from "./PresenceStrip";
import { messagesOf, useConversation } from "./useConversation";
import { useLocation } from "react-router";
import { useBoot } from "../BootContext";
import { Composer, type ComposerHandle } from "../composer/Composer";
import { OpenAsks } from "../asks/OpenAsks";

// ConversationView is one open conversation: header, messages, presence. It
// marks the conversation read up to its newest message while the page is
// visible (History has no cursor: never).
export function ConversationView({ id }: { id: string }) {
  const q = useConversation(id);
  const client = useQueryClient();
  const here = useLocation();
  const boot = useBoot();
  const [stick, setStick] = useState(0);
  const composer = useRef<ComposerHandle>(null);
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
  if (!q.data) return <Problem error={q.error!} here={here.pathname + here.search} />;
  return (
    <Stack gap={0} h="calc(100dvh - var(--app-shell-header-height) - 2 * var(--app-shell-padding))">
      <ConversationHeader c={q.data.pages[0]!} />
      <OpenAsks asks={q.data.pages[0]!.openAsks} onJump={(id) => document.getElementById(`ask-${id}`)?.scrollIntoView({ behavior: "smooth", block: "center" })} />
      {/* A failed refresh keeps what's loaded (and the reader's place) on screen. */}
      {q.error && <Problem error={q.error} here={here.pathname + here.search} />}
      <MessageList messages={messagesOf(q.data.pages)} channel={id} earlier={q.hasNextPage}
        loadingEarlier={q.isFetchingNextPage} onEarlier={() => void q.fetchNextPage()} stickToEnd={stick}
        onDiscuss={q.data.pages[0]!.sendTo ? (c) => composer.current?.discuss(c.id, c.question) : undefined} />
      <PresenceStrip channel={id} />
      {q.data.pages[0]!.sendTo
        ? <Composer ref={composer} key={q.data.pages[0]!.sendTo} to={q.data.pages[0]!.sendTo} label={q.data.pages[0]!.label} attachMax={boot.attachMax}
            onSent={() => setStick((n) => n + 1)} />
        : <Text size="sm" c="dimmed" p="xs">From before the upgrade: read-only.</Text>}
    </Stack>
  );
}
