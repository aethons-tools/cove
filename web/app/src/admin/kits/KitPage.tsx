import { Alert, Anchor, Badge, Button, Card, Grid, Group, Loader, Stack, Table, Text, TextInput, Textarea, Title } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useLocation, useNavigate, useParams, useSearchParams } from "react-router";
import type { ApiError } from "../../api/client";
import { Problem } from "../../Problem";
import type { KitDetail, KitResult } from "../api/types";
import { useConfigEditable } from "../branch/branch";
import { adminApi, nodeHref } from "../paths";
import { AttnFlag } from "../ui/AttnFlag";
import { ConfirmAction } from "../ui/ConfirmAction";
import { DiffView } from "../ui/DiffView";
import { EditForm } from "../ui/EditForm";
import { Facts } from "../ui/Facts";
import { Section } from "../ui/Section";
import { useAdminMutation } from "../ui/useAdminMutation";
import { FileCard, FilesCard, FilesDiffCard, VersionCard, hiddenNote, maskedNote } from "./KitParts";
import { deleteKitConfirm, kitsKey, mono } from "./KitsPage";
import { VersionRail } from "./VersionRail";
import { DraftSection } from "./DraftSection";

const enc = encodeURIComponent;

// KitPage is one kit: its versions (?v=), a diff between two (?diff=), a
// file (?file=) — in-page selectors, kept in the URL — and its writes.
export function KitPage() {
  const { kit: name = "" } = useParams();
  const [q] = useSearchParams();
  const here = useLocation();
  const v = q.get("v") ?? "", diff = q.get("diff") ?? "", file = q.get("file") ?? "";
  const qs = new URLSearchParams(Object.entries({ v, diff, file }).filter(([, x]) => x)).toString();
  const { data, error, isPending } = useQuery<KitDetail, ApiError>({
    queryKey: ["admin", "kit", name, v, diff, file], queryFn: () => adminApi.get(`kits/${enc(name)}${qs ? `?${qs}` : ""}`),
  });
  if (isPending) return <Loader size="sm" />;
  if (error?.code === "not_found") {
    return <Stack gap="xs"><Title order={2}>Kit not found</Title>
      <Text size="sm">No kit <Text span ff="monospace">{name}</Text>. <Anchor component={Link} to={nodeHref("config/kits")}>Back to kits</Anchor></Text></Stack>;
  }
  if (error) return <Problem error={error} here={here.pathname} />;
  return <Kit k={data} />;
}

function Kit({ k }: { k: KitDetail }) {
  const client = useQueryClient();
  const navigate = useNavigate();
  const base = nodeHref("config/kits/" + enc(k.name));
  const href = (q: Record<string, string | number>) => `${base}?${new URLSearchParams(Object.entries(q).map(([a, b]) => [a, String(b)]))}`;
  const editable = useConfigEditable();
  // A write answers with the kit at its current version: show that page.
  const done = (r: KitResult) => {
    client.setQueryData(["admin", "kit", k.name, "", "", ""], r.kit);
    void client.invalidateQueries({ queryKey: ["admin", "kit", k.name], refetchType: "none" });
    if (r.message) notifications.show({ color: "green", message: r.message });
    void navigate(base);
  };
  const write = useAdminMutation((x: { path: string; body?: unknown; del?: boolean }) =>
    x.del ? adminApi.del<KitResult>(`kits/${enc(k.name)}/${x.path}`) : adminApi.post<KitResult>(`kits/${enc(k.name)}/${x.path}`, x.body ?? {}), { invalidate: [kitsKey], onSuccess: done });
  const remove = useAdminMutation(() => adminApi.del(`kits/${enc(k.name)}`), { invalidate: [kitsKey], onSuccess: () => void navigate(nodeHref("config/kits")) });
  const del = !editable ? null : k.uses.length || k.usedByKits.length || k.isDefault
    ? <Button size="xs" color="red" variant="light" disabled title={k.isDefault ? "The built-in default kit is re-seeded at serve start" : "Roles or kits use this kit — rebind them first"}>Delete</Button>
    : <ConfirmAction label="Delete" confirm={deleteKitConfirm(k.name)} onConfirm={() => remove.mutateAsync(undefined)} />;
  const viewFile = (p: string) => href({ v: k.view.n, file: p });
  const diffFile = (p: string) => href({ v: k.diff?.to ?? k.view.n, diff: k.diff?.from ?? 0, file: p });
  return (
    <Stack>
      <Group justify="space-between" wrap="wrap">
        <Group gap="sm" align="baseline">
          <Title order={2}>{k.name}</Title>
          <AttnFlag flag={k.attn} />
          <Badge>current v{k.current}</Badge>
          <Text size="sm" c="dimmed">{k.versions.length} version(s)</Text>
          {k.draft && <Badge component="a" href="#draft" variant="light" color="gray" style={{ cursor: "pointer" }}>draft · base v{k.draft.base}</Badge>}
        </Group>
        {del}
      </Group>
      <Grid>
        <Grid.Col span={{ base: 12, md: 3 }}>
          <VersionRail k={k} href={href} onPin={(n) => write.mutateAsync({ path: "pin", body: { version: n } })} />
        </Grid.Col>
        <Grid.Col span={{ base: 12, md: 9 }}>
          <Stack>
            {k.diff && (
              <Section title={`Diff v${k.diff.from} → v${k.diff.to}`} sub={k.diff.digestChanged ? "build digest changed — a new image is built" : "same build digest — the cached image is reused"}
                actions={<Anchor component={Link} to={href({ v: k.diff.to })} size="sm">close</Anchor>}>
                {k.diff.tooLarge ? <Text size="sm" c="dimmed">Too large to diff here.</Text> : <DiffView lines={k.diff.lines} />}
              </Section>
            )}
            {k.filesDiff && <FilesDiffCard d={k.filesDiff} fileHref={diffFile} />}
            {k.file?.diff && <FileCard f={k.file} kit={k.name} />}
            <VersionCard k={k} />
            {k.files && <FilesCard f={k.files} fileHref={viewFile} />}
            {k.file && !k.file.diff && <FileCard f={k.file} kit={k.name} />}
            <Section title="Used by" sub="roles that raise this kit, and kits built on it">
              <Group gap={6}>
                {k.usedByKits.map((c) => <Badge key={c.name} variant="light" color="gray" tt="none"><Anchor component={Link} to={nodeHref("config/kits/" + enc(c.name))} size="xs" inherit>kit {c.name}</Anchor> <Text span size="xs" c="dimmed">{c.pinned ? "pinned" : "follows"}</Text></Badge>)}
                {k.uses.map((u) => <Badge key={`${u.project}/${u.role}`} variant="light" color="gray" tt="none"><Anchor component={Link} to={nodeHref(`config/projects/${enc(u.project)}/agents/${enc(u.role)}`)} size="xs" inherit>{u.project}/{u.role}</Anchor>{u.implicit && <Text span size="xs" c="dimmed"> no kit set</Text>}</Badge>)}
                {!k.uses.length && !k.usedByKits.length && <Text size="sm" c="dimmed">No role or kit uses this kit.</Text>}
              </Group>
            </Section>
            <SourceCard k={k} busy={write.isPending}
              onSync={() => write.mutateAsync({ path: "sync" })} onUntrack={() => write.mutateAsync({ path: "source", del: true })}
              onTrack={(source) => write.mutateAsync({ path: "source", body: { source } })} />
            <Section title="Versions" sub="and where each came from">
              <Table>
                <Table.Thead><Table.Tr><Table.Th>Version</Table.Th><Table.Th>Pushed</Table.Th><Table.Th>Source</Table.Th><Table.Th>Commit</Table.Th></Table.Tr></Table.Thead>
                <Table.Tbody>{k.versions.map((v) => (
                  <Table.Tr key={v.n}>
                    <Table.Td><Anchor component={Link} to={href({ v: v.n })}>v{v.n}</Anchor>{v.current && <Badge size="sm" ml={6}>current</Badge>}</Table.Td>
                    <Table.Td>{v.pushed || <Text span c="dimmed">—</Text>}</Table.Td>
                    <Table.Td>{v.source ? <>{repo(v.source)}{v.source.dir && <Text span ff="monospace"> //{v.source.dir}</Text>} <Text span ff="monospace">@{v.source.ref}</Text></> : <Text span c="dimmed">pushed</Text>}</Table.Td>
                    <Table.Td>{v.source ? commit(v.source) : <Text span c="dimmed">—</Text>}</Table.Td>
                  </Table.Tr>
                ))}</Table.Tbody>
              </Table>
            </Section>
            {editable && <>
              <DraftSection kit={k.name} />
              <Card withBorder>
                <details>
                  <summary style={{ cursor: "pointer", fontWeight: 600 }}>Push new version</summary>
                  <PushForm key={`${k.view.n}/${k.view.pushText}`} k={k} onPush={(config) => write.mutateAsync({ path: "versions", body: { config } })} />
                </details>
              </Card>
            </>}
          </Stack>
        </Grid.Col>
      </Grid>
    </Stack>
  );
}

type Src = NonNullable<KitDetail["source"]>;
const repo = (s: Src) => s.repoLink ? <Anchor href={s.repoLink} rel="noopener noreferrer" ff="monospace" size="sm">{s.repo}</Anchor> : <Text span ff="monospace" size="sm">{s.repo}</Text>;
const commit = (s: Src) => !s.commit ? <Text span c="dimmed">—</Text> : s.commitLink ? <Anchor href={s.commitLink} rel="noopener noreferrer" ff="monospace" size="sm">{s.commit}</Anchor> : <Text span ff="monospace" size="sm">{s.commit}</Text>;

function SourceCard({ k, busy, onSync, onUntrack, onTrack }: {
  k: KitDetail; busy: boolean; onSync: () => Promise<unknown>; onUntrack: () => Promise<unknown>; onTrack: (source: string) => Promise<unknown>;
}) {
  const [source, setSource] = useState("");
  const s = k.source;
  return (
    <Section title="Source" sub="where this kit is imported from">
      <Stack gap="xs">
        {s ? (
          <>
            <Facts rows={[["Repo", repo(s)], ...(s.dir ? [["Dir", <Text span ff="monospace">{s.dir}</Text>] as [string, React.ReactNode]] : []), ["Ref", <Text span ff="monospace">{s.ref}</Text>], ["Last synced", commit(s)]]} />
            {k.canSource && (
              <Group gap="xs">
                <Button size="xs" loading={busy} disabled={busy} onClick={() => { void onSync().catch((e: Error) => notifications.show({ color: "red", message: e.message, autoClose: false })); }}>Sync</Button>
                <ConfirmAction live label="Untrack" confirm={`Stop tracking the source of ${k.name}? Its versions are kept.`} onConfirm={onUntrack} />
              </Group>
            )}
          </>
        ) : <Text size="sm" c="dimmed">Not tracked — pushed by hand, or imported once.</Text>}
        {k.setWith && <Text size="sm" c="dimmed">Part of a set with {k.setWith} — imported from the same repo.</Text>}
        {k.canSource && !s && (
          <details>
            <summary style={{ cursor: "pointer" }}>Track…</summary>
            <EditForm live submit="Track" onSubmit={() => onTrack(source)}>
              <TextInput label="Source" description={`https://host/org/repo[//dir]@ref — imports it as a new version of ${k.name} and tracks it for Sync`} required autoComplete="off" spellCheck={false}
                placeholder="https://github.com/acme/kits//web@main" value={source} onChange={(e) => setSource(e.currentTarget.value)} />
            </EditForm>
          </details>
        )}
      </Stack>
    </Section>
  );
}

function PushForm({ k, onPush }: { k: KitDetail; onPush: (config: string) => Promise<unknown> }) {
  const [config, setConfig] = useState(k.view.pushText);
  return (
    <EditForm submit="Push version" onSubmit={() => onPush(config)}>
      {k.view.hidden ? <Text size="sm">{hiddenNote(k.name)}</Text> : k.view.masked && <Alert color="red" p="xs">{maskedNote}</Alert>}
      <Textarea label="Studio kit YAML" description={`pre-filled with v${k.view.n}; pushing makes a new current version (an unchanged kit makes none)`} required autosize minRows={12}
        spellCheck={false} styles={{ input: mono }} value={config} onChange={(e) => setConfig(e.currentTarget.value)} />
    </EditForm>
  );
}
