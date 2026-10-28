import { Anchor, Badge, Button, Group, Loader, NativeSelect, Paper, SimpleGrid, Stack, Text, TextInput, Title } from "@mantine/core";
import { useState, type ReactNode } from "react";
import { Link, useLocation, useNavigate, useParams } from "react-router";
import type { Created, ProjectDetail } from "../api/types";
import { adminApi, nodeHref } from "../paths";
import { Problem } from "../../Problem";
import { ConfirmAction } from "../ui/ConfirmAction";
import { ContextPanel } from "../ui/ContextPanel";
import { EditForm } from "../ui/EditForm";
import { ManagedBadge, driftNote } from "../ui/ManagedBadge";
import { Section } from "../ui/Section";
import { useAdminMutation } from "../ui/useAdminMutation";
import { projectsKey, useProject, useProjectWrite } from "./useProject";

const enc = encodeURIComponent;

// ProjectGate loads the :project route's project (section "intercom" with
// its log) and renders body with it, or says it doesn't exist.
export function ProjectGate({ section = "", body }: { section?: string; body: (d: ProjectDetail) => ReactNode }) {
  const { project = "" } = useParams();
  const here = useLocation();
  const { data, error, isPending } = useProject(project, section);
  if (isPending) return <Loader size="sm" />;
  if (error?.code === "not_found") {
    return (
      <Stack gap="xs">
        <Title order={2}>Project not found</Title>
        <Text size="sm">No project <Text span ff="monospace">{project}</Text>. <Anchor component={Link} to={nodeHref("config/projects")}>Back to projects</Anchor></Text>
      </Stack>
    );
  }
  if (error) return <Problem error={error} here={here.pathname} />;
  return <>{body(data)}</>;
}

// ProjectPage is a project's overview: rename, delete, chat service, its
// agent context, and tiles into its sections.
export function ProjectPage() {
  return <ProjectGate body={(d) => <Overview key={d.name} d={d} />} />;
}

function Overview({ d }: { d: ProjectDetail }) {
  const navigate = useNavigate();
  const p = d.name;
  const base = "config/projects/" + enc(p);
  const drift = driftNote(d.managed);
  const write = useProjectWrite(p);
  const [name, setName] = useState(p);
  const [service, setService] = useState(d.chatService);
  const rename = useAdminMutation((n: string) => adminApi.post<Created>(`projects/${enc(p)}/rename`, { name: n }), {
    invalidate: [projectsKey], onSuccess: (c) => void navigate(nodeHref(c.path), { replace: true }),
  });
  const remove = useAdminMutation(() => adminApi.del(`projects/${enc(p)}`), {
    invalidate: [projectsKey], onSuccess: () => void navigate(nodeHref("config/projects")),
  });
  const tile = (path: string, n: ReactNode, label: string) => (
    <Paper component={Link} to={nodeHref(`${base}/${path}`)} withBorder p="sm" radius="md" style={{ textDecoration: "none", color: "inherit" }}>
      <Text fz={22} fw={600}>{n}</Text><Text size="xs" c="dimmed">{label}</Text>
    </Paper>
  );
  return (
    <Stack>
      <Group justify="space-between" wrap="wrap">
        <Group gap="sm" align="baseline">
          <Title order={2}>{p}</Title>
          <ManagedBadge managed={d.managed} />
          {d.chatService ? <Badge variant="light" color="gray">chat: {d.chatService}</Badge> : <Text size="sm" c="dimmed">tracker @-mentions only</Text>}
        </Group>
        <Group gap="xs" align="flex-end">
          {d.renamable && (
            <EditForm submit="Rename" confirm={drift ? `${drift} Rename anyway?` : undefined} onSubmit={() => rename.mutateAsync(name)}>
              <TextInput aria-label="New name" size="xs" required value={name} onChange={(e) => setName(e.currentTarget.value)} />
            </EditForm>
          )}
          {d.inUseBy
            ? <Button size="xs" color="red" variant="light" disabled title={`Still referenced by ${d.inUseBy} — remove its roles, agents and grants first`}>Delete</Button>
            : <ConfirmAction label="Delete" onConfirm={() => remove.mutateAsync(undefined)}
                confirm={`Delete project ${p}? Its roster, escalation and chat service go with it; its channels are archived (history stays readable) and its name is freed.${drift ? " " + drift : ""}`} />}
        </Group>
      </Group>
      <SimpleGrid cols={{ base: 2, sm: 4 }}>
        {tile("members", d.members.length, "members")}
        {tile("agents", <>{d.liveAgents}</>, `agents live of ${d.agents} · ${d.roles} roles`)}
        {tile("intercom", d.rooms.length, "rooms")}
        {tile("escalation", d.escalation.length, "escalation chains")}
      </SimpleGrid>
      <SimpleGrid cols={{ base: 1, md: 2 }}>
        <Section title="Chat service" sub="what backs human DMs">
          <EditForm submit="Save" confirm={drift ? `${drift} Save anyway?` : undefined} onSubmit={() => write.mutateAsync({ path: "chat-service", body: { service } })}>
            <NativeSelect label="Chat service" value={service} onChange={(e) => setService(e.currentTarget.value)}
              data={d.chatServices.map((s) => ({ value: s, label: s || "none — tracker @-mentions only" }))} />
          </EditForm>
        </Section>
      </SimpleGrid>
      <ContextPanel key={d.context.yaml} panel={d.context} onSave={(yaml) => write.mutateAsync({ path: "context", body: { yaml } })}
        onClear={() => write.mutateAsync({ path: "context", del: true })} />
    </Stack>
  );
}
