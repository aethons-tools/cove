import { Anchor, Badge, Button, Card, Group, Loader, SimpleGrid, Stack, Text, TextInput, Textarea, Title } from "@mantine/core";
import { useState, type ReactNode } from "react";
import { Link, useLocation, useNavigate, useParams } from "react-router";
import { Problem } from "../../Problem";
import type { RoleDetail } from "../api/types";
import { adminApi, nodeHref } from "../paths";
import { AttnFlag } from "../ui/AttnFlag";
import { ConfirmAction } from "../ui/ConfirmAction";
import { ContextPanel } from "../ui/ContextPanel";
import { CovesTable } from "../ui/CovesTable";
import { DataTable } from "../ui/DataTable";
import { EditForm } from "../ui/EditForm";
import { ManagedBadge, driftNote } from "../ui/ManagedBadge";
import { Section } from "../ui/Section";
import { SetupView } from "../ui/SetupView";
import { StatusBadge } from "../ui/StatusBadge";
import { TypeAhead } from "../ui/TypeAhead";
import { useAdminMutation } from "../ui/useAdminMutation";
import { useRequest } from "./AgentsPage";
import { useRole, useRoleWrite } from "./useRole";

const enc = encodeURIComponent;
const edit = (label: string, body: ReactNode) => <details><summary style={{ cursor: "pointer" }}>{label}</summary><Card withBorder mt="xs">{body}</Card></details>;

// RolePage is a role: what its agents reach and run with, its standing
// agents and holders, and (when inspectable) its Setup.
export function RolePage() {
  const { project = "", role = "" } = useParams();
  const here = useLocation();
  const { data, error, isPending } = useRole(project, role);
  if (isPending) return <Loader size="sm" />;
  if (error?.code === "not_found") {
    return (
      <Stack gap="xs">
        <Title order={2}>Role not found</Title>
        <Text size="sm">No role <Text span ff="monospace">{role}</Text> in project <Text span ff="monospace">{project}</Text>. <Anchor component={Link} to={nodeHref(`config/projects/${enc(project)}/agents`)}>Back to roles</Anchor></Text>
      </Stack>
    );
  }
  if (error) return <Problem error={error} here={here.pathname} />;
  return <Role key={`${data.project}/${data.name}`} d={data} />;
}

function Role({ d }: { d: RoleDetail }) {
  const navigate = useNavigate();
  const p = d.project, r = d.name;
  const write = useRoleWrite(p, r);
  const request = useRequest(p);
  const drift = driftNote(d.managed);
  const save = drift ? `${drift} Save anyway?` : undefined;
  const remove = useAdminMutation(() => adminApi.del(`projects/${enc(p)}/roles/${enc(r)}`), {
    invalidate: [["admin", "project-agents", p]], onSuccess: () => void navigate(nodeHref(`config/projects/${enc(p)}/agents`)),
  });
  const post = (path: string, body?: unknown) => write.mutateAsync({ path, body });
  const del = (path: string) => write.mutateAsync({ path, del: true });
  return (
    <Stack>
      <Group justify="space-between" wrap="wrap">
        <Group gap="sm" align="baseline" wrap="wrap">
          <Title order={2}>{r}</Title>
          <ManagedBadge managed={d.managed} />
          <AttnFlag flag={d.attn} />
          <Anchor component={Link} to={nodeHref("config/projects/" + enc(p))} size="sm">{p}</Anchor>
          {d.kit ? <Text size="sm">kit <Anchor component={Link} to={nodeHref("config/kits/" + enc(d.kit))}>{d.kit}</Anchor></Text> : <Text size="sm" c="dimmed">no kit</Text>}
          <Text size="sm">model-spec <Anchor component={Link} to={nodeHref("config/model-specs/" + enc(d.modelSpec))}>{d.modelSpec}</Anchor>{d.modelSpecDefault && <Text span c="dimmed"> default</Text>}</Text>
          <Text size="sm">TTL {d.ttl}</Text>
        </Group>
        <Group gap="xs">
          {d.canRequest && <Button size="xs" title="Raise a personal agent of this role for you" loading={request.isPending} onClick={() => request.mutate(r)}>Request agent</Button>}
          <ConfirmAction label="Delete" confirm={`Delete role ${r} from ${p}?${drift ? " " + drift : ""}`} onConfirm={() => remove.mutateAsync(undefined)} />
        </Group>
      </Group>
      <Section title="Scope" sub="what the broker lets it reach, with which credential, and who it may squawk">
        <Stack gap="xs">
          <DataTable rows={d.dests} rowKey={(x) => x.name} empty="No destinations." columns={[
            { key: "n", label: "Destination", render: (x) => <Group gap={4}><Text size="sm" ff="monospace">{x.name}</Text>{x.missing && <Badge size="sm" color="red" variant="light" title="Not a registered destination">unregistered</Badge>}</Group> },
            { key: "c", label: "Credential", render: (x) => x.cred ? <Group gap={4}><Text size="sm" ff="monospace">{x.cred}</Text><Text size="sm" c="dimmed">{x.credIsRole ? "role mapping" : "destination default"}</Text></Group> : <Text size="sm" c="dimmed">none</Text> },
            { key: "r", label: "Route", render: (x) => x.route ? <Text size="sm" ff="monospace">{x.route}</Text> : <Text size="sm" c="dimmed">—</Text> },
          ]} />
          <Group gap="xs"><Text size="sm" fw={600}>Addressing</Text>
            {d.addressing.length ? d.addressing.map((a) => <Badge key={a} size="sm" variant="light" color="gray" tt="none">{a}</Badge>) : <Text size="sm" c="dimmed">No comms targets — it can't squawk anyone.</Text>}
          </Group>
          {edit("Edit scope", <ScopeForm key={JSON.stringify(d.form)} d={d} confirm={save} onSave={(b) => post("scope", b)} />)}
        </Stack>
      </Section>
      <SimpleGrid cols={{ base: 1, md: 2 }}>
        <Section title="Egress" sub="raw outbound domains, within the kit's ceiling">
          <Stack gap="xs">
            {d.egressManaged
              ? (d.egressDomains.length ? <Group gap={4}>{d.egressDomains.map((x) => <Badge key={x} size="sm" variant="light" color="gray" tt="none">{x}</Badge>)}</Group>
                : <Text size="sm" c="dimmed">Managed, empty — nothing beyond the sealed base and the kit's infra domains.</Text>)
              : <Text size="sm" c="dimmed">Kit default — the role sets no egress policy.</Text>}
            {edit("Edit egress", <EgressForm key={d.form.egress} d={d} confirm={save} drift={drift} onSave={(domains) => post("egress", { domains })} onReset={() => del("egress")} />)}
          </Stack>
        </Section>
        <Section title="Allocation" sub="agent caps and the personal-agent idle ladder">
          <Stack gap="xs">
            {d.allocation.map((s) => <Group key={s.label} gap="md" wrap="nowrap"><Text size="sm" c="dimmed" w={170}>{s.label}</Text><Text size="sm" c={s.unset ? "dimmed" : undefined}>{s.value}</Text></Group>)}
            {edit("Edit allocation", <AllocationForm key={JSON.stringify(d.form)} d={d} confirm={save} onSave={(b) => post("allocation", b)} />)}
          </Stack>
        </Section>
      </SimpleGrid>
      <ContextPanel key={d.context.yaml} panel={d.context} onSave={(yaml) => post("context", { yaml })} onClear={() => del("context")} />
      <Section title="Standing agents" sub="kept running, one per name">
        <Stack gap="xs">
          <DataTable rows={d.standing} rowKey={(s) => s.name} empty="No standing agents." columns={[
            { key: "n", label: "Name", render: (s) => <Text size="sm" ff="monospace">{s.name}</Text> },
            { key: "p", label: "Prompt", render: (s) => <Text size="sm">{s.prompt}</Text> },
            { key: "a", label: "Agent", render: (s) => (
              <Group gap={4}>
                {s.status ? <StatusBadge status={s.status} /> : <Text size="sm" c="dimmed">not running</Text>}
                {s.image === "stale" && <Badge size="sm" color="yellow" variant="light" title="raised on an older image than this role would run now">image stale</Badge>}
                {s.upgrade && <Badge size="sm" color="yellow" variant="light" title="pending upgrade (Jam restarts it on the current image once ready and idle)">upgrade: {s.upgrade}</Badge>}
                <Text size="xs" c="dimmed" ff="monospace">{s.actorId}</Text>
              </Group>
            ) },
            { key: "x", label: "", render: (s) => (
              <Group gap={4} wrap="nowrap">
                {d.canRequest && <>
                  <ConfirmAction label="Upgrade" color={s.image === "stale" ? "jam" : "gray"} confirm={`Upgrade standing agent ${s.name}? It restarts on the current image; its conversation and workspace are kept.`}
                    onConfirm={() => post(`standing/${enc(s.name)}/upgrade`)} />
                  <ConfirmAction label="Reset" color="gray" confirm={`Reset standing agent ${s.name}? Its conversation and workspace are deleted; Jam raises it fresh.`}
                    onConfirm={() => post(`standing/${enc(s.name)}/reset`)} />
                </>}
                <ConfirmAction label="Dismiss" confirm={`Dismiss standing agent ${s.name}? It stops, and its conversation and workspace are deleted.${drift ? " " + drift : ""}`}
                  onConfirm={() => del(`standing/${enc(s.name)}`)} />
              </Group>
            ) },
          ]} />
          {edit("Declare standing agent", <StandingForm confirm={save} onSave={(b) => post("standing", b)} />)}
        </Stack>
      </Section>
      <Section title="Holders" sub="agents granted this role — open one to manage its grants">
        {d.holders.length ? (
          <Group gap={4}>{d.holders.map((h) => (
            <Badge key={h.id} variant="light" color="gray" tt="none">
              <Anchor component={Link} to={nodeHref("agents/" + enc(h.id))} size="xs" inherit>{h.id}</Anchor>
              {h.override && <Text span c="dimmed" size="xs" title="This grant overrides part of the role's scope"> override</Text>}
            </Badge>
          ))}</Group>
        ) : <Text size="sm" c="dimmed">No agents hold this role.</Text>}
      </Section>
      <Section title="Running" sub="this role's running agents"><CovesTable rows={d.running} /></Section>
      {d.setupEnabled && (
        <Section title="Setup" sub="what a studio of this role raised now would get">
          <SetupView path={`projects/${enc(p)}/roles/${enc(r)}/setup`} />
        </Section>
      )}
    </Stack>
  );
}

function ScopeForm({ d, confirm, onSave }: { d: RoleDetail; confirm?: string; onSave: (b: unknown) => Promise<unknown> }) {
  const [f, setF] = useState({ destinations: d.form.destinations, addressing: d.form.addressing, ttl: d.form.ttl, kit: d.form.kit, modelSpec: d.form.modelSpec });
  const set = (k: keyof typeof f) => (v: string) => setF({ ...f, [k]: v });
  return (
    <EditForm submit="Save scope" confirm={confirm} onSubmit={() => onSave(f)}>
      <TypeAhead label="Destinations" description="comma-separated; name=credential maps a credential, a bare name uses the destination's default" kind="destinations" list value={f.destinations} onChange={set("destinations")} />
      <TypeAhead label="Addressing" description="comms target globs, e.g. human:*,channel:eng; empty = can't squawk anyone" kind="targets" project={d.project} list value={f.addressing} onChange={set("addressing")} />
      <TextInput label="TTL" description="e.g. 1h, 90m; blank = none" value={f.ttl} onChange={(e) => set("ttl")(e.currentTarget.value)} />
      <TypeAhead label="Kit" kind="kits" placeholder="blank = no kit (the default kit)" value={f.kit} onChange={set("kit")} />
      <TypeAhead label="Model-spec" description="how its coves run the agent; takes effect at each cove's next episode" kind="model-specs" placeholder="blank = claude-default" value={f.modelSpec} onChange={set("modelSpec")} />
    </EditForm>
  );
}

function EgressForm({ d, confirm, drift, onSave, onReset }: { d: RoleDetail; confirm?: string; drift: string; onSave: (domains: string) => Promise<unknown>; onReset: () => Promise<unknown> }) {
  const [domains, setDomains] = useState(d.form.egress);
  return (
    <Stack gap="xs">
      <EditForm submit="Save egress" confirm={confirm} onSubmit={() => onSave(domains)}>
        <Textarea label="Domains" description="one per line; a leading dot adds subdomains (.example.com); saving an empty list allows nothing extra" autosize minRows={3} spellCheck={false}
          value={domains} onChange={(e) => setDomains(e.currentTarget.value)} />
      </EditForm>
      {d.egressManaged && <Group><ConfirmAction label="Reset to kit default" confirm={`Drop ${d.name}'s egress policy and use the kit default?${drift ? " " + drift : ""}`} onConfirm={onReset} /></Group>}
    </Stack>
  );
}

function AllocationForm({ d, confirm, onSave }: { d: RoleDetail; confirm?: string; onSave: (b: unknown) => Promise<unknown> }) {
  const [f, setF] = useState({ maxEphemeral: d.form.maxEphemeral, maxPersonal: d.form.maxPersonal, maxPersonalPerOwner: d.form.maxPersonalPerOwner,
    idleAfter: d.form.idleAfter, nagEvery: d.form.nagEvery, reclaimAfter: d.form.reclaimAfter });
  const field = (k: keyof typeof f, label: string, placeholder?: string) =>
    <TextInput label={label} placeholder={placeholder} value={f[k]} onChange={(e) => setF({ ...f, [k]: e.currentTarget.value })} />;
  return (
    <EditForm submit="Save allocation" confirm={confirm} onSubmit={() => onSave(f)}>
      <Text size="sm" c="dimmed">Leave a field blank to unset it (the value shown above for unset applies).</Text>
      <SimpleGrid cols={{ base: 1, sm: 3 }}>
        {field("maxEphemeral", "Max ephemeral")}
        {field("maxPersonal", "Max personal")}
        {field("maxPersonalPerOwner", "Max personal per owner")}
        {field("idleAfter", "Idle after", "4h")}
        {field("nagEvery", "Nag every", "24h")}
        {field("reclaimAfter", "Reclaim after", "never")}
      </SimpleGrid>
    </EditForm>
  );
}

function StandingForm({ confirm, onSave }: { confirm?: string; onSave: (b: unknown) => Promise<unknown> }) {
  const [name, setName] = useState("");
  const [prompt, setPrompt] = useState("");
  return (
    <EditForm submit="Declare" confirm={confirm} onSubmit={() => onSave({ name, prompt })} resetOnSuccess={() => { setName(""); setPrompt(""); }}>
      <TextInput label="Name" required value={name} onChange={(e) => setName(e.currentTarget.value)} />
      <Textarea label="Prompt" required autosize minRows={2} value={prompt} onChange={(e) => setPrompt(e.currentTarget.value)} />
    </EditForm>
  );
}
