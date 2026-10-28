import { Anchor, Badge, Card, Group, Loader, Stack, Text, Title } from "@mantine/core";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useLocation, useNavigate } from "react-router";
import type { ApiError } from "../../api/client";
import { Problem } from "../../Problem";
import type { Created, SpecsResponse } from "../api/types";
import { adminApi, nodeHref } from "../paths";
import { ConfirmAction } from "../ui/ConfirmAction";
import { DataTable } from "../ui/DataTable";
import { EditForm } from "../ui/EditForm";
import { ManagedBadge, driftNote } from "../ui/ManagedBadge";
import { useAdminMutation } from "../ui/useAdminMutation";
import { SpecFields, type SpecValues } from "./SpecFields";

export const specsKey = ["admin", "model-specs"];
const enc = encodeURIComponent;
const mono = (s: string, unset = "—") => s ? <Text size="sm" ff="monospace">{s}</Text> : <Text size="sm" c="dimmed">{unset}</Text>;

// ModelSpecsPage is Config › Model Specs: how agents' harnesses run, and New
// model-spec.
export function ModelSpecsPage() {
  const { data, error, isPending } = useQuery<SpecsResponse, ApiError>({ queryKey: specsKey, queryFn: () => adminApi.get("model-specs") });
  const here = useLocation();
  if (isPending) return <Loader size="sm" />;
  if (error) return <Problem error={error} here={here.pathname} />;
  return <Specs d={data} />;
}

function Specs({ d }: { d: SpecsResponse }) {
  const navigate = useNavigate();
  const [v, setV] = useState<SpecValues>({ ...d.new, name: "" });
  const create = useAdminMutation(() => adminApi.post<Created>("model-specs", v), { invalidate: [specsKey], onSuccess: (c) => void navigate(nodeHref(c.path)) });
  const remove = useAdminMutation((name: string) => adminApi.del(`model-specs/${enc(name)}`), { invalidate: [specsKey] });
  return (
    <Stack>
      <div>
        <Title order={2}>Model-specs</Title>
        <Text size="sm" c="dimmed">How agents' harnesses run — who as, on what model, under which policy — open one to edit it</Text>
      </div>
      <details>
        <summary style={{ cursor: "pointer" }}>New model-spec</summary>
        <Card withBorder mt="xs">
          <EditForm submit="Create model-spec" onSubmit={() => create.mutateAsync(undefined)}>
            <SpecFields value={v} onChange={setV} choices={d.choices} />
          </EditForm>
        </Card>
      </details>
      <DataTable rows={d.specs} rowKey={(s) => s.name} empty="No model-specs." columns={[
        { key: "n", label: "Name", render: (s) => <Group gap={6}><Anchor component={Link} to={nodeHref("config/model-specs/" + enc(s.name))} fw={600} ff="monospace">{s.name}</Anchor><ManagedBadge managed={s.managed} /></Group> },
        { key: "t", label: "Type", render: (s) => mono(s.type) },
        { key: "v", label: "Version", render: (s) => <Group gap={4}>{mono(s.version)}{s.versionConstraint && <Badge size="sm" variant="light" color="gray" title="runtime version constraint">{s.versionConstraint}</Badge>}</Group> },
        { key: "p", label: "Principal", render: (s) => mono(s.principal) },
        { key: "m", label: "Model", render: (s) => s.modelId ? <Group gap={4}>{mono(s.modelId)}{s.effort && <Badge size="sm" variant="light" color="gray">{s.effort}</Badge>}</Group> : <Text size="sm" c="dimmed">default</Text> },
        { key: "o", label: "Policy", render: (s) => mono(s.mode, "default") },
        { key: "r", label: "Provider", render: (s) => mono(s.provider) },
        { key: "u", label: "Used by", render: (s) => s.usedBy || <Text span c="dimmed">0</Text> },
        { key: "d", label: "", render: (s) => <ConfirmAction label="Delete" confirm={`Delete model-spec ${s.name}?${driftNote(s.managed) ? " " + driftNote(s.managed) : ""}`} onConfirm={() => remove.mutateAsync(s.name)} /> },
      ]} />
    </Stack>
  );
}
