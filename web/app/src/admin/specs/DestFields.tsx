import { Checkbox, NativeSelect, SimpleGrid, TextInput, Textarea } from "@mantine/core";
import type { DestEndpoint, DestFields as Fields } from "../api/types";
import { TypeAhead } from "../ui/TypeAhead";

// DestValues is the New destination form's state: the destination's fields
// and its one endpoint's, flat (env and allow paths one per line). A
// destination's page edits it whole (DestinationPage, destDraft.ts).
export type DestValues = {
  name: string; route: string; upstream: string; identityIn: string; apply: string; credName: string; env: string; allowPaths: string;
  note: string; logHeadersIn: string; logHeadersOut: string; git: boolean;
};

const lines = (s: string) => s.split("\n").map((l) => l.trim()).filter(Boolean);

// destBody is the create body for v: its one endpoint, unnamed (the API
// names a lone unnamed endpoint after the destination); a client-env line
// without = throws.
export function destBody(v: DestValues): Fields {
  const e: DestEndpoint = {
    name: "", subdomain: "", route: v.route, upstream: v.upstream,
    identityIn: v.identityIn, identityInSpec: null, apply: v.apply, applySpec: null,
    allowPaths: lines(v.allowPaths), git: v.git, logHeadersIn: v.logHeadersIn, logHeadersOut: v.logHeadersOut,
  };
  const env = lines(v.env).map((l) => {
    const i = l.indexOf("=");
    if (i < 0) throw new Error(`env line "${l}" is not KEY=TEMPLATE`);
    return { key: l.slice(0, i).trim(), template: l.slice(i + 1).trim() };
  });
  return { name: v.name, credName: v.credName, note: v.note, env, endpoints: [e] };
}

// DestFields is the New destination form's body. An expression (custom) is
// set on the destination's page once it exists.
export function DestFields({ value, onChange, methods }: { value: DestValues; onChange: (v: DestValues) => void; methods: string[] }) {
  const set = <K extends keyof DestValues>(k: K) => (x: DestValues[K]) => onChange({ ...value, [k]: x });
  const text = (k: keyof DestValues) => (e: { currentTarget: { value: string } }) => set(k)(e.currentTarget.value as never);
  return (
    <SimpleGrid cols={{ base: 1, sm: 2 }}>
      <TextInput label="Name" required autoComplete="off" value={value.name} onChange={text("name")} />
      <TextInput label="Route" required description="add a subdomain and more endpoints on the destination's page" placeholder="/anthropic/" value={value.route} onChange={text("route")} />
      <TextInput label="Upstream URL" required placeholder="https://api.example.com" value={value.upstream} onChange={text("upstream")} />
      <NativeSelect label="Identity in" description="how an agent presents its identity token" value={value.identityIn} onChange={text("identityIn")} data={methods} />
      <NativeSelect label="Apply as" description="how the credential is sent upstream" value={value.apply} onChange={text("apply")} data={methods} />
      <TypeAhead label="Default credential" description="cred-name; a role can map its own" kind="credentials" value={value.credName} onChange={set("credName")} />
      <Textarea label="Client env" autosize minRows={2} spellCheck={false} placeholder="GH_HOST={host}" value={value.env} onChange={text("env")}
        description={<>one KEY=TEMPLATE per line; placeholders {"{url} {base} {host} {token} {host:<endpoint>} {url:<endpoint>}"}. Leave empty for the route's legacy default (/anthropic/, /git/).{value.route === "/git/" && !value.env && " Adding env here drops /git/'s implied git routing — tick “Route git” to keep it."}</>} />
      <Textarea label="Allowed paths" autosize minRows={2} spellCheck={false} placeholder="/v1/projects/my-project/locations/us-east5/publishers/anthropic/models/*:streamRawPredict" value={value.allowPaths} onChange={text("allowPaths")}
        description="one path.Match pattern per line, matched against the path after the route (* never crosses /); anything else is refused 403. Leave empty to allow any path." />
      <TextInput label="Note" description="shown to agents granted this destination — how to use it (≤ 300 bytes)" maxLength={300} value={value.note} onChange={text("note")} />
      <NativeSelect label="Log incoming headers" description="troubleshooting; secrets always redacted" value={value.logHeadersIn} onChange={text("logHeadersIn")} data={["none", "examined", "all"]} />
      <NativeSelect label="Log outgoing headers" description="troubleshooting; secrets always redacted" value={value.logHeadersOut} onChange={text("logHeadersOut")} data={["none", "changed", "all"]} />
      <Checkbox label="Route git" description="agents send git for the upstream host (e.g. https://github.com/) through this route" checked={value.git} onChange={(e) => set("git")(e.currentTarget.checked)} />
    </SimpleGrid>
  );
}
