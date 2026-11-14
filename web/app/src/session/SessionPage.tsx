import { Button, Group, Loader, Paper, Stack, Text } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useLocation, useSearchParams } from "react-router";
import { apiPost, type ApiError } from "../api/client";
import type { ConversationRef } from "../api/types";
import { useBoot } from "../BootContext";
import { Composer } from "../composer/Composer";
import { Problem } from "../Problem";
import { ActivityList } from "./ActivityList";
import { SessionView } from "./SessionView";
import { entriesOf, limitedOf, useActivity } from "./useActivity";

// SessionPage is a session's view (/app/user/s/<id>): its header, its
// activity in every conversation with its thinking folded between, and a
// composer that DMs it, beside the session pane. Once the session ends, its
// header and pane go (its head 404s) and the activity stays.
export function SessionPage({ id }: { id: string }) {
  const q = useActivity(id);
  const here = useLocation();
  const [search] = useSearchParams();
  const boot = useBoot();
  const [stick, setStick] = useState(0);
  if (q.isPending) return <Loader m="md" size="sm" />;
  if (!q.data) return <Problem error={q.error!} here={here.pathname + here.search} />;
  const a = q.data.pages[0]!;
  const jumpToAsk = (ask: string) => document.getElementById(`ask-${ask}`)?.scrollIntoView({ behavior: "smooth", block: "center" });
  return (
    <Stack gap={0} h="calc(100dvh - var(--app-shell-header-height, 0px) - 2 * var(--app-shell-padding, 0px))">
      <SessionView id={id} linked={search.get("tab")} onAsk={jumpToAsk} header={null}>
        {q.error && <Problem error={q.error} here={here.pathname + here.search} />}
        <ActivityList id={id} entries={entriesOf(q.data.pages)} earlier={q.hasNextPage} loadingEarlier={q.isFetchingNextPage}
          onEarlier={() => void q.fetchNextPage()} stickToEnd={stick} />
        {limitedOf(q.data.pages).map((c) => <Limited key={c.id} id={id} c={c} />)}
        {a.sendTo
          ? <Composer key={a.sendTo} to={a.sendTo} label={a.label} attachMax={boot.attachMax} accent={a.color || undefined}
              onSent={() => setStick((n) => n + 1)} />
          : <Text size="sm" c="dimmed" p="xs">This session has ended: its activity stays.</Text>}
      </SessionView>
    </Stack>
  );
}

// Limited says agents stopped waking each other in conversation c, with
// Resume to lift the limit (conversations.adoc#rate-limit).
function Limited({ id, c }: { id: string; c: ConversationRef }) {
  const client = useQueryClient();
  const resume = useMutation<unknown, ApiError>({
    mutationFn: () => apiPost(`conversations/${encodeURIComponent(c.id)}/resume`, {}),
    onSuccess: () => client.invalidateQueries({ queryKey: ["session", id, "activity"] }),
    onError: (e) => notifications.show({ color: "red", message: e.message }),
  });
  const label = `Agents stopped waking each other in ${c.label}`;
  return (
    <Paper role="status" aria-label={label} withBorder p="xs" mx="xs" my={4}>
      <Group justify="space-between" gap="xs">
        <Text size="sm">{label}: their messages there wake no agent for now.</Text>
        <Button size="compact-xs" variant="default" loading={resume.isPending} onClick={() => resume.mutate()}>Resume</Button>
      </Group>
    </Paper>
  );
}
