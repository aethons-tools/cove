import { Group, Loader, Text } from "@mantine/core";
import { useQuery } from "@tanstack/react-query";
import { apiGet, POLL_MS, type ApiError } from "../api/client";
import type { PresenceResponse } from "../api/types";

// PresenceStrip shows each session in the conversation and what it's doing
// (status and tool name only, never event content).
export function PresenceStrip({ channel }: { channel: string }) {
  const { data } = useQuery<PresenceResponse, ApiError>({
    queryKey: ["presence", channel],
    queryFn: () => apiGet<PresenceResponse>(`conversations/${encodeURIComponent(channel)}/presence`),
    refetchInterval: POLL_MS,
  });
  if (!data || data.sessions.length === 0) return null;
  return (
    <Group gap="md" px="sm" py={4}>
      {data.sessions.map((s) => (
        <Group key={s.label} gap={4} wrap="nowrap">
          <Text size="xs" fw={600} c={s.class === "wait" ? "yellow.8" : undefined}>{s.label}</Text>
          <Text size="xs" c={s.class === "dim" ? "dimmed" : undefined}>{s.text}{s.tool && <> <b>{s.tool}</b></>}</Text>
          {s.class === "busy" && <Loader type="dots" size="xs" aria-hidden />}
        </Group>
      ))}
    </Group>
  );
}
