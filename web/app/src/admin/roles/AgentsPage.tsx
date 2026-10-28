import { Anchor, Badge, Button, Card, Group, Loader, Stack, Text, TextInput, Title } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useLocation, useNavigate, useParams } from "react-router";
import type { ApiError } from "../../api/client";
import { Problem } from "../../Problem";
import type { Created, ProjectAgents, Requested } from "../api/types";
import { adminApi, nodeHref } from "../paths";
import { AttnFlag } from "../ui/AttnFlag";
import { StateCell } from "../ui/CovesTable";
import { DataTable } from "../ui/DataTable";
import { EditForm } from "../ui/EditForm";
import { StatusBadge } from "../ui/StatusBadge";
import { TypeAhead } from "../ui/TypeAhead";
import { useAdminMutation } from "../ui/useAdminMutation";

const enc = encodeURIComponent;

// useRequest raises a personal agent of a role for the signed-in operator,
// saying how it went.
export function useRequest(project: string) {
  return useAdminMutation((role: string) => adminApi.post<Requested>(`projects/${enc(project)}/roles/${enc(role)}/request`, {}), {
    onSuccess: (r) => notifications.show({ color: "green", message: r.message }),
    onError: (e) => notifications.show({ color: "red", message: e.message, autoClose: false }),
  });
}

// AgentsPage is a project's Agents: its roles (what a grant of each lets an
// agent reach) and its agents. Polled, as the old tab's tables were.
export function AgentsPage() {
  const { project = "" } = useParams();
  const here = useLocation();
  const { data, error, isPending } = useQuery<ProjectAgents, ApiError>({
    queryKey: ["admin", "project-agents", project], queryFn: () => adminApi.get(`projects/${enc(project)}/agents`), refetchInterval: 3000,
  });
  if (isPending) return <Loader size="sm" />;
  if (error?.code === "not_found") return <Stack gap="xs"><Title order={2}>Project not found</Title><Anchor component={Link} to={nodeHref("config/projects")}>Back to projects</Anchor></Stack>;
  if (error) return <Problem error={error} here={here.pathname} />;
  return <Agents d={data} />;
}

function Agents({ d }: { d: ProjectAgents }) {
  const p = d.project;
  const navigate = useNavigate();
  const request = useRequest(p);
  const [f, setF] = useState({ name: "", destinations: "", ttlSeconds: "", kit: "", modelSpec: "" });
  const set = (k: keyof typeof f) => (v: string) => setF({ ...f, [k]: v });
  const create = useAdminMutation(() => adminApi.post<Created>(`projects/${enc(p)}/roles`, f), {
    invalidate: [["admin", "project-agents", p]], onSuccess: (c) => void navigate(nodeHref(c.path)),
  });
  return (
    <Stack>
      <div>
        <Title order={2}>Agents</Title>
        <Text size="sm" c="dimmed">{p}'s roles — what a grant of each lets an agent reach — and its agents</Text>
      </div>
      <Title order={3}>Roles</Title>
      <details>
        <summary style={{ cursor: "pointer" }}>New role</summary>
        <Card withBorder mt="xs">
          <EditForm submit="Create role" onSubmit={() => create.mutateAsync(undefined)}>
            <TextInput label="Role name" required value={f.name} onChange={(e) => set("name")(e.currentTarget.value)} />
            <TypeAhead label="Destinations" description="comma-separated, e.g. git=git-pat,anthropic" kind="destinations" list value={f.destinations} onChange={set("destinations")} />
            <TextInput label="TTL" description="seconds, optional" inputMode="numeric" value={f.ttlSeconds} onChange={(e) => set("ttlSeconds")(e.currentTarget.value)} />
            <TypeAhead label="Kit" description="optional" kind="kits" value={f.kit} onChange={set("kit")} />
            <TypeAhead label="Model-spec" description="optional; blank = claude-default" kind="model-specs" value={f.modelSpec} onChange={set("modelSpec")} />
          </EditForm>
        </Card>
      </details>
      <DataTable rows={d.roles} rowKey={(r) => r.name} empty="No roles." columns={[
        { key: "n", label: "Role", render: (r) => <Group gap={6}><Anchor component={Link} to={nodeHref(`config/projects/${enc(p)}/agents/${enc(r.name)}`)} fw={600} ff="monospace">{r.name}</Anchor><AttnFlag flag={r.attn} /></Group> },
        { key: "d", label: "Destinations", render: (r) => r.destinations.length
          ? <Group gap={4}>{r.destinations.map((x) => <Badge key={x.name} size="sm" variant="light" color="gray" tt="none">{x.cred ? `${x.name} → ${x.cred}` : x.name}</Badge>)}</Group>
          : <Text c="dimmed" size="sm">—</Text> },
        { key: "t", label: "TTL", render: (r) => r.ttl },
        { key: "k", label: "Kit", render: (r) => r.kit ? <Anchor component={Link} to={nodeHref("config/kits/" + enc(r.kit))} size="sm">{r.kit}</Anchor> : <Text c="dimmed" size="sm">default</Text> },
        { key: "x", label: "", render: (r) => d.canRequest && (
          <Button size="xs" variant="light" title="Raise a personal agent of this role for you" loading={request.isPending && request.variables === r.name}
            onClick={() => request.mutate(r.name)}>Request</Button>
        ) },
      ]} />
      <Title order={3}>Agents</Title>
      <DataTable rows={d.agents} rowKey={(a) => a.id} empty="No agents in this project." columns={[
        { key: "a", label: "Agent", render: (a) => (
          <Group gap={6}>
            <Anchor component={Link} to={nodeHref("agents/" + enc(a.id))} fw={600} ff="monospace" size="sm">{a.name || a.id}</Anchor>
            {a.name && <Text size="xs" c="dimmed" ff="monospace">{a.id}</Text>}
            {a.unit && <Text size="xs" c="dimmed" ff="monospace">{a.unit}</Text>}
            <AttnFlag flag={a.attn} />
          </Group>
        ) },
        { key: "k", label: "Kind", render: (a) => <Badge size="sm" variant="light" color="gray">{a.kind}</Badge> },
        { key: "r", label: "Roles", render: (a) => <Group gap={4}>{a.roles.map((r) => <Anchor key={r} component={Link} to={nodeHref(`config/projects/${enc(p)}/agents/${enc(r)}`)} size="sm">{r}</Anchor>)}</Group> },
        { key: "s", label: "Status", render: (a) => <StatusBadge status={a.status} /> },
        { key: "i", label: "Image", render: (a) => <StateCell v={a.image} title="raised on an older image than its role would run now" /> },
        { key: "l", label: "Last seen", render: (a) => a.lastSeen || <Text span c="dimmed">—</Text> },
      ]} />
    </Stack>
  );
}
