import { Anchor, Badge, Group, Paper, Stack, Text } from "@mantine/core";
import { Link } from "react-router";
import type { KitDetail } from "../api/types";
import { ConfirmAction } from "../ui/ConfirmAction";

// VersionRail is a kit's versions: each opens (?v=), diffs against the one
// before it and the current one, and — not current — can be pinned.
export function VersionRail({ k, href, onPin }: { k: KitDetail; href: (q: Record<string, string | number>) => string; onPin: (n: number) => Promise<unknown> }) {
  return (
    <Paper withBorder p={6} radius="md" component="nav" aria-label="Versions">
      <Stack gap={2}>
        {k.versions.map((v) => (
          <Paper key={v.n} p="xs" radius="sm" bg={v.n === k.view.n ? "var(--mantine-primary-color-light)" : undefined}>
            <Group gap={6}>
              <Anchor component={Link} to={href({ v: v.n })} fw={600}>v{v.n}</Anchor>
              {v.current ? <Badge size="sm">current</Badge> : (
                <ConfirmAction label="Pin" color="gray" size="compact-xs"
                  confirm={`Make v${v.n} the current version of ${k.name}? Roles bound to it raise v${v.n} from now on.`} onConfirm={() => onPin(v.n)} />
              )}
            </Group>
            <Group gap={8}>
              {v.prev > 0 && <Anchor component={Link} to={href({ v: v.n, diff: v.prev })} size="xs" c="dimmed">diff v{v.prev}</Anchor>}
              {!v.current && <Anchor component={Link} to={href({ v: v.n, diff: k.current })} size="xs" c="dimmed">diff current</Anchor>}
            </Group>
            <Text size="xs" c="dimmed" ff="monospace" title="Build digest under claude-default's harness: versions sharing it reuse one image (a role bound to another model-spec builds its own)">{v.digest || "invalid"}</Text>
          </Paper>
        ))}
      </Stack>
    </Paper>
  );
}
