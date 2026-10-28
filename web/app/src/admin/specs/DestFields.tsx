import { Checkbox, NativeSelect, SimpleGrid, TextInput, Textarea } from "@mantine/core";
import type { DestFields as Fields } from "../api/types";
import { TypeAhead } from "../ui/TypeAhead";

// DestValues is the destination form's state: its fields, plus a name when
// creating.
export type DestValues = Fields & { name?: string };

// destBody is what the API takes: every field as a string (git "on" or "").
export function destBody(v: DestValues): Record<string, string> {
  const { git, ...rest } = v;
  return { ...rest, git: git ? "on" : "" };
}

// DestFields is the destination form body shared by create (with a Name)
// and edit (the name is the key). custom is offered only when the
// destination already uses it: its specs are set via import or the API.
export function DestFields({ value, onChange, methods, stored }: { value: DestValues; onChange: (v: DestValues) => void; methods: string[]; stored?: { identityIn: string; apply: string } }) {
  const set = <K extends keyof DestValues>(k: K) => (x: DestValues[K]) => onChange({ ...value, [k]: x });
  const text = (k: keyof Fields) => (e: { currentTarget: { value: string } }) => set(k)(e.currentTarget.value as never);
  // custom stays offered while the stored value is custom (the form can go back to it).
  const opts = (current: string, kept?: string) => [...methods, ...(current === "custom" || kept === "custom" ? ["custom"] : [])]
    .map((m) => ({ value: m, label: m === "custom" ? "custom (spec shown in Broker section; edit via import/API)" : m }));
  return (
    <SimpleGrid cols={{ base: 1, sm: 2 }}>
      {value.name !== undefined && <TextInput label="Name" required autoComplete="off" value={value.name} onChange={(e) => set("name")(e.currentTarget.value)} />}
      <TextInput label="Route" required placeholder="/anthropic/" value={value.route} onChange={text("route")} />
      <TextInput label="Upstream URL" required placeholder="https://api.example.com" value={value.upstream} onChange={text("upstream")} />
      <NativeSelect label="Identity in" description="how an agent presents its identity token" value={value.identityIn} onChange={text("identityIn")} data={opts(value.identityIn, stored?.identityIn)} />
      <NativeSelect label="Apply as" description="how the credential is sent upstream" value={value.apply} onChange={text("apply")} data={opts(value.apply, stored?.apply)} />
      <TypeAhead label="Default credential" description="cred-name; a role can map its own" kind="credentials" value={value.credName} onChange={set("credName")} />
      <Textarea label="Client env" autosize minRows={2} spellCheck={false} placeholder="GH_HOST={host}" value={value.env} onChange={text("env")}
        description={<>one KEY=TEMPLATE per line; placeholders {"{url} {base} {host} {token}"}. Leave empty for the route's legacy default (/anthropic/, /git/).{value.route === "/git/" && !value.env && " Adding env here drops /git/'s implied git routing — tick “Route git” to keep it."}</>} />
      <Textarea label="Allowed paths" autosize minRows={2} spellCheck={false} placeholder="/v1/projects/my-project/locations/us-east5/publishers/anthropic/models/*:streamRawPredict" value={value.allowPaths} onChange={text("allowPaths")}
        description="one path.Match pattern per line, matched against the path after the route (* never crosses /); anything else is refused 403. Leave empty to allow any path." />
      <TextInput label="Note" description="shown to agents granted this destination — how to use it (≤ 300 bytes)" maxLength={300} value={value.note} onChange={text("note")} />
      <NativeSelect label="Log incoming headers" description="troubleshooting; secrets always redacted" value={value.logHeadersIn} onChange={text("logHeadersIn")} data={["none", "examined", "all"]} />
      <NativeSelect label="Log outgoing headers" description="troubleshooting; secrets always redacted" value={value.logHeadersOut} onChange={text("logHeadersOut")} data={["none", "changed", "all"]} />
      <Checkbox label="Route git" description="agents send git for the upstream host (e.g. https://github.com/) through this route" checked={value.git} onChange={(e) => set("git")(e.currentTarget.checked)} />
    </SimpleGrid>
  );
}
