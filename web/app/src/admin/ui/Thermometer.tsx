import { Group, Progress, Stack, Text } from "@mantine/core";
import type { ReactNode } from "react";

export type ThermometerState = "ok" | "warning" | "over";

// thermometerState is ok below warnAt of the limit, warning from there to the
// limit, over past it. A zero limit is ok while nothing is used.
export function thermometerState(used: number, limit: number, warnAt = 0.85): ThermometerState {
  if (used > limit) return "over";
  if (limit <= 0) return "ok";
  return used >= limit * warnAt ? "warning" : "ok";
}

const COLOR: Record<ThermometerState, string> = { ok: "green", warning: "orange", over: "red" };

// Thermometer is the thin bar under a length-limited field or list: used
// against limit ("312 / 1,200 bytes"), amber near the limit, red over it.
// label is the note at its left (a hint, or the problem when over).
export function Thermometer({ used, limit, unit = "bytes", label, warnAt }: {
  used: number; limit: number; unit?: string; label?: ReactNode; warnAt?: number;
}) {
  const state = thermometerState(used, limit, warnAt);
  const count = `${used.toLocaleString("en-US")} / ${limit.toLocaleString("en-US")} ${unit}`;
  return (
    <Stack gap={3} mt={6} data-state={state}>
      <Progress value={limit > 0 ? Math.min(100, (used / limit) * 100) : used > 0 ? 100 : 0} color={COLOR[state]} size={6} aria-label={count} />
      <Group justify="space-between" gap="xs" wrap="nowrap">
        <Text size="xs" c={state === "over" ? "red" : "dimmed"} component="div">{label}</Text>
        <Text size="xs" c={state === "over" ? "red" : "dimmed"} fw={state === "over" ? 600 : undefined} style={{ whiteSpace: "nowrap" }}>{count}</Text>
      </Group>
    </Stack>
  );
}
