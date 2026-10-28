import { Badge } from "@mantine/core";
import type { AttnFlag as Flag } from "../api/types";

// AttnFlag marks a row that needs attention: red when broken, else amber;
// its title says why.
export function AttnFlag({ flag }: { flag?: Flag | null }) {
  if (!flag) return null;
  return <Badge size="xs" circle color={flag.kind === "broken" ? "red" : "yellow"} title={flag.why} aria-label={flag.why}>!</Badge>;
}
