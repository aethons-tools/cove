import { Alert, Anchor, Badge, Box, Button, Group, List, Loader, NativeSelect, SimpleGrid, Stack, Text, TextInput, Title } from "@mantine/core";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState, type ReactNode } from "react";
import { Link, useLocation, useNavigate, useParams } from "react-router";
import { ApiError } from "../../api/client";
import { Problem } from "../../Problem";
import type { DestDetail, DestFields } from "../api/types";
import { useConfigEditable } from "../branch/branch";
import { adminApi, nodeHref } from "../paths";
import { Cancelled } from "../ui/cancelled";
import { ConfirmAction } from "../ui/ConfirmAction";
import { dropProblems, type Problems } from "../ui/contextDraft";
import { DataTable } from "../ui/DataTable";
import { Facts } from "../ui/Facts";
import { QuietGrid, type GridColumn, type GridErrors, type GridExpand } from "../ui/QuietGrid";
import { Section } from "../ui/Section";
import { Thermometer } from "../ui/Thermometer";
import { TypeAhead } from "../ui/TypeAhead";
import { useAdminMutation } from "../ui/useAdminMutation";
import { OperatorNotes } from "../ui/OperatorNotes";
import {
  blankRow, draftOf, ENCODINGS, EXPRESSION, inSpecOf, LOG_IN, LOG_OUT, logs, MAX_ENDPOINTS, MAX_NOTE, outSpecOf, problemsOf, saveOf, withRows,
  type Draft, type EndpointRow, type EnvDraft, type InDraft, type OutDraft,
} from "./destDraft";
import { deleteDestConfirm, destsKey } from "./DestinationsPage";
import "./destination.css";

const enc = encodeURIComponent;
const roleHref = (p: string, r: string) => nodeHref(`config/projects/${enc(p)}/agents/${enc(r)}`);
const mono = (s: string) => <Text span ff="monospace">{s}</Text>;
const dim = (s: string) => <span className="dim">{s}</span>;
const sentence = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);
const bad = (msg?: string) => (msg ? <Text span size="xs" c="red">{msg}</Text> : undefined);
const plural = (n: number, s: string) => `${n} ${s}${n === 1 ? "" : "s"}`;

const GRID_FIELDS = ["name", "subdomain", "route", "upstream", "identityIn", "apply", "allowPaths", "git"] as const;
// A field path the card shows a problem on (else the problem shows above the footer).
const SHOWN = /^(credName|note|env(\[\d+\]\.(key|template))?|endpoints(\[\d+\]\.(name|subdomain|route|upstream|identityIn|apply|allowPaths|git|logHeadersIn|logHeadersOut|(identityInSpec|applySpec)(\.\w+)?))?)$/;
const DETAIL = /^endpoints\[(\d+)\]\.(identityInSpec|applySpec|logHeaders)/;

const envColumns: GridColumn<EnvDraft>[] = [
  { key: "key", label: "Variable", mono: true, width: 240, placeholder: "GH_HOST" },
  { key: "template", label: "Value", mono: true, placeholder: "{host:<endpoint>}" },
];

// DestinationPage is one destination: its Destination card (endpoints and
// agent connector, read-only until Edit), and the roles that list it.
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
  const save = useAdminMutation((f: DestFields) => adminApi.post<DestDetail>(`destinations/${enc(d.name)}`, f), {
    invalidate: [destsKey], onSuccess: (x) => client.setQueryData(dataKey, x),
  });
  const remove = useAdminMutation(() => adminApi.del(`destinations/${enc(d.name)}`), { invalidate: [destsKey], onSuccess: () => void navigate(nodeHref("config/destinations")) });
  const n = d.fields.endpoints.length;
  return (
    <Stack>
      <Group justify="space-between" wrap="wrap">
        <Group gap="sm" align="baseline">
          <Title order={2}>{d.name}</Title>
          <Text size="sm" c="dimmed">{plural(n, "endpoint")} · used by {plural(d.uses.length, "role")}</Text>
        </Group>
        <ConfirmAction label="Delete" confirm={deleteDestConfirm(d.name, d.uses.length)} onConfirm={() => remove.mutateAsync(undefined)} />
      </Group>
      <OperatorNotes notes={d.notes} path={`destinations/${enc(d.name)}/operator-notes`} invalidate={[dataKey]} />
      {d.conflicts.length > 0 && (
        <Alert color="red" title={`Connector conflict in ${d.conflictRoles.length} role(s): ${d.conflictRoles.join(", ")}`}>
          <Text size="sm">Agents holding these roles can't assemble a connector (Jam fails closed with 409) until one side changes:</Text>
          <List size="sm">{d.conflicts.map((c, i) => (
            <List.Item key={i}><Anchor component={Link} to={roleHref(c.project, c.role)}>{c.project}/{c.role}</Anchor>: this and <Anchor component={Link} to={nodeHref("config/destinations/" + enc(c.other))}>{c.other}</Anchor> {c.what}</List.Item>
          ))}</List>
        </Alert>
      )}
      <DestinationCard key={JSON.stringify(d.fields)} d={d} onSave={(f) => save.mutateAsync(f)} />
      <Section title="Used by" sub="roles whose scope lists this destination, and the credential each injects">
        <DataTable rows={d.uses} rowKey={(u) => `${u.project}/${u.role}`} empty="No role lists this destination." columns={[
          { key: "r", label: "Role", render: (u) => <Anchor component={Link} to={roleHref(u.project, u.role)} ff="monospace" size="sm">{u.project}/{u.role}</Anchor> },
          { key: "c", label: "Credential", render: (u) => u.credIsRole ? <>{mono(u.cred)} <Text span size="sm" c="dimmed">role mapping</Text></>
            : d.fields.credName ? <>{mono(d.fields.credName)} <Text span size="sm" c="dimmed">default</Text></> : <Text span size="sm" c="dimmed">none</Text> },
        ]} />
      </Section>
    </Stack>
  );
}

// DestinationCard is the Destination card: read-only until Edit, then the
// whole card is editable — default credential, note, the endpoints grid and
// the agent connector — and Save destination saves it in one request.
// Problems show at their fields, the client's own checks and the server's
// field errors alike (ContextPanel's pattern).
function DestinationCard({ d, onSave }: { d: DestDetail; onSave: (f: DestFields) => Promise<unknown> }) {
  const editable = useConfigEditable();
  const [draft, setDraft] = useState<Draft>(() => draftOf(d));
  const [before, setBefore] = useState<Draft | null>(null);
  const [server, setServer] = useState<Problems>({});
  const [failure, setFailure] = useState("");
  const [busy, setBusy] = useState(false);
  const running = useRef(false);
  const editing = before !== null && editable;
  // The config branch closed mid-edit: drop the unsaved draft.
  if (before !== null && !editable) { setDraft(before); setBefore(null); setServer({}); setFailure(""); }

  const problems: Problems = editing ? { ...server, ...problemsOf(draft) } : {};
  const count = Object.keys(problems).length;
  const elsewhere = Object.entries(problems).filter(([k]) => !SHOWN.test(k));

  const change = (x: Draft, field: string) => { setDraft(x); setServer((s) => dropProblems(s, field)); };
  const setRows = (rows: EndpointRow[]) => {
    // Only a row's open state changed: not an edit (works while viewing too).
    const toggled = rows.length === draft.endpoints.length && rows.every((r, i) => JSON.stringify({ ...r, open: false }) === JSON.stringify({ ...draft.endpoints[i], open: false }));
    if (toggled) setDraft({ ...draft, endpoints: rows });
    else change({ ...draft, endpoints: withRows(draft.endpoints, rows) }, "endpoints");
  };
  const setRow = (i: number, patch: Partial<EndpointRow>, field: string) =>
    change({ ...draft, endpoints: draft.endpoints.map((r, j) => (j === i ? { ...r, ...patch } : r)) }, `endpoints[${i}].${field}`);

  const edit = () => { setBefore(draft); setServer({}); setFailure(""); };
  const cancel = () => { if (before) setDraft(before); setBefore(null); setServer({}); setFailure(""); };
  const submit = async () => {
    if (running.current) return;
    running.current = true;
    setBusy(true);
    try {
      await onSave(saveOf(draft));
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
        // Open every endpoint with a problem in its detail, so it shows.
        const open = new Set(Object.keys(ps).map((k) => DETAIL.exec(k)?.[1]).filter(Boolean).map(Number));
        setDraft((x) => ({ ...x, endpoints: x.endpoints.map((r, i) => (open.has(i) ? { ...r, open: true } : r)) }));
      } else {
        setFailure(e instanceof Error ? e.message : String(e));
      }
    } finally {
      running.current = false;
      setBusy(false);
    }
  };

  const choices = [...d.methods, EXPRESSION];
  const method = (k: "identityIn" | "apply") => (r: EndpointRow) => r[k] === EXPRESSION ? <span className="expr">{EXPRESSION}</span> : r[k] || dim("none");
  const columns: GridColumn<EndpointRow>[] = [
    { key: "name", label: "Name", mono: true, width: 110, placeholder: "name" },
    { key: "subdomain", label: "Subdomain", mono: true, width: 110, show: (r) => r.subdomain || dim("Jam host") },
    { key: "route", label: "Route", mono: true, show: (r) => r.route || dim("all") },
    { key: "upstream", label: "Upstream", mono: true, show: (r) => r.upstream || dim("https://…") },
    { key: "identityIn", label: "Identity in", mono: true, choices, show: method("identityIn") },
    { key: "apply", label: "Apply as", mono: true, choices, show: method("apply") },
    { key: "allowPaths", label: "Allowed paths", mono: true, placeholder: "/v1/* /v2/*", show: (r) => r.allowPaths || dim("any") },
    { key: "git", label: "Route git", choices: ["yes", "no"], width: 130, show: (r) => <>
      {r.git === "yes" ? <span className="tick" aria-label="yes">✓</span> : dim("–")}
      {logs(r) && <Badge size="xs" color="yellow" variant="light" tt="none" ml={6}>logging</Badge>}
    </> },
  ];
  const gridErrors: GridErrors<EndpointRow> = draft.endpoints.map((_, i) => {
    const e: Partial<Record<(typeof GRID_FIELDS)[number], string>> = {};
    for (const k of GRID_FIELDS) if (problems[`endpoints[${i}].${k}`]) e[k] = problems[`endpoints[${i}].${k}`];
    return Object.keys(e).length ? e : undefined;
  });
  const expand: GridExpand<EndpointRow> = {
    open: (r) => r.open,
    toggle: (r) => ({ ...r, open: !r.open }),
    detail: (r, i) => <EndpointDetail r={r} i={i} editing={editing} problems={problems} onChange={(patch, field) => setRow(i, patch, field)} />,
  };
  const envErrors: GridErrors<EnvDraft> = draft.env.map((_, i) => {
    const e: Partial<Record<"key" | "template", string>> = {};
    for (const k of ["key", "template"] as const) if (problems[`env[${i}].${k}`]) e[k] = problems[`env[${i}].${k}`];
    return Object.keys(e).length ? e : undefined;
  });

  const footer = (
    <Group justify="space-between" wrap="wrap" gap="xs">
      <Text size="sm" c={count ? "red" : "dimmed"}>{editing && (count ? `${count} problem${count > 1 ? "s" : ""} to fix before saving` : "Ready to save")}</Text>
      <Group gap="xs">
        {editing ? <>
          <Button variant="default" onClick={cancel} disabled={busy}>Cancel</Button>
          <Button onClick={() => void submit()} loading={busy} disabled={count > 0}>Save destination</Button>
        </> : editable && <Button data-action="edit" onClick={edit}>Edit</Button>}
      </Group>
    </Group>
  );

  return (
    <Section title="Destination" sub="how agents reach this service through Jam" actions={editing && <Badge variant="light" tt="none">Editing</Badge>} footer={footer}>
      <Stack gap="lg" className="jam-dest">
        {editing
          ? <SimpleGrid cols={{ base: 1, sm: 2 }}>
              <div>
                <TypeAhead label="Default credential" description="applied upstream unless a role maps its own" kind="credentials" value={draft.credName}
                  onChange={(v) => change({ ...draft, credName: v }, "credName")} />
                {bad(problems.credName)}
              </div>
              <div>
                <TextInput label="Note" description="shown to agents granted this destination" value={draft.note} error={!!problems.note}
                  onChange={(e) => change({ ...draft, note: e.currentTarget.value }, "note")} />
                <Thermometer used={new TextEncoder().encode(draft.note).length} limit={MAX_NOTE} label={bad(problems.note)} />
              </div>
            </SimpleGrid>
          : <Facts rows={[["Default credential", draft.credName ? mono(draft.credName) : null, "none"], ["Note", draft.note || null, "none"]]} />}

        <Box>
          <Text fw={600}>Endpoints</Text>
          <Text size="xs" c="dimmed" mb={6}>
            Each is reached at <Text span ff="monospace" size="xs">{"<subdomain>.<jam host><route>"}</Text> and uses this destination's credential and connector.
            {" "}▸ shows how it takes the identity and applies the credential.{editing && " Click a cell to edit; Tab or Enter moves on; Esc leaves the cell."}
          </Text>
          <QuietGrid columns={columns} rows={draft.endpoints} readOnly={!editing} noun="endpoint" max={MAX_ENDPOINTS} errors={gridErrors}
            blank={blankRow} onChange={setRows} expand={expand} />
          <Thermometer used={draft.endpoints.length} limit={MAX_ENDPOINTS} unit="endpoints" label={bad(problems.endpoints)} />
        </Box>

        <Box>
          <Text fw={600}>Agent connector</Text>
          <Text size="xs" c="dimmed" mb={6}>
            What agents set to use this destination, once for all its endpoints.
            {" "}<Text span ff="monospace" size="xs">{"{host:<endpoint>}"}</Text> is an endpoint's host,
            {" "}<Text span ff="monospace" size="xs">{"{url:<endpoint>}"}</Text> its host and route,
            {" "}<Text span ff="monospace" size="xs">{"{token}"}</Text> the agent's Jam identity.
          </Text>
          {!editing && draft.env.length === 0
            ? <div className="none">{d.envImplied && d.env.length ? <>None declared; the route's legacy default sets {d.env.map((e) => e.key).join(", ")}.</> : "None."}</div>
            : <QuietGrid columns={envColumns} rows={draft.env} readOnly={!editing} noun="variable" errors={envErrors}
                blank={() => ({ key: "", template: "" })} onChange={(rows) => change({ ...draft, env: rows }, "env")} />}
          {problems.env && <Text size="xs" c="red" mt={4}>{problems.env}</Text>}
          {d.gitImplied && <Text size="xs" c="dimmed" mt={4}>Agents' git goes through the <Text span ff="monospace" size="xs">/git/</Text> route (legacy default).</Text>}
        </Box>

        {elsewhere.length > 0 && <Alert color="red" p="xs">{elsewhere.map(([k, v]) => <Text key={k} size="sm">{v}</Text>)}</Alert>}
        {failure && <Alert color="red" p="xs">{failure}</Alert>}
      </Stack>
    </Section>
  );
}

// EndpointDetail is an endpoint's expanded row: what its Identity in and
// Apply as mean (editable only for an expression) and its header logging.
function EndpointDetail({ r, i, editing, problems, onChange }: {
  r: EndpointRow; i: number; editing: boolean; problems: Problems; onChange: (patch: Partial<EndpointRow>, field: string) => void;
}) {
  const p = (k: string) => problems[`endpoints[${i}].${k}`];
  const label = r.name || `endpoint ${i + 1}`;
  const inSpec = inSpecOf(r);
  const outSpec = outSpecOf(r);
  const inEdit = editing && r.identityIn === EXPRESSION && r.inSpec !== null;
  const outEdit = editing && r.apply === EXPRESSION && r.outSpec !== null;
  const setIn = (k: keyof InDraft, v: string) => onChange({ inSpec: { ...r.inSpec!, [k]: v } }, `identityInSpec.${k}`);
  const setOut = (k: keyof OutDraft, v: string) => onChange({ outSpec: { ...r.outSpec!, [k]: v } }, `applySpec.${k}`);
  const field = (text: string, value: string, on: boolean, set: (v: string) => void, err?: string, choices?: readonly string[]): ReactNode => (
    <div className="field">
      <Text size="xs" c="dimmed">{text}</Text>
      {on && choices
        ? <NativeSelect size="xs" aria-label={`${text}, ${label}`} data={[...choices]} value={value} error={err} onChange={(e) => set(e.currentTarget.value)} />
        : on
          ? <TextInput size="xs" aria-label={`${text}, ${label}`} value={value} error={err} onChange={(e) => set(e.currentTarget.value)}
              styles={{ input: { fontFamily: "var(--mantine-font-family-monospace)" } }} />
          : <><Text size="sm" ff="monospace" className="ro">{value || dim("—")}</Text>{bad(err)}</>}
    </div>
  );
  const kind = (m: string) => (m === EXPRESSION ? null : <Text span size="xs" c="dimmed" fw={400}> {m ? `(preset ${m})` : "(none)"}</Text>);
  return (
    <div className="jam-dest-detail" data-endpoint={r.name}>
      <div>
        <Text size="sm" fw={600}>Identity in{kind(r.identityIn)}</Text>
        {bad(p("identityInSpec"))}
        {inSpec && <>
          {field("Header", inSpec.header, inEdit, (v) => setIn("header", v), p("identityInSpec.header"))}
          {field("Prefixes, comma-separated", inSpec.prefixes, inEdit, (v) => setIn("prefixes", v), p("identityInSpec.prefixes"))}
          {field("Encoding", inSpec.encoding, inEdit, (v) => setIn("encoding", v), p("identityInSpec.encoding"), ENCODINGS)}
        </>}
      </div>
      <div>
        <Text size="sm" fw={600}>Apply as{kind(r.apply)}</Text>
        {bad(p("applySpec"))}
        {outSpec && <>
          {field("Header", outSpec.header, outEdit, (v) => setOut("header", v), p("applySpec.header"))}
          {field("Template, with {cred} once", outSpec.template, outEdit, (v) => setOut("template", v), p("applySpec.template"))}
          {field("Encoding", outSpec.encoding, outEdit, (v) => setOut("encoding", v), p("applySpec.encoding"), ENCODINGS)}
          {field("Basic user", outSpec.basicUser, outEdit, (v) => setOut("basicUser", v), p("applySpec.basicUser"))}
        </>}
      </div>
      <div className="logging">
        <Text size="sm" fw={600}>Header logging <Text span size="xs" c="dimmed" fw={400}>for debugging; credentials never logged in clear</Text></Text>
        <SimpleGrid cols={2}>
          {field("Log incoming headers", r.logHeadersIn, editing, (v) => onChange({ logHeadersIn: v }, "logHeadersIn"), p("logHeadersIn"), LOG_IN)}
          {field("Log outgoing headers", r.logHeadersOut, editing, (v) => onChange({ logHeadersOut: v }, "logHeadersOut"), p("logHeadersOut"), LOG_OUT)}
        </SimpleGrid>
      </div>
    </div>
  );
}
