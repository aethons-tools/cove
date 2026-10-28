import { Anchor, Badge, Card, Group, Stack, Text, TextInput, Title } from "@mantine/core";
import { useState } from "react";
import { Link } from "react-router";
import type { ProjectDetail } from "../api/types";
import { nodeHref } from "../paths";
import { ConfirmAction } from "../ui/ConfirmAction";
import { DataTable } from "../ui/DataTable";
import { EditForm } from "../ui/EditForm";
import { driftNote } from "../ui/ManagedBadge";
import { Section } from "../ui/Section";
import { SquawkTable } from "../ui/SquawkTable";
import { TypeAhead } from "../ui/TypeAhead";
import { ProjectGate } from "./ProjectPage";
import { useProjectWrite } from "./useProject";

const enc = encodeURIComponent;

// IntercomPage is a project's Intercom: its rooms and its recent messages.
export function IntercomPage() {
  return <ProjectGate section="intercom" body={(d) => <Intercom key={d.name} d={d} />} />;
}

function Intercom({ d }: { d: ProjectDetail }) {
  const p = d.name;
  const write = useProjectWrite(p, "intercom");
  const drift = driftNote(d.managed);
  const [name, setName] = useState("");
  const [connection, setConnection] = useState("");
  const [ref, setRef] = useState("");
  return (
    <Stack>
      <Group justify="space-between">
        <div>
          <Title order={2}>Intercom</Title>
          <Text size="sm" c="dimmed">{p}'s rooms and its recent messages</Text>
        </div>
        {/* The full channel log moves with Health (slice 8). */}
        <Anchor component={Link} to={nodeHref(`health/intercom?project=${enc(p)}`)} size="sm">Full log</Anchor>
      </Group>
      <Section title="Rooms">
        <DataTable rows={d.rooms} rowKey={(r) => r.name} empty="No rooms." columns={[
          { key: "n", label: "Name", render: (r) => <Text fw={600} ff="monospace" size="sm">{r.name}</Text> },
          { key: "c", label: "Connection", render: (r) => <Group gap={4}>{r.connection}<Badge size="sm" variant="light" color="gray">{r.kind}</Badge></Group> },
          { key: "r", label: "Ref", render: (r) => <Group gap={4}><Text size="sm" ff="monospace">{r.ref}</Text>{r.postOnly && <Badge size="sm" variant="light" color="gray" title="another channel receives this ref's replies">post-only</Badge>}</Group> },
          { key: "x", label: "", render: (r) => <ConfirmAction label="Remove" confirm={`Remove room ${r.name} from ${p}?${drift ? " " + drift : ""}`}
            onConfirm={() => write.mutateAsync({ path: `rooms/${enc(r.name)}`, del: true })} /> },
        ]} />
        <details>
          <summary style={{ cursor: "pointer" }}>Add room</summary>
          <Card withBorder mt="xs">
            <EditForm submit="Add room" confirm={drift ? `${drift} Save anyway?` : undefined} resetOnSuccess={() => { setName(""); setConnection(""); setRef(""); }}
              onSubmit={() => write.mutateAsync({ path: "rooms", body: { name, connection, ref } })}>
              <TextInput label="Name" description="an existing name is rebound" required value={name} onChange={(e) => setName(e.currentTarget.value)} />
              <TypeAhead label="Connection" description="a connection name, or a service (its connection)" kind="services" required placeholder="discord" value={connection} onChange={setConnection} />
              <TextInput label="Ref" description="the service's channel id" required value={ref} onChange={(e) => setRef(e.currentTarget.value)} />
            </EditForm>
          </Card>
        </details>
      </Section>
      <Title order={3}>Recent messages</Title>
      {d.logConfigured ? <SquawkTable rows={d.squawks} /> : <Text c="dimmed" size="sm">The message log is unavailable.</Text>}
    </Stack>
  );
}
