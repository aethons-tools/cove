import { Alert, Anchor, Badge, Button, Card, Code, CloseButton, Group, Loader, Stack, Text, TextInput, Textarea, Title } from "@mantine/core";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useLocation, useNavigate, useSearchParams } from "react-router";
import type { ApiError } from "../../api/client";
import { Problem } from "../../Problem";
import type { AgentsResponse, Created, Enrolled } from "../api/types";
import { adminApi, nodeHref } from "../paths";
import { AttnFlag } from "../ui/AttnFlag";
import { ConfirmAction } from "../ui/ConfirmAction";
import { StateCell } from "../ui/CovesTable";
import { DataTable } from "../ui/DataTable";
import { EditForm } from "../ui/EditForm";
import { StatusBadge } from "../ui/StatusBadge";
import { TypeAhead } from "../ui/TypeAhead";
import { useAdminMutation } from "../ui/useAdminMutation";

const enc = encodeURIComponent;
export const agentsKey = ["admin", "agents"];

// AgentsPage is the Agents tab: every agent, running or not (?status=
// filters it), polled; Raise and Enroll.
export function AgentsPage() {
  const [q] = useSearchParams();
  const status = q.get("status") ?? "";
  const here = useLocation();
  const { data, error, isPending } = useQuery<AgentsResponse, ApiError>({
    queryKey: [...agentsKey, status], queryFn: () => adminApi.get(`agents${status ? `?status=${enc(status)}` : ""}`), refetchInterval: 3000,
  });
  const [enrolled, setEnrolled] = useState<Enrolled | null>(null);
  return (
    <Stack>
      <div>
        <Title order={2}>Agents</Title>
        <Text size="sm" c="dimmed">every agent, running or not — refreshes every 3s</Text>
      </div>
      {data?.canEdit && <Raise />}
      <Enroll onEnrolled={setEnrolled} />
      {enrolled && <TokenPanel e={enrolled} onClose={() => setEnrolled(null)} />}
      {isPending ? <Loader size="sm" /> : error ? <Problem error={error} here={here.pathname} /> : <Agents d={data} />}
    </Stack>
  );
}

function Agents({ d }: { d: AgentsResponse }) {
  const remove = useAdminMutation((id: string) => adminApi.del(`agents/${enc(id)}/studio`), { invalidate: [agentsKey] });
  return (
    <Stack gap="xs">
      <Group gap={4}>
        {d.filters.map((f) => (
          <Anchor key={f.key} component={Link} to={nodeHref("agents") + (f.key ? `?status=${enc(f.key)}` : "")} size="sm"
            fw={f.key === d.status ? 700 : undefined} aria-current={f.key === d.status ? "page" : undefined}>{f.value}</Anchor>
        ))}
      </Group>
      <DataTable rows={d.agents} rowKey={(a) => a.id} empty={d.status ? "No agents with this status." : "No agents."} columns={[
        { key: "a", label: "Agent", render: (a) => (
          <Group gap={6}>
            <Anchor component={Link} to={nodeHref("agents/" + enc(a.id))} fw={600} ff="monospace" size="sm">{a.name || a.id}</Anchor>
            {a.name && <Text size="xs" c="dimmed" ff="monospace">{a.id}</Text>}
            {a.unit && <Text size="xs" c="dimmed" ff="monospace">{a.unit}</Text>}
            <AttnFlag flag={a.attn} />
          </Group>
        ) },
        { key: "k", label: "Kind", render: (a) => <Badge size="sm" variant="light" color="gray">{a.kind}</Badge> },
        { key: "p", label: "Project", render: (a) => a.project ? <Anchor component={Link} to={nodeHref("config/projects/" + enc(a.project))} size="sm">{a.project}</Anchor> : <Text c="dimmed" size="sm">—</Text> },
        { key: "r", label: "Role", render: (a) => a.role
          ? <Group gap={4}><Anchor component={Link} to={nodeHref(`config/projects/${enc(a.project)}/agents/${enc(a.role)}`)} size="sm">{a.role}</Anchor>{a.moreGrants > 0 && <Text size="xs" c="dimmed">+{a.moreGrants}</Text>}</Group>
          : <Text c="dimmed" size="sm">—</Text> },
        { key: "s", label: "Status", render: (a) => <StatusBadge status={a.status} /> },
        { key: "c", label: "Connector", render: (a) => <StateCell v={a.connector} /> },
        { key: "i", label: "Image", render: (a) => <StateCell v={a.image} title="raised on an older image than its role would run now" /> },
        { key: "l", label: "Last seen", render: (a) => a.lastSeen || <Text span c="dimmed">—</Text> },
        ...(d.canEdit ? [{ key: "x", label: "", render: (a: AgentsResponse["agents"][number]) => a.hasStudio && (
          <ConfirmAction label="Teardown" confirm={`Tear down ${a.id}?`} onConfirm={() => remove.mutateAsync(a.id)} />
        ) }] : []),
      ]} />
    </Stack>
  );
}

function Raise() {
  const navigate = useNavigate();
  const [f, setF] = useState({ id: "", role: "", project: "default", unit: "", prompt: "" });
  const set = (k: keyof typeof f) => (v: string) => setF({ ...f, [k]: v });
  const raise = useAdminMutation(() => adminApi.post<Created>("agents", f), { invalidate: [agentsKey], onSuccess: (c) => void navigate(nodeHref(c.path)) });
  return (
    <details>
      <summary style={{ cursor: "pointer" }}>Raise agent</summary>
      <Card withBorder mt="xs">
        <EditForm submit="Raise" onSubmit={() => raise.mutateAsync(undefined)}>
          <TextInput label="Label" description="its name; the agent gets its own id" required autoComplete="off" value={f.id} onChange={(e) => set("id")(e.currentTarget.value)} />
          <TypeAhead label="Role" kind="roles" project={f.project} required value={f.role} onChange={set("role")} />
          <TypeAhead label="Project" kind="projects" value={f.project} onChange={set("project")} />
          <TextInput label="Unit" description="e.g. a ticket id" value={f.unit} onChange={(e) => set("unit")(e.currentTarget.value)} />
          <Textarea label="Workload prompt" description="optional" autosize minRows={2} value={f.prompt} onChange={(e) => set("prompt")(e.currentTarget.value)} />
        </EditForm>
      </Card>
    </details>
  );
}

function Enroll({ onEnrolled }: { onEnrolled: (e: Enrolled) => void }) {
  const [f, setF] = useState({ id: "", role: "", project: "default", destinations: "" });
  const set = (k: keyof typeof f) => (v: string) => setF({ ...f, [k]: v });
  const enroll = useAdminMutation(() => adminApi.post<Enrolled>("enrollments", f), { invalidate: [agentsKey], onSuccess: onEnrolled });
  return (
    <details>
      <summary style={{ cursor: "pointer" }}>Enroll agent</summary>
      <Card withBorder mt="xs">
        <EditForm submit="Enroll" onSubmit={() => enroll.mutateAsync(undefined)} resetOnSuccess={() => setF({ id: "", role: "", project: f.project, destinations: "" })}>
          <TextInput label="Agent id" required autoComplete="off" value={f.id} onChange={(e) => set("id")(e.currentTarget.value)} />
          <TypeAhead label="Role" kind="roles" project={f.project} required value={f.role} onChange={set("role")} />
          <TypeAhead label="Project" kind="projects" value={f.project} onChange={set("project")} />
          <TypeAhead label="Destination overrides" description="optional, e.g. git=git-pat" kind="destinations" list value={f.destinations} onChange={set("destinations")} />
        </EditForm>
      </Card>
    </details>
  );
}

// TokenPanel shows an enrollment's identity token: this once, until closed.
function TokenPanel({ e, onClose }: { e: Enrolled; onClose: () => void }) {
  return (
    <Alert color="yellow" withCloseButton={false}>
      <Group justify="space-between" align="flex-start" wrap="nowrap">
        <Stack gap={6} style={{ minWidth: 0 }}>
          <Text size="sm"><b>Enrolled {e.id}.</b> Copy this identity token now — it is not shown again.</Text>
          <Code block style={{ wordBreak: "break-all", whiteSpace: "pre-wrap" }}>{e.token}</Code>
        </Stack>
        <Group gap={4} wrap="nowrap">
          <Button size="xs" onClick={() => void navigator.clipboard?.writeText(e.token)}>Copy</Button>
          <CloseButton aria-label="Dismiss the token" onClick={onClose} />
        </Group>
      </Group>
    </Alert>
  );
}
