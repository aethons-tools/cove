import { Anchor, Button, Checkbox, FileInput, Group, Stack, Text, TextInput, Textarea } from "@mantine/core";
import { useQueryClient } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { Link } from "react-router";
import type { ApiError } from "../../api/client";
import type { DraftEditor as Editor, DraftView } from "../api/types";
import { Confirm } from "../ui/Confirm";
import { ConfirmAction } from "../ui/ConfirmAction";
import { EditForm } from "../ui/EditForm";
import { Section } from "../ui/Section";
import { mono } from "./KitsPage";
import { draftKey, type DraftWrite } from "./useDraft";
import { unsavedAsk, useUnsavedGuard } from "./useUnsavedGuard";

const enc = encodeURIComponent;

// DraftEditor is one draft file, opened: its text to edit and save (with
// the draft's etag), or why it can't be edited; download, mode, replace,
// rename and delete. Typed edits hold navigation until saved or discarded.
export function DraftEditor({ kit, d, e, fileHref, run }: {
  kit: string; d: DraftView; e: Editor; fileHref: (path: string, diff?: boolean) => string; run: (w: DraftWrite) => Promise<unknown>;
}) {
  const client = useQueryClient();
  const [text, setText] = useState(e.text);
  const [exec, setExec] = useState(e.exec);
  const [stale, setStale] = useState(false);
  const [asking, setAsking] = useState(false);
  const dirty = useRef(false);
  const blocker = useUnsavedGuard(dirty);
  const save = async () => {
    dirty.current = false; // the answer moves the page: let it
    try {
      await run({ path: "save", body: { etag: e.etag, path: e.path, content: text, executable: exec }, open: e.path });
    } catch (err) {
      dirty.current = true;
      if ((err as ApiError).code === "stale") setStale(true);
      throw err;
    }
  };
  const reload = () => {
    dirty.current = false;
    setAsking(false);
    void client.invalidateQueries({ queryKey: draftKey(kit) });
  };
  return (
    <Section title={e.label} sub={`${e.mode} · ${e.size} B`} actions={
      <Group gap="sm">
        <Anchor component={Link} to={fileHref(e.path, true)} size="sm">diff vs v{d.base}</Anchor>
        <Anchor href={`/app/admin/api/kits/${enc(kit)}/draft/download?path=${enc(e.path)}`} download size="sm">Download</Anchor>
      </Group>
    }>
      <Stack gap="sm">
        {e.editable ? (
          <EditForm submit="Save" onSubmit={save}>
            {e.invisible && <Text size="sm" c="dimmed">This file holds invisible or bidirectional characters (a byte-order mark, a direction override); the editor keeps them as they are, and diffs show them escaped.</Text>}
            <Textarea aria-label={e.label} value={text} spellCheck={false} autoComplete="off" autosize minRows={16} maxRows={40} wrap="off"
              styles={{ input: { ...mono, whiteSpace: "pre", tabSize: 4 } }}
              onChange={(ev) => { setText(ev.currentTarget.value); dirty.current = true; }} />
            <Checkbox label="Executable (0755)" checked={exec} onChange={(ev) => { setExec(ev.currentTarget.checked); dirty.current = true; }} />
            {stale && <Group><Button size="xs" variant="light" onClick={() => setAsking(true)}>Reload</Button></Group>}
          </EditForm>
        ) : (
          <>
            <Text size="sm" c="dimmed">{e.note}</Text>
            <EditForm submit="Set mode" onSubmit={() => run({ path: "mode", body: { etag: e.etag, path: e.path, executable: exec }, open: e.path })}>
              <Checkbox label="Executable (0755)" checked={exec} onChange={(ev) => setExec(ev.currentTarget.checked)} />
            </EditForm>
          </>
        )}
        <Replace d={d} e={e} run={run} />
        <Rename e={e} run={run} />
        <Group><ConfirmAction label="Delete" confirm={`Remove ${e.label} from the draft?`} onConfirm={() => run({ path: "delete", body: { etag: e.etag, path: e.path } })} /></Group>
      </Stack>
      <Confirm opened={asking} text={unsavedAsk} action="Discard" color="red" onCancel={() => setAsking(false)} onConfirm={reload} />
      <Confirm opened={blocker.state === "blocked"} text={unsavedAsk} action="Discard" color="red"
        onCancel={() => blocker.reset?.()} onConfirm={() => { dirty.current = false; blocker.proceed?.(); }} />
    </Section>
  );
}

function Replace({ d, e, run }: { d: DraftView; e: Editor; run: (w: DraftWrite) => Promise<unknown> }) {
  const [file, setFile] = useState<File | null>(null);
  const [exec, setExec] = useState(e.exec);
  const submit = () => {
    const f = new FormData();
    f.set("etag", e.etag);
    f.set("path", e.path);
    if (exec) f.set("executable", "1");
    if (file) f.set("file", file);
    return run({ path: "replace", form: f, open: e.path });
  };
  return (
    <details>
      <summary style={{ cursor: "pointer" }}>Replace with an upload…</summary>
      <EditForm submit="Replace" onSubmit={submit}>
        <FileInput label="New content" description={`up to ${d.maxFile}; the uploaded file's own name is ignored`} required value={file} onChange={setFile} />
        <Checkbox label="Executable (0755)" checked={exec} onChange={(ev) => setExec(ev.currentTarget.checked)} />
      </EditForm>
    </details>
  );
}

function Rename({ e, run }: { e: Editor; run: (w: DraftWrite) => Promise<unknown> }) {
  const [to, setTo] = useState(e.path);
  return (
    <EditForm submit="Rename" onSubmit={() => run({ path: "rename", body: { etag: e.etag, from: e.path, to }, open: to.trim() })}>
      <TextInput label="Rename to" required autoComplete="off" spellCheck={false} value={to} onChange={(ev) => setTo(ev.currentTarget.value)} />
    </EditForm>
  );
}
