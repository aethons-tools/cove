import { Badge, Card, Group, List, Stack, Text, TextInput, Title } from "@mantine/core";
import { useState } from "react";
import type { ChainRow, ProjectDetail } from "../api/types";
import { useConfigEditable } from "../branch/branch";
import { ConfirmAction } from "../ui/ConfirmAction";
import { EditForm } from "../ui/EditForm";
import { Section } from "../ui/Section";
import { TypeAhead } from "../ui/TypeAhead";
import { ProjectGate } from "./ProjectPage";
import { useProjectWrite } from "./useProject";

const tiersHint = "one per line, user:alice,channel:eng@15m — first line is tier 0";

// EscalationPage is a project's Escalation: who is pinged, in order, when an
// agent there is waiting — the default chain and per-category chains.
export function EscalationPage() {
  return <ProjectGate body={(d) => <Escalation key={d.name} d={d} />} />;
}

function Escalation({ d }: { d: ProjectDetail }) {
  const p = d.name;
  const write = useProjectWrite(p);
  const editable = useConfigEditable();
  const put = (category: string, tiers: string) => write.mutateAsync({ path: "escalation", body: { category, tiers } });
  const clear = (category: string) => write.mutateAsync({ path: `escalation?category=${encodeURIComponent(category)}`, del: true });
  const [category, setCategory] = useState("");
  const [tiers, setTiers] = useState("");
  return (
    <Stack>
      <div>
        <Title order={2}>Escalation</Title>
        <Text size="sm" c="dimmed">who is pinged, in order, when an agent in {p} is waiting</Text>
      </div>
      <Section title="Chains">
        <Stack>
          {d.escalation.length === 0 && <Text size="sm" c="dimmed">No escalation policy.</Text>}
          {d.escalation.map((c) => (
            <div key={c.category}>
              <Text size="sm" fw={600} c="dimmed">{c.category ? <>category <Text span ff="monospace">{c.category}</Text></> : "default chain"}</Text>
              <Tiers c={c} />
              {editable && c.category && (
                <details>
                  <summary style={{ cursor: "pointer" }}>Edit</summary>
                  <ChainForm key={c.spec} p={p} c={c} onSave={(t) => put(c.category, t)} onClear={() => clear(c.category)} />
                </details>
              )}
            </div>
          ))}
        </Stack>
      </Section>
      {editable && <>
        <details>
          <summary style={{ cursor: "pointer" }}>Edit default chain</summary>
          <Card withBorder mt="xs">
            <ChainForm key={d.defaultChain.spec} p={p} c={d.defaultChain} onSave={(t) => put("", t)} onClear={() => clear("")} />
          </Card>
        </details>
        <details>
          <summary style={{ cursor: "pointer" }}>Add category chain</summary>
          <Card withBorder mt="xs">
            <EditForm submit="Add chain" resetOnSuccess={() => { setCategory(""); setTiers(""); }} onSubmit={() => put(category, tiers)}>
              <TextInput label="Category" required value={category} onChange={(e) => setCategory(e.currentTarget.value)} />
              <TypeAhead label="Tiers" description={tiersHint} kind="targets" project={p} list multiline required value={tiers} onChange={setTiers} />
            </EditForm>
          </Card>
        </details>
      </>}
    </Stack>
  );
}

function Tiers({ c }: { c: ChainRow }) {
  return (
    <List type="ordered" size="sm" spacing={4}>
      {c.tiers.map((t, i) => (
        <List.Item key={i}>
          <Group gap={4}>
            {t.targets.map((x) => <Badge key={x.text} size="sm" variant="light" color={x.unknown ? "red" : "gray"} title={x.unknown ? "Not on this project's roster" : undefined}>{x.text}</Badge>)}
            <Text size="sm" c="dimmed">then wait {t.timeout}</Text>
          </Group>
        </List.Item>
      ))}
    </List>
  );
}

function ChainForm({ p, c, onSave, onClear }: {
  p: string; c: ChainRow; onSave: (tiers: string) => Promise<unknown>; onClear: () => Promise<unknown>;
}) {
  const [tiers, setTiers] = useState(c.spec);
  return (
    <Stack gap="xs">
      <EditForm submit="Save chain" onSubmit={() => onSave(tiers)}>
        <TypeAhead label="Tiers" description={tiersHint} kind="targets" project={p} list multiline required value={tiers} onChange={setTiers} />
      </EditForm>
      {c.tiers.length > 0 && <Group><ConfirmAction label="Clear" confirm={`Clear the ${c.category || "default"} chain?`} onConfirm={onClear} /></Group>}
    </Stack>
  );
}
