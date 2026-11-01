import { Alert, Anchor, Group, Loader, NativeSelect, SimpleGrid, Stack, Text, TextInput, Textarea, Title } from "@mantine/core";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useLocation, useNavigate, useParams } from "react-router";
import type { ApiError } from "../../api/client";
import { Problem } from "../../Problem";
import type { UserDetail } from "../api/types";
import { useConfigEditable } from "../branch/branch";
import { adminApi, nodeHref } from "../paths";
import { ConfirmAction } from "../ui/ConfirmAction";
import { DataTable } from "../ui/DataTable";
import { EditForm } from "../ui/EditForm";
import { Section } from "../ui/Section";
import { useAdminMutation } from "../ui/useAdminMutation";
import { AppTokens } from "./AppTokens";
import { usersKey } from "./UsersPage";

const enc = encodeURIComponent;

// UserPage is one user (by name or id): rename, logins, OIDC identities,
// accounts, remove. A removed user's page is read-only.
export function UserPage() {
  const { user: ref = "" } = useParams();
  const here = useLocation();
  const key = ["admin", "user", ref];
  const { data, error, isPending } = useQuery<UserDetail, ApiError>({ queryKey: key, queryFn: () => adminApi.get(`users/${enc(ref)}`) });
  if (isPending) return <Loader size="sm" />;
  if (error?.code === "not_found") {
    return (
      <Stack gap="xs">
        <Title order={2}>User not found</Title>
        <Text size="sm">No user <Text span ff="monospace">{ref}</Text>. <Anchor component={Link} to={nodeHref("config/users")}>Back to users</Anchor></Text>
      </Stack>
    );
  }
  if (error) return <Problem error={error} here={here.pathname} />;
  // Keyed by user: another user's page starts with fresh forms.
  return <UserBody key={data.user.id} d={data} refKey={key} byName={ref !== data.user.id} />;
}

function UserBody({ d, refKey, byName }: { d: UserDetail; refKey: unknown[]; byName: boolean }) {
  const u = d.user;
  const client = useQueryClient();
  const navigate = useNavigate();
  const base = `users/${enc(u.id)}`;
  const set = (next: UserDetail) => client.setQueryData(refKey, next);
  const write = useAdminMutation((v: { path: string; body: unknown; del?: boolean }) =>
    v.del ? adminApi.del<UserDetail>(v.path) : adminApi.post<UserDetail>(v.path, v.body), { invalidate: [usersKey], onSuccess: set });
  const remove = useAdminMutation(() => adminApi.del(base), { invalidate: [usersKey], onSuccess: () => void navigate(nodeHref("config/users")) });
  const editable = useConfigEditable();
  const [name, setName] = useState(u.name);
  const [logins, setLogins] = useState(d.loginSpec);
  const [oidc, setOidc] = useState(d.oidcSpec);
  const [conn, setConn] = useState(d.connections[0]?.name ?? "");
  const [uid, setUid] = useState("");
  const [handle, setHandle] = useState("");
  return (
    <Stack>
      <Group justify="space-between" wrap="wrap">
        <Group gap="sm" align="baseline">
          <Title order={2} ff="monospace">{u.name}</Title>
          <Text size="sm" c="dimmed" ff="monospace">{u.id}{d.removed && " · removed"}</Text>
        </Group>
        {!d.removed && <ConfirmAction label="Remove user" onConfirm={() => remove.mutateAsync(undefined)}
          confirm={`Remove user ${u.name}? They leave every project; history keeps showing them as removed.`} />}
      </Group>
      {d.removed ? (
        <Alert color="gray">This user was removed. Their id still labels history as {u.name} (removed); nothing here can be changed.</Alert>
      ) : (
        <SimpleGrid cols={{ base: 1, md: 2 }}>
          {editable && <Section title="Name" sub="Jam-wide; escalation tiers still name people (human:<name>) — update them after a rename">
            <EditForm submit="Rename" onSubmit={async () => {
              await write.mutateAsync({ path: `${base}/name`, body: { name } });
              // Opened by name: that name is gone now; the id's page stays.
              if (byName) void navigate(nodeHref("config/users/" + enc(u.id)), { replace: true });
            }}>
              <TextInput label="Name" required value={name} onChange={(e) => setName(e.currentTarget.value)} />
            </EditForm>
          </Section>}
          <Section title="Projects" sub="add or remove members on the project page">
            {u.projects.length ? <Group gap="xs">{u.projects.map((p) => <Anchor key={p} component={Link} to={nodeHref("config/projects/" + enc(p))}>{p}</Anchor>)}</Group>
              : <Text c="dimmed" size="sm">Not a member of any project.</Text>}
          </Section>
          <Section title="Logins" sub="operator identities this person signs in as — they own personal agents">
            {editable ? (
              <EditForm submit="Save logins" onSubmit={() => write.mutateAsync({ path: `${base}/logins`, body: { logins } })}>
                <Textarea label="Logins" description="one per line; the set is replaced" autosize minRows={2} value={logins} onChange={(e) => setLogins(e.currentTarget.value)} />
              </EditForm>
            ) : u.logins.length ? <Stack gap={2}>{u.logins.map((l) => <Text key={l} size="sm" ff="monospace">{l}</Text>)}</Stack>
              : <Text c="dimmed" size="sm">No logins.</Text>}
          </Section>
          <Section title="OIDC identities" sub="browser logins mapped to this person (the Inbox)">
            <EditForm live submit="Save identities" onSubmit={() => write.mutateAsync({ path: `${base}/oidc`, body: { oidc } })}>
              <Textarea label="Identities" description="one per line, issuer:subject; the set is replaced" autosize minRows={2} value={oidc} onChange={(e) => setOidc(e.currentTarget.value)} />
            </EditForm>
          </Section>
        </SimpleGrid>
      )}
      {!d.removed && (
        <Section title="Accounts" sub="this person on connected services: a tracker handle, a Discord user id">
          <DataTable rows={u.accounts} rowKey={(a) => a.id} empty="No accounts." columns={[
            { key: "c", label: "Connection", render: (a) => a.connection },
            { key: "uid", label: "User id", render: (a) => <Text size="sm" ff="monospace">{a.serviceUid || "—"}</Text> },
            { key: "h", label: "Handle", render: (a) => <Text size="sm" ff="monospace">{a.handle ? `@${a.handle}` : "—"}</Text> },
            { key: "id", label: "Account", render: (a) => <Text size="sm" ff="monospace">{a.id}</Text> },
            { key: "x", label: "", render: (a) => <ConfirmAction live label="Unlink" confirm={`Unlink this ${a.connection} account from ${u.name}?`}
              onConfirm={() => write.mutateAsync({ path: `${base}/accounts/${enc(a.id)}`, body: undefined, del: true })} /> },
          ]} />
          <details>
            <summary style={{ cursor: "pointer" }}>Add account</summary>
            <EditForm live submit="Add account" onSubmit={() => write.mutateAsync({ path: `${base}/accounts`, body: { connection: conn, uid, handle } })}
              resetOnSuccess={() => { setUid(""); setHandle(""); }}>
              <NativeSelect label="Connection" required value={conn} onChange={(e) => setConn(e.currentTarget.value)}
                data={d.connections.map((c) => ({ value: c.name, label: `${c.name} (${c.kind})` }))} />
              <TextInput label="User id" description="the service's id, e.g. a Discord user id" value={uid} onChange={(e) => setUid(e.currentTarget.value)} />
              <TextInput label="Handle" description="the service's mention handle, without the @" value={handle} onChange={(e) => setHandle(e.currentTarget.value)} />
            </EditForm>
          </details>
        </Section>
      )}
      {!d.removed && (
        <Section title="Connected apps" sub="apps (VS Code) signed in as this person; revoking signs them out">
          <AppTokens user={u} tokens={d.appTokens} refKey={refKey} />
        </Section>
      )}
    </Stack>
  );
}
