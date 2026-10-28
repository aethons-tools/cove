import { Badge } from "@mantine/core";

// tone is an agent status's colour.
const tone: Record<string, string> = {
  running: "green", waiting: "blue", holding: "blue", orienting: "blue",
  "setting up": "yellow", blocked: "red", lost: "red",
};

// StatusBadge is an agent's display status (jam.StatusOf).
export function StatusBadge({ status }: { status: string }) {
  const c = tone[status] ?? "gray";
  return <Badge size="sm" variant="light" color={c} data-tone={c}>{status}</Badge>;
}
