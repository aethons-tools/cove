import { Anchor, Group, Loader, NativeSelect, Stack, Text, Title } from "@mantine/core";
import { useQuery } from "@tanstack/react-query";
import { lazy, Suspense, useMemo, useState } from "react";
import { Link, useLocation, useParams, useSearchParams } from "react-router";
import type { ApiError } from "../../api/client";
import { Problem } from "../../Problem";
import { num, usd } from "../../session/thinking/format";
import { adminSessionSource } from "../../session/thinking/source";
import type { SessionInfo, SessionTotals } from "../api/types";
import { adminApi, nodeHref } from "../paths";

// The Thinking components are their own chunk, as in the user area.
const ThinkingView = lazy(() => import("../../session/thinking/ThinkingTab").then((m) => ({ default: m.ThinkingView })));
const enc = encodeURIComponent;

// TimelinePage is an agent's session timeline: the stream shown (?stream=,
// else the newest; with none yet, the first to start), its events and its
// running totals, live, through the user area's Thinking view.
export function TimelinePage() {
  const { id = "" } = useParams();
  const here = useLocation();
  const [params, setParams] = useSearchParams();
  const asked = params.get("stream") ?? "";
  const { data, error, isPending, refetch } = useQuery<SessionInfo, ApiError>({
    queryKey: ["admin", "session", id, asked],
    queryFn: () => adminApi.get(`agents/${enc(id)}/session${asked ? `?stream=${enc(asked)}` : ""}`),
  });
  // The server's choice: ?stream= when it names a stream, else the newest.
  const stream = data?.stream ?? "";
  const source = useMemo(() => adminSessionSource(id, stream), [id, stream]);
  const [totals, setTotals] = useState<{ key: string; t: SessionTotals } | null>(null);
  const t = totals?.key === source.key ? totals.t : null;
  if (isPending) return <Loader size="sm" />;
  if (error) return <Problem error={error} here={here.pathname} />;
  const back = <Anchor component={Link} to={nodeHref(`agents/${enc(id)}`)}>Back to {id}</Anchor>;
  if (!data.enabled) return <Stack gap="xs"><Title order={2} ff="monospace">{id}</Title><Text size="sm">Session events are not configured on this Jam.</Text>{back}</Stack>;
  return (
    <Stack h="calc(100dvh - 120px)" gap="xs">
      <Group justify="space-between" wrap="wrap">
        <Group gap="sm" align="baseline">
          <Title order={2} ff="monospace">{id}</Title>
          {t && <Text size="sm" c="dimmed">
            {num(t.turns)} turns · {num(t.episodes)} episodes · {num(t.toolCalls)} tool calls · {num(t.inputTokens)} in / {num(t.outputTokens)} out · {usd(t.costUsd)}
          </Text>}
        </Group>
        <Group gap="xs">
          <NativeSelect size="xs" aria-label="Stream" value={stream} onChange={(e) => setParams({ stream: e.currentTarget.value })}
            data={data.streams.length ? data.streams.map((s) => ({ value: s.streamId, label: `${s.started} · ${num(s.events)} events` })) : [{ value: "", label: "No streams yet" }]} />
          {back}
        </Group>
      </Group>
      {!stream && <Text size="sm" c="dimmed">No session captured yet — following the first one to start.</Text>}
      <Suspense fallback={<Loader size="sm" />}>
        <ThinkingView key={source.key} source={source} busy={false}
          onTotals={(x) => setTotals({ key: source.key, t: x as SessionTotals })}
          onStream={() => { if (!stream) void refetch(); }} />
      </Suspense>
    </Stack>
  );
}
