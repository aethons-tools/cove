import { Loader, Stack, Text } from "@mantine/core";
import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { apiPost } from "../api/client";
import { Problem } from "../Problem";
import { ConversationHeader } from "./ConversationHeader";
import { MessageList } from "./MessageList";
import { PresenceStrip } from "./PresenceStrip";
import { messagesOf, useConversation } from "./useConversation";
import { useLocation, useSearchParams } from "react-router";
import { useBoot } from "../BootContext";
import { Composer, type ComposerHandle } from "../composer/Composer";
import { OpenAsks } from "../asks/OpenAsks";
import { SessionView } from "../session/SessionView";
import { useFocusReport } from "../push/useFocusReport";

// ConversationView is one open conversation: header, messages, presence. It
// marks the conversation read up to its newest message while the page is
// visible (History has no cursor: never).
export function ConversationView({ id, withSession = true }: { id: string; withSession?: boolean }) {
  const q = useConversation(id);
  const client = useQueryClient();
  const here = useLocation();
  const [search] = useSearchParams();
  const boot = useBoot();
  const [stick, setStick] = useState(0);
  const composer = useRef<ComposerHandle>(null);
  const head = q.data?.pages[0];
  const lastSeq = head?.lastSeq ?? 0;
  const canRead = !!head?.sendTo;
  // With Web Push, the conversation you are reading isn't pushed to you.
  useFocusReport(canRead ? id : null, boot.pushFocus);

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
  const c = q.data.pages[0]!;
  const jumpToAsk = (a: string) => document.getElementById(`ask-${a}`)?.scrollIntoView({ behavior: "smooth", block: "center" });
  const main = (
    <>
      <OpenAsks asks={c.openAsks} onJump={jumpToAsk} />
      {/* A failed refresh keeps what's loaded (and the reader's place) on screen. */}
      {q.error && <Problem error={q.error} here={here.pathname + here.search} />}
      <MessageList messages={messagesOf(q.data.pages)} channel={id} earlier={q.hasNextPage}
        loadingEarlier={q.isFetchingNextPage} onEarlier={() => void q.fetchNextPage()} stickToEnd={stick}
        onDiscuss={c.sendTo ? (card) => composer.current?.discuss(card.id, card.question) : undefined} />
      <PresenceStrip channel={id} />
      {c.sendTo
        ? <Composer ref={composer} key={c.sendTo} to={c.sendTo} label={c.label} attachMax={boot.attachMax} accent={c.color || undefined}
            onSent={() => setStick((n) => n + 1)} />
        : <Text size="sm" c="dimmed" p="xs">From before the upgrade: read-only.</Text>}
    </>
  );
  return (
    <Stack gap={0} h="calc(100dvh - var(--app-shell-header-height, 0px) - 2 * var(--app-shell-padding, 0px))">
      {withSession
        ? <SessionView id={c.session || null} linked={search.get("tab")} onAsk={jumpToAsk}
            header={<ConversationHeader c={c} />}>{main}</SessionView>
        : <><ConversationHeader c={c} />{main}</>}
    </Stack>
  );
}
