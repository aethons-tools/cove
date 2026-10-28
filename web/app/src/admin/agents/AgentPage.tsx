import { Alert, Anchor, Badge, Button, Card, Group, Loader, SimpleGrid, Stack, Table, Text, Title } from "@mantine/core";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { Link, useLocation, useNavigate, useParams } from "react-router";
import type { ApiError } from "../../api/client";
import { Problem } from "../../Problem";
import type { AgentDetail } from "../api/types";
import { adminApi, nodeHref } from "../paths";
import { ConfirmAction } from "../ui/ConfirmAction";
import { DataTable } from "../ui/DataTable";
import { EditForm } from "../ui/EditForm";
import { Facts } from "../ui/Facts";
import { Section } from "../ui/Section";
import { SetupView } from "../ui/SetupView";
import { SquawkTable } from "../ui/SquawkTable";
import { StatusBadge } from "../ui/StatusBadge";
import { TypeAhead } from "../ui/TypeAhead";
import { useAdminMutation } from "../ui/useAdminMutation";
import { agentsKey } from "./AgentsPage";

const enc = encodeURIComponent;
const mono = (s: string) => <Text span ff="monospace">{s}</Text>;
const roleHref = (p: string, r: string) => nodeHref(`config/projects/${enc(p)}/agents/${enc(r)}`);

// AgentPage is one agent: its identity and grants, its studio while it
// runs (runtime, waiting, egress, access), its session and squawks — the
// audit trail that outlives it — and its Setup.
export function AgentPage() {
  const { id = "" } = useParams();
  const here = useLocation();
  const { data, error, isPending } = useQuery<AgentDetail, ApiError>({ queryKey: ["admin", "agent", id], queryFn: () => adminApi.get(`agents/${enc(id)}`) });
  if (isPending) return <Loader size="sm" />;
  if (error?.code === "not_found") {
    return <Stack gap="xs"><Title order={2}>Agent not found</Title>
      <Text size="sm">Nothing is known about agent {mono(id)} — it isn't enrolled, isn't running and has no session or squawks. <Anchor component={Link} to={nodeHref("agents")}>Back to agents</Anchor></Text></Stack>;
  }
  if (error) return <Problem error={error} here={here.pathname} />;
  return <Agent key={data.id} d={data} />;
}

function Agent({ d }: { d: AgentDetail }) {
  const client = useQueryClient();
  const navigate = useNavigate();
  const set = (x: AgentDetail) => client.setQueryData(["admin", "agent", d.id], x);
  const toList = () => void navigate(nodeHref("agents"));
  const teardown = useAdminMutation(() => adminApi.del(`agents/${enc(d.id)}/studio`), { invalidate: [agentsKey], onSuccess: toList });
  const revoke = useAdminMutation(() => adminApi.del(`enrollments/${enc(d.id)}`), { invalidate: [agentsKey], onSuccess: toList });
  const write = useAdminMutation((x: { path: string; body?: unknown; del?: boolean }) =>
    x.del ? adminApi.del<AgentDetail>(`agents/${enc(d.id)}/${x.path}`) : adminApi.post<AgentDetail>(`agents/${enc(d.id)}/${x.path}`, x.body ?? {}), { invalidate: [agentsKey], onSuccess: set });
  return (
    <Stack>
      <Group justify="space-between" wrap="wrap">
        <Group gap="sm" align="baseline" wrap="wrap">
          <Title order={2} ff="monospace">{d.id}</Title>
          {d.running ? (
            <>
              <StatusBadge status={d.status} />
              <Badge variant="light" color="gray" tt="none">
                {d.kind}{d.owner && <> · {d.ownerId ? <Anchor component={Link} to={nodeHref("config/users/" + enc(d.ownerId))} inherit>{d.owner}</Anchor> : d.owner}</>}{d.name && ` · ${d.name}`}
              </Badge>
              <Anchor component={Link} to={nodeHref("config/projects/" + enc(d.project))} size="sm">{d.project}</Anchor>
              <Anchor component={Link} to={roleHref(d.project, d.role)} size="sm">{d.role}</Anchor>
              {d.unit && mono(d.unit)}
            </>
          ) : d.kind && <Badge variant="light" color="gray">{d.kind}</Badge>}
        </Group>
        <Group gap="xs">
          {d.sessionsEnabled && <Button component={Link} to={nodeHref(`agents/${enc(d.id)}/session`)} size="xs" variant="light">Open live timeline</Button>}
          {d.running && d.canEdit && <ConfirmAction label="Teardown" confirm={`Tear down ${d.id}?`} onConfirm={() => teardown.mutateAsync(undefined)} />}
          {d.hasActor && <ConfirmAction label="Revoke" confirm={`Revoke ${d.id}? This invalidates its token.`} onConfirm={() => revoke.mutateAsync(undefined)} />}
        </Group>
      </Group>
      {!d.running && (
        <Alert color="gray">{d.hasActor ? "This agent isn't running." : "This agent is no longer running — it was torn down and left the registry."} Its session and squawks, if any, remain below as an audit trail.</Alert>
      )}
      <Identity d={d} onGrant={(b) => write.mutateAsync({ path: "grants", body: b })}
        onRemove={(p, r) => write.mutateAsync({ path: `grants/${enc(p)}/${enc(r)}`, del: true })} />
      {d.running && (
        <SimpleGrid cols={{ base: 1, md: 2 }}>
          <Section title="Runtime">
            <Facts rows={[["Raised", d.raised], ["Last seen", d.lastSeen || null], ["Lease", d.lease ? mono(d.lease) : null], ["Backend", d.backend ? <>{d.backend}{d.location && <> {mono(d.location)}</>}</> : null]]} />
          </Section>
          <Section title="Waiting & escalation" sub="wake-on and who has been pinged">
            <Facts rows={[
              ["Waiting", d.waitingSince ? <>since {d.waitingSince} <Text span c="dimmed">· wakes on a reply past wait seq {d.waitSeq}</Text></> : null, "not waiting"],
              ["Escalation", d.tierPingedAt ? <>tier {d.escalationTier} pinged {d.tierPingedAt} · {d.escalationCategory ? <>category {mono(d.escalationCategory)}</> : "default chain"}</> : null, "none open"],
              ...(d.owner ? [["Owner nags", d.nags ? `${d.nags} nag(s)${d.lastNagAt ? `, last ${d.lastNagAt}` : ""}` : null, "none this wait"] as [string, ReactNode, string]] : []),
              ["Inbox", <Text span c="dimmed">committed through seq {d.commitSeq}</Text>],
            ]} />
          </Section>
          <Section title="Egress" sub="the role policy this agent runs under">
            <Facts rows={[
              ["Policy", d.egressKnown ? mono(d.egressId) : null, "unknown — re-applied on the next reconcile"],
              ["Re-apply", d.egressFailures ? <><Badge color="red" variant="light">{d.egressFailures} failure(s)</Badge> <Text span c="dimmed" size="sm">the supervisor tears the agent down at its limit</Text></> : null, "ok"],
            ]} />
          </Section>
          <Access d={d} onUnshare={(user) => write.mutateAsync({ path: `shares/${enc(user)}`, del: true })} />
        </SimpleGrid>
      )}
      <Section title="Session" sub="captured agent event streams, newest first">
        {d.sessionsEnabled ? (
          <DataTable rows={d.streams} rowKey={(s) => s.streamId} empty="No session captured yet." columns={[
            { key: "s", label: "Stream", render: (s) => <Anchor component={Link} to={nodeHref(`agents/${enc(d.id)}/session?stream=${enc(s.streamId)}`)} ff="monospace" size="sm">{s.streamId.slice(0, 12)}</Anchor> },
            { key: "a", label: "Started", render: (s) => s.started },
            { key: "l", label: "Last event", render: (s) => s.lastEvent },
            { key: "n", label: "Events", render: (s) => `${s.events} event(s)` },
          ]} />
        ) : <Text size="sm" c="dimmed">Session capture is not configured on this Jam.</Text>}
      </Section>
      <Section title="Squawks" sub="in its conversations, newest first"
        actions={d.squawksConfigured ? <Anchor component={Link} to={nodeHref(`health/intercom?participant=${enc(d.id)}`)} size="sm">{d.squawksMore && "all "}in Intercom</Anchor> : undefined}>
        {d.squawksConfigured ? <SquawkTable rows={d.squawks} /> : <Text size="sm" c="dimmed">Squawk log not configured.</Text>}
      </Section>
      {d.setupEnabled && <SetupCard path={`agents/${enc(d.id)}/setup`} />}
    </Stack>
  );
}

function Identity({ d, onGrant, onRemove }: { d: AgentDetail; onGrant: (b: unknown) => Promise<unknown>; onRemove: (p: string, r: string) => Promise<unknown> }) {
  const [g, setG] = useState({ role: "", project: "default", destinations: "" });
  const set = (k: keyof typeof g) => (v: string) => setG({ ...g, [k]: v });
  return (
    <Section title="Identity" sub="the grants this agent's token carries">
      {d.hasActor ? (
        <Stack gap="xs">
          <Group gap={6}>
            {d.grants.length === 0 && <Text size="sm" c="dimmed">no grants</Text>}
            {d.grants.map((x) => (
              <Badge key={`${x.project}/${x.role}`} variant="light" color="gray" tt="none" title={x.destinations.length ? `destinations: ${x.destinations.join(", ")}` : "no destinations"}
                rightSection={<ConfirmAction label="Remove" trigger="×" ariaLabel={`Remove grant ${x.project}/${x.role}`} variant="subtle" size="compact-xs"
                  confirm={`Remove ${x.role} grant on ${x.project} from ${d.id}?`} onConfirm={() => onRemove(x.project, x.role)} />}>
                <Anchor component={Link} to={roleHref(x.project, x.role)} inherit>{x.project}/{x.role}</Anchor>
              </Badge>
            ))}
          </Group>
          <Facts rows={[["Token expiry", d.tokenExpiry || null, "none"]]} />
          <details>
            <summary style={{ cursor: "pointer" }}>+ Grant</summary>
            <Card withBorder mt="xs">
              <EditForm submit="Add" onSubmit={() => onGrant(g)} resetOnSuccess={() => setG({ role: "", project: g.project, destinations: "" })}>
                <TypeAhead label="Role" kind="roles" project={g.project} required value={g.role} onChange={set("role")} />
                <TypeAhead label="Project" kind="projects" value={g.project} onChange={set("project")} />
                <TypeAhead label="Destination overrides" description="e.g. git=git-pat" kind="destinations" list value={g.destinations} onChange={set("destinations")} />
              </EditForm>
            </Card>
          </details>
        </Stack>
      ) : <Text size="sm" c="dimmed">Not enrolled — no identity token or grants (its token was released, or it was revoked).</Text>}
    </Section>
  );
}

function Access({ d, onUnshare }: { d: AgentDetail; onUnshare: (user: string) => Promise<unknown> }) {
  const a = d.access;
  return (
    <Section title="Access" sub="who can reach this studio, and why">
      {!a.enabled ? <Text size="sm" c="dimmed">Studio collaboration is off on this Jam.</Text> : (
        <Stack gap="xs">
          <Facts rows={[
            ["Operators", "always"],
            ...(a.requester ? [["Requester", <><Anchor component={Link} to={nodeHref("config/users/" + enc(a.requester.id))}>{a.requester.name}</Anchor> <Text span c="dimmed">· preview and ssh on their personal session</Text></>] as [string, ReactNode]] : []),
            ["Members", <>preview: {a.membersPreview} · ssh: {a.membersSsh} <Text span c="dimmed">· the <Anchor component={Link} to={roleHref(a.roleProject, a.roleName)}>{a.roleProject}/{a.roleName}</Anchor> role's policy for project members</Text></>],
          ]} />
          <DataTable rows={a.shares} rowKey={(s) => s.userId} empty={<>Not shared with anyone. <Text span c="dimmed">jam studio share {d.id} --with user:&lt;name&gt; --rights ssh</Text></>} columns={[
            { key: "u", label: "Shared with", render: (s) => <Anchor component={Link} to={nodeHref("config/users/" + enc(s.userId))}>{s.user}</Anchor> },
            { key: "r", label: "Rights", render: (s) => s.rights.join(", ") },
            { key: "e", label: "Expires", render: (s) => <>{s.expired && <Text span c="dimmed">expired </Text>}{s.expiresAt}</> },
            { key: "g", label: "Granted by", render: (s) => mono(s.grantedBy) },
            { key: "x", label: "", render: (s) => <ConfirmAction label="Unshare" confirm={`Unshare ${d.id} from ${s.user}? Their open SSH connections close unless they have access another way.`} onConfirm={() => onUnshare(s.userId)} /> },
          ]} />
          <Table>
            <Table.Thead><Table.Tr><Table.Th>Connected</Table.Th><Table.Th>Right</Table.Th><Table.Th>Since</Table.Th></Table.Tr></Table.Thead>
            <Table.Tbody>
              {a.live.length === 0 && <Table.Tr><Table.Td colSpan={3} c="dimmed">No open connections.</Table.Td></Table.Tr>}
              {a.live.map((l, i) => <Table.Tr key={i}><Table.Td>{l.name}</Table.Td><Table.Td>{l.right}</Table.Td><Table.Td>{l.since}</Table.Td></Table.Tr>)}
            </Table.Tbody>
          </Table>
          {a.previewsOn ? (
            <Table>
              <Table.Thead><Table.Tr><Table.Th>Previews</Table.Th><Table.Th>Port</Table.Th></Table.Tr></Table.Thead>
              <Table.Tbody>
                {a.previews.length === 0 && <Table.Tr><Table.Td colSpan={2} c="dimmed">Nothing exposed.</Table.Td></Table.Tr>}
                {a.previews.map((p) => <Table.Tr key={p.name}><Table.Td><Anchor href={p.url} target="_blank" rel="noopener noreferrer">{p.name}</Anchor></Table.Td><Table.Td>{p.port}</Table.Td></Table.Tr>)}
              </Table.Tbody>
            </Table>
          ) : <Text size="sm" c="dimmed">Previews are off on this Jam.</Text>}
        </Stack>
      )}
    </Section>
  );
}

// SetupCard reads the studio's setup only once opened (an image read on the
// launcher host, audit-logged).
function SetupCard({ path }: { path: string }) {
  const [open, setOpen] = useState(false);
  return (
    <Card withBorder>
      <details onToggle={(e) => setOpen(e.currentTarget.open)}>
        <summary style={{ cursor: "pointer", fontWeight: 600 }}>Setup — what this studio got: its snapshots (else a labelled recompute), config as of now, its image</summary>
        {open && <SetupView path={path} diffToggle />}
      </details>
    </Card>
  );
}
