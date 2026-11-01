import { Anchor, Badge, Button, Card, Checkbox, Group, Loader, Stack, Text, TextInput, Textarea, Title } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useLocation, useNavigate } from "react-router";
import type { ApiError } from "../../api/client";
import { Problem } from "../../Problem";
import type { Created, KitsResponse } from "../api/types";
import { useConfigEditable } from "../branch/branch";
import { adminApi, nodeHref } from "../paths";
import { AttnFlag } from "../ui/AttnFlag";
import { ConfirmAction } from "../ui/ConfirmAction";
import { DataTable } from "../ui/DataTable";
import { EditForm } from "../ui/EditForm";
import { useAdminMutation } from "../ui/useAdminMutation";

export const kitsKey = ["admin", "kits"];
const enc = encodeURIComponent;
export const mono = { fontFamily: "var(--mantine-font-family-monospace)" };

// deleteKitConfirm is a kit's Delete confirm.
export const deleteKitConfirm = (name: string) => `Delete kit ${name} and all its versions?`;

// KitsPage is Config › Kits: versioned studio kits, New kit and Import.
export function KitsPage() {
  const { data, error, isPending } = useQuery<KitsResponse, ApiError>({ queryKey: kitsKey, queryFn: () => adminApi.get("kits") });
  const here = useLocation();
  const navigate = useNavigate();
  const editable = useConfigEditable();
  const [name, setName] = useState("");
  const [config, setConfig] = useState("");
  const [imp, setImp] = useState({ source: "", name: "", track: true });
  const open = (c: Created) => {
    if (c.message) notifications.show({ color: "green", message: c.message });
    void navigate(nodeHref(c.path));
  };
  const create = useAdminMutation(() => adminApi.post<Created>("kits", { name, config }), { invalidate: [kitsKey], onSuccess: open });
  const importKit = useAdminMutation(() => adminApi.post<Created>("kits/import", imp), { invalidate: [kitsKey], onSuccess: open });
  const remove = useAdminMutation((k: string) => adminApi.del(`kits/${enc(k)}`), { invalidate: [kitsKey] });
  return (
    <Stack>
      <div>
        <Title order={2}>Kits</Title>
        <Text size="sm" c="dimmed">Versioned studio kits — open one to see its versions, diff them, or push a new one</Text>
      </div>
      {editable && <details>
        <summary style={{ cursor: "pointer" }}>New kit</summary>
        <Card withBorder mt="xs">
          <EditForm submit="Create kit" onSubmit={() => create.mutateAsync(undefined)}>
            <TextInput label="Kit name" description="tag-safe: letters, digits, _ . -" required pattern="[A-Za-z0-9_.\-]+" autoComplete="off" value={name} onChange={(e) => setName(e.currentTarget.value)} />
            <Textarea label="Studio kit YAML" description="kind: studio with base, egress, build-args, secrets, prompt — no name: (the kit name above names it)" required autosize minRows={8}
              spellCheck={false} styles={{ input: mono }} placeholder={"kind: studio\negress: [github.com]\nprompt: You work on …"} value={config} onChange={(e) => setConfig(e.currentTarget.value)} />
          </EditForm>
        </Card>
      </details>}
      {data?.canSource && (
        <details>
          <summary style={{ cursor: "pointer" }}>Import from git</summary>
          <Card withBorder mt="xs">
            <EditForm live submit="Import" onSubmit={() => importKit.mutateAsync(undefined)}>
              <TextInput label="Source" description="https://host/org/repo[//dir]@ref — a kit.yml in the repo (or dir); imported as a new version" required autoComplete="off" spellCheck={false}
                placeholder="https://github.com/acme/kits//web@main" value={imp.source} onChange={(e) => setImp({ ...imp, source: e.currentTarget.value })} />
              <TextInput label="Kit name" description="optional — defaults to the kit's own name, then its directory's or repo's" pattern="[A-Za-z0-9_.\-]*" autoComplete="off"
                value={imp.name} onChange={(e) => setImp({ ...imp, name: e.currentTarget.value })} />
              <Checkbox label="Track this source" description="later Syncs pull new commits from it" checked={imp.track} onChange={(e) => setImp({ ...imp, track: e.currentTarget.checked })} />
            </EditForm>
          </Card>
        </details>
      )}
      {isPending ? <Loader size="sm" /> : error ? <Problem error={error} here={here.pathname} /> : (
        <DataTable rows={data.kits} rowKey={(k) => k.name} empty="No kits." columns={[
          { key: "n", label: "Kit", render: (k) => (
            <Group gap={6}>
              <Anchor component={Link} to={nodeHref("config/kits/" + enc(k.name))} fw={600} ff="monospace">{k.name}</Anchor>
              <AttnFlag flag={k.attn} />
              {k.draft && <Badge size="sm" variant="light" color="gray" title="An open draft">draft</Badge>}
            </Group>
          ) },
          { key: "c", label: "Current", render: (k) => <Group gap={4}><Badge size="sm">v{k.current}</Badge><Text size="sm" c="dimmed">of {k.count}</Text></Group> },
          { key: "b", label: "Base", render: (k) => k.invalid ? <Badge size="sm" color="red" variant="light" title="The current version is not a valid studio kit">invalid</Badge> : k.base },
          { key: "e", label: "Egress", render: (k) => k.invalid ? <Text c="dimmed" size="sm">—</Text> : `${k.egress} domain(s)` },
          { key: "r", label: "Roles", render: (k) => k.usedBy || <Text span c="dimmed">0</Text> },
          { key: "d", label: "", render: (k) => !editable ? null : k.usedBy
            ? <Button size="xs" color="red" variant="light" disabled title="Roles use this kit — rebind them first">Delete</Button>
            : <ConfirmAction label="Delete" confirm={deleteKitConfirm(k.name)} onConfirm={() => remove.mutateAsync(k.name)} /> },
        ]} />
      )}
    </Stack>
  );
}
