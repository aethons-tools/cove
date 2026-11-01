import { Badge, Code, Group, List, Stack, Table, Text, Textarea } from "@mantine/core";
import { useState } from "react";
import type { ContextPanel as Panel } from "../api/types";
import { useConfigEditable } from "../branch/branch";
import { ConfirmAction } from "./ConfirmAction";
import { EditForm } from "./EditForm";
import { Section } from "./Section";

// ContextPanel is an agent-context card (Jam, a project or a role): the
// stored layer, its delivered size against the budget, and the YAML editor.
export function ContextPanel({ panel, onSave, onClear }: { panel: Panel; onSave: (yaml: string) => Promise<unknown>; onClear: () => Promise<unknown> }) {
  const [yaml, setYaml] = useState(panel.yaml);
  const editable = useConfigEditable();
  return (
    <Section title="Agent context" sub={<>{panel.scope} — what every agent is told when raised</>}>
      <Stack gap="xs">
        <Group gap="xs">
          <Badge variant="light" color={panel.over ? "red" : "gray"}>{panel.coreBytes.toLocaleString()} / {panel.budget.toLocaleString()} bytes</Badge>
          {panel.leaves.length > 0 && <Badge variant="light" color="gray">{panel.leaves.length} leaves</Badge>}
          {panel.resources.length > 0 && <Badge variant="light" color="gray">{panel.resources.length} resources</Badge>}
        </Group>
        {panel.empty ? <Text size="sm" c="dimmed">None — agents get no {panel.scope} section.</Text> : (
          <>
            {panel.core && <Code block style={{ whiteSpace: "pre-wrap" }}>{panel.core}</Code>}
            {panel.leaves.length > 0 && <List size="sm">{panel.leaves.map((l) => <List.Item key={l.name}><Text span ff="monospace">{l.name}</Text> — read when {l.readWhen}</List.Item>)}</List>}
            {panel.resources.length > 0 && (
              <Table>
                <Table.Thead><Table.Tr><Table.Th>Name</Table.Th><Table.Th>Kind</Table.Th><Table.Th>Ref</Table.Th><Table.Th>Note</Table.Th></Table.Tr></Table.Thead>
                <Table.Tbody>{panel.resources.map((r) => <Table.Tr key={r.name}><Table.Td>{r.name}</Table.Td><Table.Td>{r.kind}</Table.Td><Table.Td ff="monospace">{r.ref}</Table.Td><Table.Td>{r.note}</Table.Td></Table.Tr>)}</Table.Tbody>
              </Table>
            )}
          </>
        )}
        {editable && <details>
          <summary style={{ cursor: "pointer" }}>Edit agent context</summary>
          <EditForm submit="Save context" onSubmit={() => onSave(yaml)}>
            <Textarea label="YAML" autosize minRows={8} spellCheck={false} value={yaml} onChange={(e) => setYaml(e.currentTarget.value)} styles={{ input: { fontFamily: "var(--mantine-font-family-monospace)" } }}
              description={<>core, leaves (name, read-when, body){panel.showResources && ", resources"} — the jam context file format, bodies inline. Never put a credential here.</>} />
          </EditForm>
          {!panel.empty && <Group mt="xs"><ConfirmAction label="Clear" confirm={`Remove this ${panel.scope} agent context?`} onConfirm={onClear} /></Group>}
        </details>}
      </Stack>
    </Section>
  );
}
