import { Alert, Anchor, Badge, Card, Checkbox, FileInput, Loader, NativeSelect, Stack, Table, Text, TextInput, Title } from "@mantine/core";
import { useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router";
import { Problem } from "../../Problem";
import type { DraftPage, DraftView } from "../api/types";
import { nodeHref } from "../paths";
import { ConfirmAction } from "../ui/ConfirmAction";
import { DiffView } from "../ui/DiffView";
import { EditForm } from "../ui/EditForm";
import { Section } from "../ui/Section";
import { DraftEditor } from "./DraftEditor";
import { DraftPublish, PublishedCard } from "./DraftPublish";
import { FileCard, FilesDiffCard, hiddenNote } from "./KitParts";
import { useDraft, useDraftWrite, type DraftWrite } from "./useDraft";

const enc = encodeURIComponent;

// DraftSection is a kit's Draft: a copy of one version's files, edited here
// and published as the next version. Its file (?dfile=, with ?ddiff=1 its
// diff against the base) is an in-page selector, kept in the URL.
export function DraftSection({ kit }: { kit: string }) {
  const [q] = useSearchParams();
  const file = q.get("dfile") ?? "", diff = q.get("ddiff") === "1";
  const { data, error, isPending } = useDraft(kit, file, diff);
  if (isPending) return <Loader size="sm" />;
  if (error) return <Problem error={error} here={location.pathname} />;
  return <Draft kit={kit} p={data} />;
}

function Draft({ kit, p }: { kit: string; p: DraftPage }) {
  const navigate = useNavigate();
  const base = nodeHref("config/kits/" + enc(kit));
  const fileHref = (path: string, diff = false) => `${base}?${new URLSearchParams({ dfile: path, ...(diff ? { ddiff: "1" } : {}) })}`;
  const [published, setPublished] = useState(p.published);
  const write = useDraftWrite(kit, (r, w) => {
    setPublished(r.page.published);
    void navigate(w.open ? fileHref(w.open) : base, { replace: true });
  });
  const run = (w: DraftWrite) => write.mutateAsync(w);
  return (
    <Stack id="draft">
      <Title order={3}>Draft</Title>
      {published && <PublishedCard kit={kit} p={published} />}
      {p.draft ? <OpenDraft kit={kit} d={p.draft} p={p} fileHref={fileHref} run={run} /> : <NoDraft p={p} run={run} />}
    </Stack>
  );
}

function NoDraft({ p, run }: { p: DraftPage; run: (w: DraftWrite) => Promise<unknown> }) {
  const [from, setFrom] = useState(String(p.current));
  return (
    <Section title="No draft" sub="a draft is a copy of one version's files: edit it here, then publish it as the next version">
      <EditForm submit="Open draft" onSubmit={() => run({ path: "", body: { from: Number(from) } })}>
        <NativeSelect label="Open from" value={from} onChange={(e) => setFrom(e.currentTarget.value)}
          data={p.versions.map((v) => ({ value: String(v), label: `v${v}${v === p.current ? " (current)" : ""}` }))} />
      </EditForm>
    </Section>
  );
}

function OpenDraft({ kit, d, p, fileHref, run }: {
  kit: string; d: DraftView; p: DraftPage; fileHref: (path: string, diff?: boolean) => string; run: (w: DraftWrite) => Promise<unknown>;
}) {
  return (
    <>
      <Section title="Draft" sub={`base v${d.base}, current v${d.current} · last saved by ${d.actor} at ${d.updated}`}
        actions={<ConfirmAction label="Discard" confirm={`Discard the draft of ${kit}? Its changes are lost.`} onConfirm={() => run({ path: "discard", body: { etag: d.etag } })} />}>
        {d.superseded && <Alert color="yellow">base v{d.base}, current v{d.current}: publishing makes the draft's files the next version as they are; v{d.current}'s changes are not merged.</Alert>}
      </Section>
      <DraftPublish kit={kit} d={d} run={run} />
      {d.fileMissing && <Alert color="red">No such file in the draft.</Alert>}
      {d.editor && <DraftEditor key={`${d.editor.path}/${d.editor.etag}`} kit={kit} d={d} e={d.editor} fileHref={fileHref} run={run} />}
      {d.fileDiff && <FileCard f={d.fileDiff} kit={kit} />}
      <Section title="Files" sub={`${d.size} · marked against v${d.base}`}>
        <Table aria-label="Draft files">
          <Table.Thead><Table.Tr><Table.Th>Path</Table.Th><Table.Th>Mode</Table.Th><Table.Th>Size</Table.Th><Table.Th>vs v{d.base}</Table.Th></Table.Tr></Table.Thead>
          <Table.Tbody>
            {d.tree.map((r) => r.dir ? (
              <Table.Tr key={"d:" + r.label}><Table.Td c="dimmed" ff="monospace" pl={8 + r.depth * 16}>{r.name}/</Table.Td><Table.Td /><Table.Td /><Table.Td /></Table.Tr>
            ) : (
              <Table.Tr key={r.path} data-change={r.change || undefined}>
                <Table.Td pl={8 + r.depth * 16}><Anchor component={Link} to={fileHref(r.path)} ff="monospace" size="sm">{r.name}</Anchor></Table.Td>
                <Table.Td ff="monospace">{r.mode}</Table.Td><Table.Td>{r.size} B</Table.Td>
                <Table.Td>{r.change && <Badge size="sm" variant="light" color="gray">{r.change}</Badge>}</Table.Td>
              </Table.Tr>
            ))}
            {d.removed.map((r) => (
              <Table.Tr key={"r:" + r.path} data-change="removed">
                <Table.Td><Anchor component={Link} to={fileHref(r.path, true)} ff="monospace" size="sm" td="line-through">{r.name}</Anchor></Table.Td>
                <Table.Td ff="monospace">{r.mode}</Table.Td><Table.Td>{r.size} B</Table.Td><Table.Td><Badge size="sm" variant="light" color="gray">removed</Badge></Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      </Section>
      <AddFile d={d} run={run} />
      {d.changes && <FilesDiffCard d={{ ...d.changes, from: d.base }} fileHref={(path) => fileHref(path, true)} title={`Changes vs v${d.base}`} sub="the draft's files against its base, file by file" />}
      {d.yamlDiff && (
        <Section title={`kit.yml vs v${d.yamlDiff.from}`}>
          {d.yamlDiff.note ? <Text size="sm" c="dimmed">{d.yamlDiff.note}</Text> : (
            <>
              {d.yamlDiff.hidden && <Text size="sm">{hiddenNote(p.name)}</Text>}
              {d.yamlDiff.tooLarge ? <Text size="sm" c="dimmed">Too large to diff here.</Text> : <DiffView lines={d.yamlDiff.lines} />}
            </>
          )}
        </Section>
      )}
    </>
  );
}

function AddFile({ d, run }: { d: DraftView; run: (w: DraftWrite) => Promise<unknown> }) {
  const [path, setPath] = useState("");
  const [file, setFile] = useState<File | null>(null);
  const [exec, setExec] = useState(false);
  const submit = () => {
    const f = new FormData();
    f.set("etag", d.etag);
    f.set("path", path);
    if (exec) f.set("executable", "1");
    if (file) f.set("file", file);
    return run({ path: "add", form: f, open: path.trim() });
  };
  return (
    <Card withBorder>
      <details>
        <summary style={{ cursor: "pointer", fontWeight: 600 }}>Add file…</summary>
        <EditForm submit="Add" onSubmit={submit} resetOnSuccess={() => { setPath(""); setFile(null); setExec(false); }}>
          <TextInput label="Path" description="slash-separated, inside the kit directory, e.g. scripts/setup.sh" required autoComplete="off" spellCheck={false} value={path} onChange={(e) => setPath(e.currentTarget.value)} />
          <FileInput label="Content" description={`optional: upload a file (up to ${d.maxFile}; its own name is ignored), or leave empty for an empty text file to edit`} clearable value={file} onChange={setFile} />
          <Checkbox label="Executable (0755)" checked={exec} onChange={(e) => setExec(e.currentTarget.checked)} />
        </EditForm>
      </details>
    </Card>
  );
}

