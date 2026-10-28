import { Alert, Anchor, Checkbox, Code, Group, List, Stack, Text } from "@mantine/core";
import { useState } from "react";
import { Link } from "react-router";
import type { DraftView, Published } from "../api/types";
import { nodeHref } from "../paths";
import { ConfirmAction } from "../ui/ConfirmAction";
import { Section } from "../ui/Section";
import type { DraftWrite } from "./useDraft";

// DraftPublish makes the draft the next version: blocked by its problems; a
// config-managed kit (or a Pin that rewrites managed followers) needs the
// drift box ticked.
export function DraftPublish({ kit, d, run }: { kit: string; d: DraftView; run: (w: DraftWrite) => Promise<unknown> }) {
  const [pin, setPin] = useState(false);
  const [drift, setDrift] = useState(false);
  const needDrift = d.managed || (pin && d.pinDrift.length > 0);
  const blocked = d.problems.length > 0;
  return (
    <Section title="Publish" sub={`makes the draft the next version of ${kit} and closes it`}>
      <Stack gap="xs">
        {blocked
          ? <Alert color="red">Publishing is blocked until these are fixed:<List size="sm">{d.problems.map((p) => <List.Item key={p}>{p}</List.Item>)}</List></Alert>
          : <Text size="sm" c="dimmed">No problems: the draft can be published.</Text>}
        {d.managed && <Alert color="yellow">{d.driftWarning}</Alert>}
        <Checkbox label={<>Pin followers <Text span size="xs" c="dimmed">kits that follow {kit} stay on the version they follow now (v{d.current})</Text></>}
          checked={pin} onChange={(e) => setPin(e.currentTarget.checked)} />
        {d.pinDriftNote && <Alert color="yellow">{d.pinDriftNote}</Alert>}
        {(d.managed || d.pinDrift.length > 0) && (
          <Checkbox label={<>{d.driftConfirm}{!d.managed && <Text span size="xs" c="dimmed"> needed with Pin followers</Text>}</>}
            checked={drift} onChange={(e) => setDrift(e.currentTarget.checked)} />
        )}
        <Group>
          {blocked
            ? <ConfirmAction label="Publish" color="jam" variant="filled" size="sm" disabled confirm="" onConfirm={async () => {}} title="Fix the problems first" />
            : <ConfirmAction label="Publish" color="jam" variant="filled" size="sm" disabled={needDrift && !drift}
                confirm={`Publish the draft as the next version of ${kit}?${d.managed ? " " + d.driftWarning : ""}`}
                onConfirm={() => run({ path: "publish", body: { etag: d.etag, pin, drift: needDrift && drift } })} />}
        </Group>
      </Stack>
    </Section>
  );
}

// PublishedCard is a just-published draft: its version, what it pinned, and
// — drifting from the config repo — the files to write there.
export function PublishedCard({ kit, p }: { kit: string; p: Published }) {
  return (
    <Section title={`Published v${p.version}`} sub={`from the draft (base v${p.base})`}
      actions={<Anchor component={Link} to={`${nodeHref("config/kits/" + encodeURIComponent(kit))}?v=${p.version}`} size="sm">view v{p.version}</Anchor>}>
      <Stack gap="xs">
        {p.pinned.map((x) => <Text key={x} size="sm">pinned {x}</Text>)}
        {p.warning && <Alert color="yellow">{p.warning}</Alert>}
        {p.drift && (
          <div>
            <Text fw={600} size="sm">Write to config repo</Text>
            <Text size="sm">In your config repo checkout run <Code>jam config write &lt;repo&gt;</Code>, check <Code>git diff</Code>, and commit. Expect these files:</Text>
            <List size="sm">{p.configFiles.map((f) => <List.Item key={f.path}><Text span ff="monospace">{f.path}</Text> <Text span c="dimmed">{f.change}</Text></List.Item>)}</List>
          </div>
        )}
      </Stack>
    </Section>
  );
}
