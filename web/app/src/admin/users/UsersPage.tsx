import { Anchor, Badge, Card, Group, Loader, Stack, Text, TextInput, Textarea, Title } from "@mantine/core";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useLocation, useNavigate } from "react-router";
import type { ApiError } from "../../api/client";
import { Problem } from "../../Problem";
import type { Created, UserRow, UsersResponse } from "../api/types";
import { useConfigEditable } from "../branch/branch";
import { adminApi, nodeHref } from "../paths";
import { DataTable } from "../ui/DataTable";
import { EditForm } from "../ui/EditForm";
import { useAdminMutation } from "../ui/useAdminMutation";

export const usersKey = ["admin", "users"];

// account is how an account reads in a list: its connection and handle (or id).
export function AccountChip({ a }: { a: UserRow["accounts"][number] }) {
  return <Group gap={4} wrap="nowrap"><Badge size="sm" variant="light" color="gray">{a.connection}</Badge><Text size="sm" ff="monospace">{a.handle ? `@${a.handle}` : a.serviceUid}</Text></Group>;
}

// UsersPage is Config › Users: everyone agents talk to, and New user.
export function UsersPage() {
  const { data, error, isPending } = useQuery<UsersResponse, ApiError>({ queryKey: usersKey, queryFn: () => adminApi.get("users") });
  const here = useLocation();
  const navigate = useNavigate();
  const editable = useConfigEditable();
  const [name, setName] = useState("");
  const [logins, setLogins] = useState("");
  const [oidc, setOidc] = useState("");
  const create = useAdminMutation((v: { name: string; logins: string; oidc: string }) => adminApi.post<Created>("users", v), {
    invalidate: [usersKey], onSuccess: (c) => void navigate(nodeHref(c.path)),
  });
  return (
    <Stack>
      <div>
        <Title order={2}>Users</Title>
        <Text size="sm" c="dimmed">The people agents talk to — open one to edit their logins, OIDC bindings and accounts; add them to a project on its page</Text>
      </div>
      {editable && <details>
        <summary style={{ cursor: "pointer" }}>New user</summary>
        <Card withBorder mt="xs">
          <EditForm submit="Create user" onSubmit={() => create.mutateAsync({ name, logins, oidc })}>
            <TextInput label="Name" description="Jam-wide; addressed as human:<name>" required value={name} onChange={(e) => setName(e.currentTarget.value)} />
            <Textarea label="Logins" description="one per line — operator identities (OIDC sub, or local) this person signs in as" autosize minRows={2} value={logins} onChange={(e) => setLogins(e.currentTarget.value)} />
            <Textarea label="OIDC identities" description="one per line, issuer:subject" autosize minRows={2} value={oidc} onChange={(e) => setOidc(e.currentTarget.value)} />
          </EditForm>
        </Card>
      </details>}
      {isPending ? <Loader size="sm" /> : error ? <Problem error={error} here={here.pathname} /> : (
        <DataTable rows={data.users} rowKey={(u) => u.id} empty="No users." columns={[
          { key: "name", label: "Name", render: (u) => <Anchor component={Link} to={nodeHref("config/users/" + encodeURIComponent(u.id))} fw={600}>{u.name}</Anchor> },
          { key: "logins", label: "Logins", render: (u) => u.logins.length ? u.logins.map((l) => <Text key={l} size="sm" ff="monospace">{l}</Text>) : <Text c="dimmed">—</Text> },
          { key: "accounts", label: "Accounts", render: (u) => u.accounts.length ? u.accounts.map((a) => <AccountChip key={a.id} a={a} />) : <Text c="dimmed">—</Text> },
          { key: "projects", label: "Projects", render: (u) => u.projects.length ? <Group gap={4}>{u.projects.map((p) => <Anchor key={p} component={Link} to={nodeHref("config/projects/" + encodeURIComponent(p))} size="sm">{p}</Anchor>)}</Group> : <Text c="dimmed">—</Text> },
        ]} />
      )}
    </Stack>
  );
}
