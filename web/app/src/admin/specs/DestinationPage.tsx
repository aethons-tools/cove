import { Alert, Anchor, Badge, Card, Group, List, Loader, SimpleGrid, Stack, Text, Title } from "@mantine/core";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useLocation, useNavigate, useParams } from "react-router";
import type { ApiError } from "../../api/client";
import { Problem } from "../../Problem";
import type { DestDetail } from "../api/types";
import { adminApi, nodeHref } from "../paths";
import { ConfirmAction } from "../ui/ConfirmAction";
import { DataTable } from "../ui/DataTable";
import { EditForm } from "../ui/EditForm";
import { Facts } from "../ui/Facts";
import { ManagedBadge, driftNote } from "../ui/ManagedBadge";
import { Section } from "../ui/Section";
import { useAdminMutation } from "../ui/useAdminMutation";
import { DestFields, destBody, type DestValues } from "./DestFields";
import { deleteDestConfirm, destsKey } from "./DestinationsPage";

const enc = encodeURIComponent;
const roleHref = (p: string, r: string) => nodeHref(`config/projects/${enc(p)}/agents/${enc(r)}`);
const mono = (s: string) => <Text span ff="monospace">{s}</Text>;

// DestinationPage is one destination: how Jam brokers it, what an agent
// sets to use it, the roles that list it, and its editor.
export function DestinationPage() {
  const { name = "" } = useParams();
  const here = useLocation();
  const key = ["admin", "destination", name];
  const { data, error, isPending } = useQuery<DestDetail, ApiError>({ queryKey: key, queryFn: () => adminApi.get(`destinations/${enc(name)}`) });
  if (isPending) return <Loader size="sm" />;
  if (error?.code === "not_found") {
    return <Stack gap="xs"><Title order={2}>Destination not found</Title>
      <Text size="sm">No destination {mono(name)}. <Anchor component={Link} to={nodeHref("config/destinations")}>Back to destinations</Anchor></Text></Stack>;
  }
  if (error) return <Problem error={error} here={here.pathname} />;
  return <Destination key={data.name} d={data} dataKey={key} />;
}

function Destination({ d, dataKey }: { d: DestDetail; dataKey: unknown[] }) {
  const client = useQueryClient();
  const navigate = useNavigate();
  const f = d.fields;
  const drift = driftNote(d.managed);
  const save = useAdminMutation((v: DestValues) => adminApi.post<DestDetail>(`destinations/${enc(d.name)}`, destBody(v)), {
    invalidate: [destsKey], onSuccess: (x) => client.setQueryData(dataKey, x),
  });
  const remove = useAdminMutation(() => adminApi.del(`destinations/${enc(d.name)}`), { invalidate: [destsKey], onSuccess: () => void navigate(nodeHref("config/destinations")) });
  const logging = f.logHeadersIn !== "none" || f.logHeadersOut !== "none";
  return (
    <Stack>
      <Group justify="space-between" wrap="wrap">
        <Group gap="sm" align="baseline">
          <Title order={2}>{d.name}</Title>
          <ManagedBadge managed={d.managed} />
          <Text size="sm" c="dimmed" ff="monospace">{f.route} → {f.upstream}</Text>
        </Group>
        <ConfirmAction label="Delete" confirm={deleteDestConfirm(d.name, d.uses.length, drift)} onConfirm={() => remove.mutateAsync(undefined)} />
      </Group>
      {d.conflicts.length > 0 && (
        <Alert color="red" title={`Connector conflict in ${d.conflictRoles.length} role(s): ${d.conflictRoles.join(", ")}`}>
          <Text size="sm">Agents holding these roles can't assemble a connector (Jam fails closed with 409) until one side changes:</Text>
          <List size="sm">{d.conflicts.map((c, i) => (
            <List.Item key={i}><Anchor component={Link} to={roleHref(c.project, c.role)}>{c.project}/{c.role}</Anchor>: this and <Anchor component={Link} to={nodeHref("config/destinations/" + enc(c.other))}>{c.other}</Anchor> {c.what}</List.Item>
          ))}</List>
        </Alert>
      )}
      <SimpleGrid cols={{ base: 1, md: 2 }}>
        <Section title="Broker" sub="how Jam proxies and authenticates the request">
          <Facts rows={[
            ["Route", mono(f.route)],
            ["Upstream", mono(f.upstream)],
            ["Identity in", f.identityIn ? mono(f.identityIn) : null],
            ...(d.identityInSpec ? [["Identity-in spec", mono(d.identityInSpec)] as [string, React.ReactNode]] : []),
            ["Apply as", f.apply ? mono(f.apply) : null],
            ...(d.applySpec ? [["Apply spec", mono(d.applySpec)] as [string, React.ReactNode]] : []),
            ["Default credential", f.credName ? mono(f.credName) : null, "none"],
            ["Allowed paths", d.allowPaths.length ? <>{d.allowPaths.map((p) => <div key={p}>{mono(p)}</div>)}</> : null, "any"],
            ["Note", f.note || null, "none"],
            ["Header logging", logging ? <Group gap={4}><Badge size="sm">on</Badge>{mono(`in ${f.logHeadersIn} · out ${f.logHeadersOut}`)}<Text span size="xs" c="dimmed">(secrets redacted; turn off when done)</Text></Group> : null, "off"],
          ]} />
        </Section>
        <Section title="Agent connector" sub="what an agent sets to use this destination">
          <Stack gap="xs">
            <DataTable rows={d.env} rowKey={(e) => e.key} empty="No client env." columns={[
              { key: "k", label: "Env", render: (e) => mono(e.key) },
              { key: "t", label: "Template", render: (e) => mono(e.template) },
            ]} />
            {d.envImplied && <Text size="sm" c="dimmed">No env declared — implied by the {f.route} route (legacy default).</Text>}
            <Text size="sm">Git routing: {d.gitImplied ? <><Badge size="sm">git</Badge> implied by the /git/ route</> : d.gitRouted ? <><Badge size="sm">git</Badge> on</> : <Text span c="dimmed">off</Text>}</Text>
          </Stack>
        </Section>
      </SimpleGrid>
      <Section title="Used by" sub="roles whose scope lists this destination, and the credential each injects">
        <Stack gap="xs">
          <DataTable rows={d.uses} rowKey={(u) => `${u.project}/${u.role}`} empty="No role lists this destination." columns={[
            { key: "r", label: "Role", render: (u) => <Anchor component={Link} to={roleHref(u.project, u.role)} ff="monospace" size="sm">{u.project}/{u.role}</Anchor> },
            { key: "c", label: "Credential", render: (u) => u.credIsRole ? <>{mono(u.cred)} <Text span size="sm" c="dimmed">role mapping</Text></>
              : f.credName ? <>{mono(f.credName)} <Text span size="sm" c="dimmed">default</Text></> : <Text span size="sm" c="dimmed">none</Text> },
          ]} />
          <details>
            <summary style={{ cursor: "pointer" }}>Edit destination</summary>
            <Card withBorder mt="xs"><EditDest key={JSON.stringify(f)} d={d} confirm={drift ? `${drift} Save anyway?` : undefined} onSave={(v) => save.mutateAsync(v)} /></Card>
          </details>
        </Stack>
      </Section>
    </Stack>
  );
}

function EditDest({ d, confirm, onSave }: { d: DestDetail; confirm?: string; onSave: (v: DestValues) => Promise<unknown> }) {
  const [v, setV] = useState<DestValues>({ ...d.fields });
  return (
    <EditForm submit="Save destination" confirm={confirm} onSubmit={() => onSave(v)}>
      <DestFields value={v} onChange={setV} methods={d.methods} stored={d.fields} />
    </EditForm>
  );
}
