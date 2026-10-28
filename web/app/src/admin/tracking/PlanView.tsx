import { Alert, Anchor, Badge, Checkbox, Code, Group, Stack, Table, Text } from "@mantine/core";
import { Link } from "react-router";
import type { Plan } from "../api/types";
import { nodeHref } from "../paths";
import { ConfirmAction } from "../ui/ConfirmAction";
import { DiffView } from "../ui/DiffView";
import { Section } from "../ui/Section";

const keys = (ks: { kind: string; name: string }[]) => ks.map((k) => `${k.kind} ${k.name}`).join(", ");

// PlanView is a config plan for review: the changes with their diffs, what
// it forgets or leaves alone, and Apply (bound to the plan's digest).
export function PlanView({ plan, busy, onForce, onApply }: { plan: Plan; busy: boolean; onForce: (force: boolean) => void; onApply: () => Promise<unknown> }) {
  const confirm = plan.changes ? `Apply ${plan.changes} change(s) to this Jam${plan.forced ? ", overwriting the drifted objects" : ""}?` : "Record this plan's commit?";
  return (
    <Section title="Plan" sub={<>{plan.source}{plan.commit && <> @ <Text span ff="monospace">{plan.commit}</Text></>} · {plan.changes} change(s), {plan.unchanged} unchanged{plan.forced && " · forced"}</>}>
      <Stack gap="xs">
        {plan.refusals.length > 0 && (
          <Alert color="red" title="Refused — nothing can be applied until the repo or Jam changes:">
            <ul style={{ margin: 0 }}>{plan.refusals.map((r) => <li key={r}>{r}</li>)}</ul>
          </Alert>
        )}
        {plan.hasDrift && <Checkbox checked={plan.forced} disabled={busy} onChange={(e) => onForce(e.currentTarget.checked)}
          label="Force: overwrite the objects edited in Jam since their apply with the repo's version" />}
        {plan.kitSources.length > 0 && <Text size="sm" c="dimmed">Kit sources: {plan.kitSources.map((k) => `${k.path} ${k.source} @ ${k.commit}`).join(", ")}</Text>}
        <Table>
          <Table.Thead><Table.Tr><Table.Th>Action</Table.Th><Table.Th>Object</Table.Th><Table.Th>File</Table.Th></Table.Tr></Table.Thead>
          <Table.Tbody>
            {plan.ops.length === 0 && <Table.Tr><Table.Td colSpan={3} c="dimmed">Nothing to change.</Table.Td></Table.Tr>}
            {plan.ops.map((op) => (
              <Table.Tr key={`${op.kind}/${op.name}`}>
                <Table.Td><Group gap={4}><Badge variant="light" color="gray">{op.action}</Badge>{op.drift && <Badge color="yellow">drift</Badge>}{op.unmanaged && <Badge color="yellow">not managed</Badge>}</Group></Table.Td>
                <Table.Td>
                  <Badge variant="outline" color="gray" mr={4}>{op.kind}</Badge>
                  {op.path ? <Anchor component={Link} to={nodeHref(op.path)} ff="monospace">{op.name}</Anchor> : <Text span ff="monospace">{op.name}</Text>}
                  {(op.lines.length > 0 || op.tooLarge || op.diff) && (
                    <details open={op.drift || op.unmanaged}>
                      <summary style={{ cursor: "pointer" }}>{op.drift || op.unmanaged ? "Jam (−) against the repo (+)" : "Changes"}</summary>
                      {op.diff ? <Code block>{op.diff}</Code> : op.tooLarge ? <Text size="sm" c="dimmed">Too large to diff here.</Text> : <DiffView lines={op.lines} />}
                    </details>
                  )}
                </Table.Td>
                <Table.Td><Text size="sm" ff="monospace">{op.file}</Text></Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
        {plan.forget.length > 0 && <Text size="sm" c="dimmed">Forget (mark only, the object is gone from Jam and the repo): {keys(plan.forget)}</Text>}
        {plan.unmanaged.length > 0 && <Text size="sm" c="dimmed">Unmanaged (left alone): {keys(plan.unmanaged)}</Text>}
        {plan.serve.state === "differs" ? <Text size="sm" c="dimmed">Serve config: differs (restart to apply): {plan.serve.keys.join(", ")}</Text>
          : plan.serve.state && <Text size="sm" c="dimmed">Serve config: {plan.serve.state}</Text>}
        {plan.refusals.length === 0 && <Group><ConfirmAction label="Apply this plan" color="jam" variant="filled" size="sm" confirm={confirm} onConfirm={onApply} /></Group>}
      </Stack>
    </Section>
  );
}
