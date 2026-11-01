import { Alert, Anchor, Group, List, Stack, Text } from "@mantine/core";
import { Link } from "react-router";
import type { DraftView, Published } from "../api/types";
import { nodeHref } from "../paths";
import { ConfirmAction } from "../ui/ConfirmAction";
import { Section } from "../ui/Section";
import type { DraftWrite } from "./useDraft";

// DraftPublish makes the draft the next version, unless its problems block it.
// It never pins followers: the draft is on a branch, and the config repo
// names a parent without a version (the server refuses pin).
export function DraftPublish({ kit, d, run }: { kit: string; d: DraftView; run: (w: DraftWrite) => Promise<unknown> }) {
  const blocked = d.problems.length > 0;
  return (
    <Section title="Publish" sub={`makes the draft the next version of ${kit} and closes it`}>
      <Stack gap="xs">
        {blocked
          ? <Alert color="red">Publishing is blocked until these are fixed:<List size="sm">{d.problems.map((p) => <List.Item key={p}>{p}</List.Item>)}</List></Alert>
          : <Text size="sm" c="dimmed">No problems: the draft can be published.</Text>}
        <Group>
          {blocked
            ? <ConfirmAction label="Publish" color="jam" variant="filled" size="sm" disabled confirm="" onConfirm={async () => {}} title="Fix the problems first" />
            : <ConfirmAction label="Publish" color="jam" variant="filled" size="sm"
                confirm={`Publish the draft as the next version of ${kit}?`}
                onConfirm={() => run({ path: "publish", body: { etag: d.etag, pin: false } })} />}
        </Group>
      </Stack>
    </Section>
  );
}

// PublishedCard is a just-published draft: its version and what it pinned.
export function PublishedCard({ kit, p }: { kit: string; p: Published }) {
  return (
    <Section title={`Published v${p.version}`} sub={`from the draft (base v${p.base})`}
      actions={<Anchor component={Link} to={`${nodeHref("config/kits/" + encodeURIComponent(kit))}?v=${p.version}`} size="sm">view v{p.version}</Anchor>}>
      <Stack gap="xs">
        {p.pinned.map((x) => <Text key={x} size="sm">pinned {x}</Text>)}
        {p.warning && <Alert color="yellow">{p.warning}</Alert>}
      </Stack>
    </Section>
  );
}
