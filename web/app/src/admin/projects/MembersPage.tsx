import { Anchor, Badge, Card, Group, Stack, Text, Textarea, Title } from "@mantine/core";
import { useState } from "react";
import { Link } from "react-router";
import type { MemberRow, ProjectDetail } from "../api/types";
import { nodeHref } from "../paths";
import { ConfirmAction } from "../ui/ConfirmAction";
import { DataTable } from "../ui/DataTable";
import { EditForm } from "../ui/EditForm";
import { driftNote } from "../ui/ManagedBadge";
import { TypeAhead } from "../ui/TypeAhead";
import { ProjectGate } from "./ProjectPage";
import { useProjectWrite } from "./useProject";

const enc = encodeURIComponent;

// MembersPage is a project's Members: who agents there can address, and
// where each gets DMs.
export function MembersPage() {
  return <ProjectGate body={(d) => <Members key={d.name} d={d} />} />;
}

function Members({ d }: { d: ProjectDetail }) {
  const p = d.name;
  const write = useProjectWrite(p);
  const drift = driftNote(d.managed);
  const save = drift ? `${drift} Save anyway?` : undefined;
  const [user, setUser] = useState("");
  const [delivery, setDelivery] = useState("");
  return (
    <Stack>
      <div>
        <Title order={2}>Members</Title>
        <Text size="sm" c="dimmed">the users agents in {p} can address, and where they get DMs here</Text>
      </div>
      <DataTable rows={d.members} rowKey={(m) => m.userId} empty="No members." columns={[
        { key: "u", label: "User", render: (m) => <Anchor component={Link} to={nodeHref(`config/projects/${enc(p)}/members/${enc(m.userId)}`)} fw={600} ff="monospace">{m.name}</Anchor> },
        { key: "h", label: "Handle", render: (m) => <Text size="sm" ff="monospace">{m.handle ? `@${m.handle}` : "—"}</Text> },
        { key: "d", label: "Delivery", render: (m) => <Delivery m={m} /> },
        { key: "x", label: "", render: (m) => (
          <Group gap="xs" align="flex-start" wrap="nowrap">
            <EditDelivery key={m.deliverySpec} m={m} confirm={save} onSave={(spec) => write.mutateAsync({ path: "members", body: { user: m.userId, delivery: spec, add: false } })} />
            <ConfirmAction label="Remove" confirm={`Remove ${m.name} from ${p}?${drift ? " " + drift : ""}`}
              onConfirm={() => write.mutateAsync({ path: `members/${enc(m.userId)}`, del: true })} />
          </Group>
        ) },
      ]} />
      <details>
        <summary style={{ cursor: "pointer" }}>Add member</summary>
        <Card withBorder mt="xs">
          <EditForm submit="Add member" confirm={save} resetOnSuccess={() => { setUser(""); setDelivery(""); }}
            onSubmit={() => write.mutateAsync({ path: "members", body: { user, delivery, add: true } })}>
            <TypeAhead label="User" description="a user's name" kind="users" required value={user} onChange={setUser} />
            <Textarea label="Delivery" description="one per line, service:address, e.g. discord:<inbox-channel-id>" autosize minRows={2} spellCheck={false}
              value={delivery} onChange={(e) => setDelivery(e.currentTarget.value)} />
          </EditForm>
        </Card>
      </details>
    </Stack>
  );
}

export function Delivery({ m }: { m: MemberRow }) {
  if (m.delivery.length === 0) return <Text size="sm" c="dimmed">—</Text>;
  return <>{m.delivery.map((x) => <Group key={x.service + x.address} gap={4}><Badge size="sm" variant="light" color="gray">{x.service}</Badge><Text size="sm" ff="monospace">{x.address}</Text></Group>)}</>;
}

function EditDelivery({ m, confirm, onSave }: { m: MemberRow; confirm?: string; onSave: (spec: string) => Promise<unknown> }) {
  const [spec, setSpec] = useState(m.deliverySpec);
  return (
    <details>
      <summary style={{ cursor: "pointer" }}>Edit</summary>
      <EditForm submit={`Save ${m.name}`} confirm={confirm} onSubmit={() => onSave(spec)}>
        <Textarea label="Delivery" description="one per line, service:address" autosize minRows={2} spellCheck={false} value={spec} onChange={(e) => setSpec(e.currentTarget.value)} />
      </EditForm>
    </details>
  );
}
