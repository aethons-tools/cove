import { Anchor, Badge, Card, Group, Loader, SimpleGrid, Stack, Text, Title } from "@mantine/core";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { Link, useLocation, useNavigate, useParams } from "react-router";
import type { ApiError } from "../../api/client";
import { Problem } from "../../Problem";
import type { SpecDetail } from "../api/types";
import { adminApi, nodeHref } from "../paths";
import { ConfirmAction } from "../ui/ConfirmAction";
import { EditForm } from "../ui/EditForm";
import { Facts } from "../ui/Facts";
import { ManagedBadge, driftNote } from "../ui/ManagedBadge";
import { Section } from "../ui/Section";
import { useAdminMutation } from "../ui/useAdminMutation";
import { specsKey } from "./ModelSpecsPage";
import { SpecFields, type SpecValues } from "./SpecFields";

const enc = encodeURIComponent;
const mono = (s: string) => <Text span ff="monospace">{s}</Text>;
const chips = (xs: string[]): ReactNode | null => xs.length ? <Group gap={4}>{xs.map((x) => <Badge key={x} size="sm" variant="light" color="gray" tt="none">{x}</Badge>)}</Group> : null;

// ModelSpecPage is one model-spec: its harness, policy, Claude body, the
// roles that run it, and its editor.
export function ModelSpecPage() {
  const { name = "" } = useParams();
  const here = useLocation();
  const key = ["admin", "model-spec", name];
  const { data, error, isPending } = useQuery<SpecDetail, ApiError>({ queryKey: key, queryFn: () => adminApi.get(`model-specs/${enc(name)}`) });
  if (isPending) return <Loader size="sm" />;
  if (error?.code === "not_found") {
    return <Stack gap="xs"><Title order={2}>Model-spec not found</Title>
      <Text size="sm">No model-spec {mono(name)}. <Anchor component={Link} to={nodeHref("config/model-specs")}>Back to model-specs</Anchor></Text></Stack>;
  }
  if (error) return <Problem error={error} here={here.pathname} />;
  return <Spec key={data.name} d={data} dataKey={key} />;
}

function Spec({ d, dataKey }: { d: SpecDetail; dataKey: unknown[] }) {
  const client = useQueryClient();
  const navigate = useNavigate();
  const f = d.fields;
  const drift = driftNote(d.managed);
  const save = useAdminMutation((v: SpecValues) => adminApi.post<SpecDetail>(`model-specs/${enc(d.name)}`, v), {
    invalidate: [specsKey], onSuccess: (x) => client.setQueryData(dataKey, x),
  });
  const remove = useAdminMutation(() => adminApi.del(`model-specs/${enc(d.name)}`), { invalidate: [specsKey], onSuccess: () => void navigate(nodeHref("config/model-specs")) });
  return (
    <Stack>
      <Group justify="space-between" wrap="wrap">
        <Group gap="sm" align="baseline">
          <Title order={2}>{d.name}</Title>
          <ManagedBadge managed={d.managed} />
          <Text size="sm" c="dimmed" ff="monospace">{f.type} {f.version}</Text>
        </Group>
        <ConfirmAction label="Delete" confirm={`Delete model-spec ${d.name}?${drift ? " " + drift : ""}`} onConfirm={() => remove.mutateAsync(undefined)} />
      </Group>
      <SimpleGrid cols={{ base: 1, md: 2 }}>
        <Section title="Harness" sub="who the agent runs as, and on what model">
          <Facts rows={[
            ["Type", mono(f.type)],
            ["Version", mono(f.version)],
            ["Version constraint", f.versionConstraint ? mono(f.versionConstraint) : null, "exactly the version"],
            ["Principal", <>{mono(f.principal)} <Text span size="sm" c="dimmed">credential name</Text></>],
            ["Header rules", chips(d.headerRules.map((r) => `${r.name} ${r.op} ${r.value}`)), "none"],
            ["Model", f.modelId ? mono(f.modelId) : null, "harness default"],
            ["Effort", f.effort ? mono(f.effort) : null, "harness default"],
            ["Note", f.note || null, "none"],
          ]} />
        </Section>
        <Section title="Policy" sub="the harness permission policy">
          <Facts rows={[
            ["Mode", f.mode ? mono(f.mode) : null, "unset = bypassPermissions (legacy)"],
            ["Allow", chips(d.allow), "none"],
            ["Deny", chips(d.deny), "none"],
          ]} />
        </Section>
      </SimpleGrid>
      <Section title="Used by" sub="roles that run this model-spec">
        {d.uses.length ? (
          <Group gap={6}>{d.uses.map((u) => (
            <Group key={`${u.project}/${u.role}`} gap={4}>
              <Anchor component={Link} to={nodeHref(`config/projects/${enc(u.project)}/agents/${enc(u.role)}`)} size="sm" ff="monospace">{u.project}/{u.role}</Anchor>
              {u.implicit && <Text span size="xs" c="dimmed">no model-spec set</Text>}
            </Group>
          ))}</Group>
        ) : <Text size="sm" c="dimmed">No role runs this model-spec.</Text>}
      </Section>
      <Section title="Claude" sub="the per-harness body">
        <Stack gap="xs">
          {d.hasClaude ? (
            <Facts rows={[
              ["Provider", mono(f.provider)],
              ["Provider env", chips(d.providerEnvKeys), "none"],
              ["Plugins", <>{chips(d.plugins) ?? <Text span c="dimmed">none</Text>} <Text span size="xs" c="dimmed">installed at image build</Text></>],
              ["Settings", chips(d.settingsKeys), "none"],
            ]} />
          ) : <Text size="sm" c="dimmed">No body.</Text>}
          <details>
            <summary style={{ cursor: "pointer" }}>Edit model-spec</summary>
            <Card withBorder mt="xs"><EditSpec key={JSON.stringify(f)} d={d} confirm={drift ? `${drift} Save anyway?` : undefined} onSave={(v) => save.mutateAsync(v)} /></Card>
          </details>
        </Stack>
      </Section>
    </Stack>
  );
}

function EditSpec({ d, confirm, onSave }: { d: SpecDetail; confirm?: string; onSave: (v: SpecValues) => Promise<unknown> }) {
  const [v, setV] = useState<SpecValues>({ ...d.fields });
  return (
    <EditForm submit="Save model-spec" confirm={confirm} onSubmit={() => onSave(v)}>
      <SpecFields value={v} onChange={setV} choices={d.choices} />
    </EditForm>
  );
}
