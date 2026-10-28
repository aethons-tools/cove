import { Alert, Anchor, Badge, Button, Code, Group, Table, Text } from "@mantine/core";
import type { ReactNode } from "react";
import { Link } from "react-router";
import type { FileList, FilesDiff, KitDetail, KitFile, KitView } from "../api/types";
import { nodeHref } from "../paths";
import { DiffView } from "../ui/DiffView";
import { Facts, type Fact } from "../ui/Facts";
import { Section } from "../ui/Section";

const enc = encodeURIComponent;
const mono = (s: string) => <Text span ff="monospace">{s}</Text>;
const chips = (xs: string[]): ReactNode | null => xs.length ? <Group gap={4}>{xs.map((x) => <Badge key={x} size="sm" variant="light" color="gray" tt="none">{x}</Badge>)}</Group> : null;
const kvList = (xs: { key: string; value: string }[]): ReactNode | null => xs.length ? <>{xs.map((x) => <div key={x.key}>{mono(x.key)} <Text span c="dimmed">{x.value}</Text></div>)}</> : null;

export const maskedNote = "A packed context in this text is shown masked («packed context, not shown»); remove it before pushing.";
export const hiddenNote = (kit: string) => <>This kit.yml is not shown: it may hold a packed context. Write it out with <code>jam config write &lt;dir&gt; --all</code> (it is <code>kits/{kit}/kit.yml</code>), fix it, and push that directory with <code>jam kit push</code>.</>;

// VersionCard is a version: what a studio raising it gets, then its YAML.
export function VersionCard({ k }: { k: KitDetail }) {
  const v = k.view;
  if (v.parseErr) {
    return (
      <Section title={`v${v.n}`}>
        <Alert color="red">{v.rawShown && "Stored config is not a valid studio kit: "}{v.parseErr}</Alert>
        <Text size="sm" c="dimmed" mt="xs">Raises from this version fail closed. Push a valid version to replace it.</Text>
        {v.hidden ? <Text size="sm" mt="xs">{hiddenNote(k.name)}</Text> : v.masked && <Text size="sm" mt="xs">{maskedNote}</Text>}
        {v.rawShown && <Code block mt="xs">{v.rawShown}</Code>}
      </Section>
    );
  }
  return (
    <>
      {k.chainErr && <Alert color="red">Kit chain does not resolve: {k.chainErr}</Alert>}
      <Section title={`v${v.n}`} sub={<>{v.n === k.current && <Badge size="sm" mr={6}>current</Badge>}<Text span ff="monospace" size="xs">build digest {v.digest}</Text></>}>
        <Facts rows={[
          ["Base", <>{v.base}{v.image && <> {mono(v.image)}</>}</>],
          ...(k.chain.length ? [["Built on", <>{k.chain.map((p, i) => (
            <span key={p.name}>{i > 0 && " → "}<Anchor component={Link} to={nodeHref("config/kits/" + enc(p.name))}>{p.name} v{p.version}</Anchor> <Text span c="dimmed">{p.follow ? "follows" : "pinned"}</Text></span>
          ))} <Text span c="dimmed">→ this kit</Text></>] as Fact] : []),
          ...viewFacts(v, false),
        ]} />
      </Section>
      {k.effective && (
        <Section title="Effective" sub={`this kit merged over ${k.chain.length} parent(s) — what a role binding it gets`}>
          <Facts rows={viewFacts(k.effective, true)} />
        </Section>
      )}
      <Section title="YAML" sub={`v${v.n}'s kit.yml, as stored`} actions={<Button size="compact-xs" variant="light" onClick={() => void navigator.clipboard?.writeText(v.yaml)}>Copy</Button>}>
        <Code block>{v.yaml}</Code>
      </Section>
    </>
  );
}

function viewFacts(v: KitView, effective: boolean): Fact[] {
  return [
    ["Egress", chips(v.egress), "none"],
    ["Egress ceiling", chips(v.ceiling), "none"],
    ...(v.excluded.length ? [["Excluded", <>{chips(v.excluded)}{!effective && <Text span size="xs" c="dimmed"> Anthropic is reached only through the broker (COV-208)</Text>}</>] as Fact] : []),
    ...(effective ? [["MCP servers", kvList(v.mcpServers), "none"] as Fact] : []),
    ["Build args", chips(v.buildArgs.map((b) => `${b.key}=${b.value}`)), "none"],
    ["Secret demands", kvList(v.secrets), "none"],
    ...(effective ? [["Notes", kvList(v.notes), "none"] as Fact] : []),
    ["Prompt", v.prompt ? <Text span style={{ whiteSpace: "pre-wrap" }}>{v.prompt}</Text> : null, "none"],
  ];
}

// FilesCard is a version's directory, each file opening in place.
export function FilesCard({ f, fileHref }: { f: FileList; fileHref: (path: string) => string }) {
  return (
    <Section title="Files" sub={`v${f.n}'s directory (${f.size}), kit.yml included`}>
      <Table>
        <Table.Thead><Table.Tr><Table.Th>Path</Table.Th><Table.Th>Mode</Table.Th><Table.Th>Size</Table.Th></Table.Tr></Table.Thead>
        <Table.Tbody>{f.rows.map((r) => (
          <Table.Tr key={r.path}><Table.Td><Anchor component={Link} to={fileHref(r.path)} ff="monospace" size="sm">{r.label}</Anchor></Table.Td><Table.Td ff="monospace">{r.mode}</Table.Td><Table.Td>{r.size} B</Table.Td></Table.Tr>
        ))}</Table.Tbody>
      </Table>
      {f.hidden > 0 && <Text size="sm" c="dimmed">{f.hidden} more file(s) not listed</Text>}
    </Section>
  );
}

// FilesDiffCard is two versions' directories compared file by file.
export function FilesDiffCard({ d, fileHref, title, sub }: { d: FilesDiff; fileHref: (path: string) => string; title?: string; sub?: string }) {
  return (
    <Section title={title ?? `Files v${d.from} → v${d.to}`} sub={sub ?? "the kit directory, file by file"}>
      {d.rows.length > 0 && (
        <Table>
          <Table.Thead><Table.Tr><Table.Th>Change</Table.Th><Table.Th>Path</Table.Th><Table.Th>Mode</Table.Th><Table.Th>Size</Table.Th></Table.Tr></Table.Thead>
          <Table.Tbody>{d.rows.map((r) => (
            <Table.Tr key={r.path} data-change={r.change}><Table.Td>{r.change}</Table.Td><Table.Td><Anchor component={Link} to={fileHref(r.path)} ff="monospace" size="sm">{r.label}</Anchor></Table.Td><Table.Td ff="monospace">{r.mode}</Table.Td><Table.Td>{r.size}</Table.Td></Table.Tr>
          ))}</Table.Tbody>
        </Table>
      )}
      <Text size="sm" c="dimmed">{d.more > 0 && `… ${d.more} more changed, not listed; `}{d.rows.length === 0 && "No file changed; "}{d.unchanged} unchanged</Text>
    </Section>
  );
}

// FileCard is one file: its content, its line diff, or why neither.
export function FileCard({ f, kit }: { f: KitFile; kit: string }) {
  if (f.missing) {
    return <Section title="File"><Alert color="red">No such file in {f.diff ? "either version" : "this version"}'s files.</Alert></Section>;
  }
  return (
    <Section title={f.path} sub={f.diff ? `v${f.from} → ${f.to ? `v${f.to}` : "draft"}` : "content"}>
      {f.note ? <Text size="sm" c="dimmed">{f.note}</Text> : (
        <>
          {f.hidden ? <Text size="sm">{hiddenNote(kit)}</Text> : !f.diff && f.masked && <Text size="sm">{maskedNote}</Text>}
          {f.diff ? (f.tooLarge ? <Text size="sm" c="dimmed">Too large to diff here.</Text> : <DiffView lines={f.lines} />) : <Code block>{f.content}</Code>}
        </>
      )}
    </Section>
  );
}
