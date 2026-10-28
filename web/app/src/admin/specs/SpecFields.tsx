import { NativeSelect, SimpleGrid, TextInput, Textarea } from "@mantine/core";
import type { SpecChoices, SpecFields as Fields } from "../api/types";

// SpecValues is the model-spec form's state: its fields, plus a name when
// creating.
export type SpecValues = Fields & { name?: string };

// SpecFields is the model-spec form body shared by create (with a Name) and
// edit. Principal choices are credential names only.
export function SpecFields({ value, onChange, choices }: { value: SpecValues; onChange: (v: SpecValues) => void; choices: SpecChoices }) {
  const text = (k: keyof SpecValues) => (e: { currentTarget: { value: string } }) => onChange({ ...value, [k]: e.currentTarget.value });
  const area = (k: keyof Fields, label: string, description: string, placeholder?: string) =>
    <Textarea label={label} description={description} placeholder={placeholder} autosize minRows={2} spellCheck={false} value={value[k]} onChange={text(k)} />;
  return (
    <SimpleGrid cols={{ base: 1, sm: 2 }}>
      {value.name !== undefined && <TextInput label="Name" required autoComplete="off" value={value.name} onChange={text("name")} />}
      <NativeSelect label="Type" description="harness family" value={value.type} onChange={text("type")} data={choices.types} />
      <TextInput label="Version" description="exact X.Y.Z the image installs" required placeholder="2.1.287" pattern="[0-9]+\.[0-9]+\.[0-9]+" spellCheck={false} value={value.version} onChange={text("version")} />
      <TextInput label="Version constraint" description="runtime check; empty = exactly the version" placeholder="2.x" spellCheck={false} value={value.versionConstraint} onChange={text("versionConstraint")} />
      <NativeSelect label="Principal credential" required value={value.principal} onChange={text("principal")} data={[{ value: "", label: "—" }, ...choices.principals]} />
      <TextInput label="Model id" description="empty = harness default" placeholder="claude-opus-5-5" spellCheck={false} value={value.modelId} onChange={text("modelId")} />
      <TextInput label="Effort" description="empty = harness default" spellCheck={false} value={value.effort} onChange={text("effort")} />
      <NativeSelect label="Policy mode" description="Claude permission mode" value={value.mode} onChange={text("mode")} data={[{ value: "", label: "unset = bypassPermissions (legacy)" }, ...choices.modes]} />
      <NativeSelect label="Claude provider" required value={value.provider} onChange={text("provider")} data={choices.providers} />
      {area("headers", "Principal header rules", "sent to the provider destination after the credential, one per line: NAME += ITEM ensures ITEM is in a comma-list header, NAME = VALUE sets it; not secrets", "anthropic-beta += oauth-2025-04-20")}
      {area("allow", "Allow", "permission rules, one per line", "Bash(go test:*)")}
      {area("deny", "Deny", "permission rules, one per line")}
      {area("providerEnv", "Provider env", "non-secret, one KEY=VALUE per line (e.g. Vertex project/region); credentials go in the principal, never here", "CLOUD_ML_REGION=us-east5")}
      {area("plugins", "Plugins", "one per line")}
      {area("settings", "Settings", "a Claude settings.json fragment as a JSON object — preferences only", "{}")}
      <TextInput label="Note" description="operator hint (≤ 300 bytes)" maxLength={300} spellCheck={false} value={value.note} onChange={text("note")} />
    </SimpleGrid>
  );
}
