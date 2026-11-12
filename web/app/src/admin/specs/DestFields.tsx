import { Checkbox, NativeSelect, SimpleGrid, TextInput, Textarea } from "@mantine/core";
import type { DestEndpoint, DestFields as Fields } from "../api/types";
import { TypeAhead } from "../ui/TypeAhead";

// DestValues is the destination form's state: the destination's fields and
// its first endpoint's, flat (env and allow paths one per line), plus a name
// when creating. The endpoints grid replaces it (COV-315); until then the
// form edits the first endpoint and destBody keeps the others.
export type DestValues = {
  name?: string; subdomain?: string; route: string; upstream: string; identityIn: string; apply: string; credName: string; env: string; allowPaths: string;
  note: string; logHeadersIn: string; logHeadersOut: string; git: boolean;
};

const lines = (s: string) => s.split("\n").map((l) => l.trim()).filter(Boolean);

// destValues is a destination's fields as the form shows them.
export function destValues(f: Fields): DestValues {
  const e = f.endpoints[0];
  return { subdomain: e?.subdomain ?? "", route: e?.route ?? "", upstream: e?.upstream ?? "", identityIn: e?.identityIn ?? "", apply: e?.apply ?? "", credName: f.credName,
    env: f.env.map((r) => `${r.key}=${r.template}`).join("\n"), allowPaths: (e?.allowPaths ?? []).join("\n"), note: f.note,
    logHeadersIn: e?.logHeadersIn ?? "none", logHeadersOut: e?.logHeadersOut ?? "none", git: e?.git ?? false };
}

// destBody is what the API takes for v: v's first endpoint over stored's
// (its name, subdomain and, while still custom, expression kept), stored's
// other endpoints unchanged; a client-env line without = throws. A new destination's one endpoint is named after
// it (the API names a lone unnamed endpoint so).
export function destBody(v: DestValues, stored?: Fields): Fields {
  const first: DestEndpoint | undefined = stored?.endpoints[0];
  const e: DestEndpoint = {
    name: first?.name ?? "", subdomain: first?.subdomain ?? "", route: v.route, upstream: v.upstream,
    identityIn: v.identityIn, identityInSpec: v.identityIn === "custom" ? first?.identityInSpec ?? null : null,
    apply: v.apply, applySpec: v.apply === "custom" ? first?.applySpec ?? null : null,
    allowPaths: lines(v.allowPaths), git: v.git, logHeadersIn: v.logHeadersIn, logHeadersOut: v.logHeadersOut,
  };
  const env = lines(v.env).map((l) => {
    const i = l.indexOf("=");
    if (i < 0) throw new Error(`env line "${l}" is not KEY=TEMPLATE`);
    return { key: l.slice(0, i).trim(), template: l.slice(i + 1).trim() };
  });
  return { ...(v.name !== undefined ? { name: v.name } : {}), credName: v.credName, note: v.note, env, endpoints: [e, ...(stored?.endpoints.slice(1) ?? [])] };
}

// DestFields is the destination form body shared by create (with a Name)
// and edit (the name is the key). custom is offered only when the
// destination already uses it: its specs are set via import or the API.
export function DestFields({ value, onChange, methods, stored }: { value: DestValues; onChange: (v: DestValues) => void; methods: string[]; stored?: { identityIn: string; apply: string } }) {
  const set = <K extends keyof DestValues>(k: K) => (x: DestValues[K]) => onChange({ ...value, [k]: x });
  const text = (k: keyof DestValues) => (e: { currentTarget: { value: string } }) => set(k)(e.currentTarget.value as never);
  // custom stays offered while the stored value is custom (the form can go back to it).
  const opts = (current: string, kept?: string) => [...methods, ...(current === "custom" || kept === "custom" ? ["custom"] : [])]
    .map((m) => ({ value: m, label: m === "custom" ? "custom (expression shown in Broker section; edit via import/API)" : m }));
  return (
    <SimpleGrid cols={{ base: 1, sm: 2 }}>
      {value.name !== undefined && <TextInput label="Name" required autoComplete="off" value={value.name} onChange={(e) => set("name")(e.currentTarget.value)} />}
      <TextInput label="Route" required={!value.subdomain} description={value.subdomain ? `on subdomain ${value.subdomain} (edited in the destination's file)` : undefined} placeholder="/anthropic/" value={value.route} onChange={text("route")} />
      <TextInput label="Upstream URL" required placeholder="https://api.example.com" value={value.upstream} onChange={text("upstream")} />
      <NativeSelect label="Identity in" description="how an agent presents its identity token" value={value.identityIn} onChange={text("identityIn")} data={opts(value.identityIn, stored?.identityIn)} />
      <NativeSelect label="Apply as" description="how the credential is sent upstream" value={value.apply} onChange={text("apply")} data={opts(value.apply, stored?.apply)} />
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
