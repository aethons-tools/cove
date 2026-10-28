import { Badge } from "@mantine/core";
import { Link } from "react-router";
import type { Managed } from "../api/types";
import { nodeHref } from "../paths";

// ManagedBadge marks an object the tracked config repo manages; it links
// Tracking. Nothing for an unmanaged one.
export function ManagedBadge({ managed }: { managed?: Managed | null }) {
  if (!managed) return null;
  return (
    <Badge component={Link} to={nodeHref("config/tracking")} variant="light" color="gray" style={{ cursor: "pointer" }}
      title={`Applied from the config repo ${managed.applied}; edits here drift from it`}>
      managed · config @{managed.commit}
    </Badge>
  );
}

// driftNote is what a write to a managed object confirms first; "" when the
// object is unmanaged.
export function driftNote(m?: Managed | null): string {
  return m ? `This will drift from the config repo (managed · config @${m.commit}).` : "";
}
