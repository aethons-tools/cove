import { Anchor, Group, Loader, NativeSelect, Stack, Text, Title } from "@mantine/core";
import { useQuery } from "@tanstack/react-query";
import { lazy, Suspense, useMemo } from "react";
import { Link, useLocation, useParams, useSearchParams } from "react-router";
import type { ApiError } from "../../api/client";
import { Problem } from "../../Problem";
import { num, usd } from "../../session/thinking/format";
import { adminSessionSource } from "../../session/thinking/source";
import type { SessionInfo } from "../api/types";
import { adminApi, nodeHref } from "../paths";

// The Thinking components are their own chunk, as in the user area.
const ThinkingView = lazy(() => import("../../session/thinking/ThinkingTab").then((m) => ({ default: m.ThinkingView })));
const enc = encodeURIComponent;

// TimelinePage is an agent's session timeline: its totals, the stream
// shown (?stream=, else the newest) and that stream's events, live,
// through the user area's Thinking view.
export function TimelinePage() {
  const { id = "" } = useParams();
  const here = useLocation();
  const [params, setParams] = useSearchParams();
  const asked = params.get("stream") ?? "";
  const { data, error, isPending } = useQuery<SessionInfo, ApiError>({
    queryKey: ["admin", "session", id, asked],
    queryFn: () => adminApi.get(`agents/${enc(id)}/session${asked ? `?stream=${enc(asked)}` : ""}`),
  });
  const stream = asked || data?.stream || "";
  const source = useMemo(() => adminSessionSource(id, stream), [id, stream]);
  if (isPending) return <Loader size="sm" />;
  if (error) return <Problem error={error} here={here.pathname} />;
  const back = <Anchor component={Link} to={nodeHref(`agents/${enc(id)}`)}>Back to {id}</Anchor>;
  if (!data.enabled) return <Stack gap="xs"><Title order={2} ff="monospace">{id}</Title><Text size="sm">Session events are not configured on this Jam.</Text>{back}</Stack>;
  const t = data.totals;
  return (
    <Stack h="calc(100dvh - 120px)" gap="xs">
      <Group justify="space-between" wrap="wrap">
        <Group gap="sm" align="baseline">
          <Title order={2} ff="monospace">{id}</Title>
          <Text size="sm" c="dimmed">
            {num(t.turns)} turns · {num(t.episodes)} episodes · {num(t.toolCalls)} tool calls · {num(t.inputTokens)} in / {num(t.outputTokens)} out · {usd(t.costUsd)}
          </Text>
        </Group>
        <Group gap="xs">
          <NativeSelect size="xs" aria-label="Stream" value={stream} onChange={(e) => setParams({ stream: e.currentTarget.value })}
            data={data.streams.length ? data.streams.map((s) => ({ value: s.streamId, label: `${s.started} · ${num(s.events)} events` })) : [{ value: "", label: "No streams" }]} />
          {back}
        </Group>
      </Group>
      {stream ? (
        <Suspense fallback={<Loader size="sm" />}>
          <ThinkingView key={source.key} source={source} busy={false} />
        </Suspense>
      ) : <Text size="sm" c="dimmed">No session captured yet.</Text>}
    </Stack>
  );
}
