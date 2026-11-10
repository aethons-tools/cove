import { ActionIcon, Alert, Badge, Box, Button, Code, CopyButton, Group, Stack, Text, Textarea, TextInput } from "@mantine/core";
import { useRef, useState, type ReactNode } from "react";
import { ApiError } from "../../api/client";
import type { ContextPanel as Panel, ContextSave } from "../api/types";
import { useConfigEditable } from "../branch/branch";
import { Cancelled } from "./cancelled";
import { ConfirmAction } from "./ConfirmAction";
import { MarkdownView } from "./MarkdownView";
import { QuietGrid, type GridColumn, type GridErrors } from "./QuietGrid";
import { Section } from "./Section";
import { Thermometer } from "./Thermometer";
import {
  bytes, coreBytes, draftOf, dropProblems, isEmpty, KINDS, kib, leafBytes, leafKey, problemsOf, saveOf,
  type Draft, type LeafDraft, type Problems, type ResourceRow,
} from "./contextDraft";
import "./contextpanel.css";

const SUB: Record<string, string> = {
  project: "project — what every agent is told when raised",
  role: "role — what every agent of this role is told when raised",
  "Jam-wide": "Jam-wide — what every agent on this Jam is told, last",
};

const columns: GridColumn<ResourceRow>[] = [
  { key: "name", label: "Name", width: 170 },
  { key: "kind", label: "Kind", choices: KINDS, width: 120 },
  { key: "ref", label: "Ref", mono: true, placeholder: "owner/repo, URL, …" },
  { key: "note", label: "Note", placeholder: "add a note" },
];

// A field path the card shows a problem on (else the problem shows above the footer).
const SHOWN = /^(core|leaves|resources)(\[\d+\]\.(name|readWhen|body|kind|ref|note))?$/;
const sentence = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);
const bad = (msg?: string) => (msg ? <Text span size="xs" c="red">{msg}</Text> : undefined);

// ContextPanel is the Agent context card (Jam-wide, a project's or a role's):
// read-only until Edit, then the whole card is editable — core instructions,
// progressive instructions (the API's leaves) and, for a project, resources —
// and Save context saves it in one request. Problems show at their fields,
// the client's own checks and the server's field errors alike.
export function ContextPanel({ panel, onSave, onClear }: { panel: Panel; onSave: (s: ContextSave) => Promise<unknown>; onClear: () => Promise<unknown> }) {
  const editable = useConfigEditable();
  const [draft, setDraft] = useState<Draft>(() => draftOf(panel));
  const [before, setBefore] = useState<Draft | null>(null);
  const [server, setServer] = useState<Problems>({});
  const [failure, setFailure] = useState("");
  const [busy, setBusy] = useState(false);
  const [yaml, setYaml] = useState(false);
  const [focusLeaf, setFocusLeaf] = useState<number | null>(null);
  const running = useRef(false);
  const editing = before !== null && editable;
  // The config branch closed mid-edit: drop the unsaved draft, so view mode
  // never shows edits as if they were saved.
  if (before !== null && !editable) { setDraft(before); setBefore(null); setServer({}); setFailure(""); }
  const lim = panel.limits;
  const res = panel.showResources;

  const problems: Problems = editing ? { ...server, ...problemsOf(draft, panel) } : {};
  const count = Object.keys(problems).length;
  const elsewhere = Object.entries(problems).filter(([k]) => !SHOWN.test(k) || (!res && k.startsWith("resources")));

  const change = (d: Draft, field: string) => { setDraft(d); setServer((s) => dropProblems(s, field)); };
  const setLeaf = (i: number, patch: Partial<LeafDraft>, field?: string) => {
    const leaves = draft.leaves.map((l, j) => (j === i ? { ...l, ...patch } : l));
    if (field) change({ ...draft, leaves }, `leaves[${i}].${field}`);
    else setDraft({ ...draft, leaves });
  };

  const edit = () => { setBefore(draft); setServer({}); setFailure(""); };
  const cancel = () => { if (before) setDraft(before); setBefore(null); setServer({}); setFailure(""); };
  const addLeaf = () => {
    const key = leafKey();
    setFocusLeaf(key);
    change({ ...draft, leaves: [...draft.leaves.map((l) => ({ ...l, open: false })), { key, name: "", readWhen: "", body: "", open: true }] }, "leaves");
  };
  const removeLeaf = (i: number) => change({ ...draft, leaves: draft.leaves.filter((_, j) => j !== i) }, "leaves");

  const save = async () => {
    if (running.current) return;
    running.current = true;
    setBusy(true);
    try {
      // A card emptied of everything is cleared (a save must set something).
      if (isEmpty(draft)) { if (!panel.empty) await onClear(); }
      else await onSave(saveOf(draft, res));
      setDraft({ ...draft, leaves: draft.leaves.map((l) => ({ ...l, open: false })) });
      setBefore(null);
      setServer({});
      setFailure("");
    } catch (e: unknown) {
      if (e instanceof Cancelled) return;
      if (e instanceof ApiError && e.fields.length > 0) {
        const ps: Problems = {};
        for (const f of e.fields) ps[f.field] ??= sentence(f.message);
        setServer(ps);
        setFailure("");
        // Open every instruction with a problem, so it shows.
        setDraft((d) => ({ ...d, leaves: d.leaves.map((l, i) => (Object.keys(ps).some((k) => k.startsWith(`leaves[${i}].`)) ? { ...l, open: true } : l)) }));
      } else {
        setFailure(e instanceof Error ? e.message : String(e));
      }
    } finally {
      running.current = false;
      setBusy(false);
    }
  };

  const core = editing ? coreBytes(draft.core, draft.resources.length) : panel.coreBytes;
  const pointer = res && draft.resources.length > 0;
  const gridErrors: GridErrors<ResourceRow> = draft.resources.map((_, i) => {
    const e: Partial<Record<keyof ResourceRow, string>> = {};
    for (const k of ["name", "kind", "ref", "note"] as const) if (problems[`resources[${i}].${k}`]) e[k] = problems[`resources[${i}].${k}`];
    return Object.keys(e).length ? e : undefined;
  });

  const footer = (
    <Group justify="space-between" wrap="wrap" gap="xs">
      <Group gap={6} wrap="wrap">
        {editing && <Text size="sm" c={count ? "red" : "dimmed"}>{count ? `${count} problem${count > 1 ? "s" : ""} to fix before saving` : "Ready to save"}</Text>}
        {editing && panel.yaml && <Text size="sm" c="dimmed">·</Text>}
        {panel.yaml && <Button variant="subtle" size="compact-sm" onClick={() => setYaml(!yaml)}>{yaml ? "Hide YAML" : "View as YAML"}</Button>}
        {editing && !panel.empty && <>
          <Text size="sm" c="dimmed">·</Text>
          <ConfirmAction label="Clear context" variant="subtle" size="compact-sm" confirm={`Remove this ${panel.scope} agent context?`} onConfirm={onClear} />
        </>}
      </Group>
      <Group gap="xs">
        {editing ? <>
          <Button variant="default" onClick={cancel} disabled={busy}>Cancel</Button>
          <Button onClick={() => void save()} loading={busy} disabled={count > 0}>Save context</Button>
        </> : editable && <Button data-action="edit" onClick={edit}>Edit</Button>}
      </Group>
    </Group>
  );

  return (
    <Section title="Agent context" sub={SUB[panel.scope] ?? `${panel.scope} — what every agent is told when raised`}
      actions={editing && <Badge variant="light" tt="none">Editing</Badge>} footer={footer}>
      <Stack gap="lg">
        <Alert color="yellow" p="xs"><Text size="sm">Changes apply to each agent at its <b>next raise</b>. Never put a credential here.</Text></Alert>

        <Box>
          <Group justify="space-between" align="baseline" mb={6}>
            <Text fw={600}>Core instructions</Text>
            <Text size="xs" c="dimmed">always in every agent's prompt</Text>
          </Group>
          {editing
            ? <Textarea aria-label="Core instructions" autosize minRows={4} value={draft.core} error={!!problems.core}
                onChange={(e) => change({ ...draft, core: e.currentTarget.value }, "core")} />
            : <div className="jam-ctx-md">{draft.core.trim() ? <MarkdownView>{draft.core}</MarkdownView> : <Text size="sm" c="dimmed">No core instructions.</Text>}</div>}
          <Thermometer used={core} limit={panel.budget} label={bad(problems.core) ?? (pointer && "includes the one-line resources pointer")} />
        </Box>

        <Box>
          <Text fw={600}>Progressive instructions</Text>
          <Text size="xs" c="dimmed" mb={6}>Detail an agent opens only when its “read when” matches the task. {draft.leaves.length} of {lim.maxLeaves}</Text>
          <div className="jam-ctx-rows">
            <div className="cols hdr"><span /><span>Name</span><span>Read when</span><span>Size</span><span /></div>
            {draft.leaves.map((l, i) => (
              <LeafRow key={l.key} leaf={l} i={i} editing={editing} problems={problems} maxReadWhen={lim.maxReadWhen} autoFocus={focusLeaf === l.key}
                onToggle={() => setLeaf(i, { open: !l.open })} onChange={(patch, field) => setLeaf(i, patch, field)} onRemove={() => removeLeaf(i)} />
            ))}
            {!editing && draft.leaves.length === 0 && <div className="none">None.</div>}
            {editing && draft.leaves.length < lim.maxLeaves && <div className="row"><button type="button" className="add" onClick={addLeaf}>+ new instruction…</button></div>}
          </div>
          <Thermometer used={kib(leafBytes(draft))} limit={lim.maxLeafBytes / 1024} unit="KiB" label={bad(problems.leaves) ?? "all progressive instructions together"} />
        </Box>

        {res && <Box>
          <Text fw={600}>Resources</Text>
          <Text size="xs" c="dimmed" mb={6}>
            Listed for agents in one generated instruction.{editing && " Click a cell to edit; Tab or Enter moves on; Esc leaves the cell."} {draft.resources.length} of {lim.maxResources}
          </Text>
          {!editing && draft.resources.length === 0
            ? <div className="jam-ctx-none">None.</div>
            : <QuietGrid columns={columns} rows={draft.resources} readOnly={!editing} noun="resource" max={lim.maxResources} errors={gridErrors}
                blank={() => ({ name: "", kind: "repo", ref: "", note: "" })} onChange={(rows) => change({ ...draft, resources: rows }, "resources")} />}
          {problems.resources && <Text size="xs" c="red" mt={4}>{problems.resources}</Text>}
        </Box>}

        {elsewhere.length > 0 && <Alert color="red" p="xs">{elsewhere.map(([k, v]) => <Text key={k} size="sm">{v}</Text>)}</Alert>}
        {failure && <Alert color="red" p="xs">{failure}</Alert>}
        {yaml && panel.yaml && (
          <Box pos="relative">
            <Text size="xs" c="dimmed" mb={4}>The saved context in the <Text span ff="monospace" size="xs">jam context</Text> file format{editing && "; your unsaved changes are not in it"}.</Text>
            <Code block aria-label="Agent context as YAML">{panel.yaml}</Code>
            <CopyButton value={panel.yaml}>
              {({ copied, copy }) => <Button size="compact-xs" variant="default" pos="absolute" top={24} right={6} onClick={copy}>{copied ? "Copied" : "Copy"}</Button>}
            </CopyButton>
          </Box>
        )}
      </Stack>
    </Section>
  );
}

// LeafRow is one progressive instruction: a summary row that ▸ expands in
// place to its name, read-when and body (rendered while viewing).
function LeafRow({ leaf: l, i, editing, problems, maxReadWhen, autoFocus, onToggle, onChange, onRemove }: {
  leaf: LeafDraft; i: number; editing: boolean; problems: Problems; maxReadWhen: number; autoFocus: boolean;
  onToggle: () => void; onChange: (patch: Partial<LeafDraft>, field: string) => void; onRemove: () => void;
}) {
  const p = (k: string) => problems[`leaves[${i}].${k}`];
  const any = p("name") || p("readWhen") || p("body");
  const label = l.name || `instruction ${i + 1}`;
  const field = (k: "name" | "readWhen", text: string, mono = false): ReactNode => editing
    ? <TextInput aria-label={`${text}, ${label}`} size="sm" value={l[k]} autoFocus={k === "name" && autoFocus} placeholder={k === "readWhen" ? "you are …" : "name.md"}
        error={k === "name" ? p(k) : !!p(k)} onChange={(e) => onChange({ [k]: e.currentTarget.value }, k)}
        styles={mono ? { input: { fontFamily: "var(--mantine-font-family-monospace)" } } : undefined} />
    : <Text size="sm" ff={mono ? "monospace" : undefined} py={4}>{l[k]}</Text>;
  return (
    <div className={`row${l.open ? " open" : ""}`} data-leaf={l.name}>
      <div className="cols head">
        <button type="button" className="toggle" aria-expanded={l.open} onClick={onToggle}>
          <span className="chev" aria-hidden="true">{l.open ? "▾" : "▸"}</span>
          <span className="name">{l.name || <span className="unnamed">unnamed</span>}{any && <span className="dot" aria-label="has problems">●</span>}</span>
          <span className="when">{l.readWhen}</span>
          <span>{kib(bytes(l.body)).toFixed(1)} KiB</span>
        </button>
        {editing && <ActionIcon variant="subtle" color="gray" aria-label={`Remove ${label}`} onClick={onRemove}>✕</ActionIcon>}
      </div>
      {l.open && (
        <div className="body">
          <div className="fields">
            <div><Text size="xs" fw={600}>Name</Text>{field("name", "Name", true)}</div>
            <div>
              <Text size="xs" fw={600}>Read when</Text>{field("readWhen", "Read when")}
              <Thermometer used={bytes(l.readWhen)} limit={maxReadWhen} label={bad(p("readWhen"))} />
            </div>
          </div>
          {editing
            ? <Textarea label="Body (markdown)" aria-label={`Body, ${label}`} autosize minRows={5} value={l.body} error={p("body")}
                onChange={(e) => onChange({ body: e.currentTarget.value }, "body")} styles={{ input: { fontFamily: "var(--mantine-font-family-monospace)" } }} />
            : <div className="jam-ctx-md">{l.body.trim() ? <MarkdownView>{l.body}</MarkdownView> : <Text size="sm" c="dimmed">No body.</Text>}</div>}
        </div>
      )}
    </div>
  );
}
