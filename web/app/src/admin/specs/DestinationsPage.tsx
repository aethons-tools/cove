import { Anchor, Badge, Card, Group, Loader, Stack, Text, Title } from "@mantine/core";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useLocation, useNavigate } from "react-router";
import type { ApiError } from "../../api/client";
import { Problem } from "../../Problem";
import type { Created, DestinationsResponse } from "../api/types";
import { adminApi, nodeHref } from "../paths";
import { AttnFlag } from "../ui/AttnFlag";
import { ConfirmAction } from "../ui/ConfirmAction";
import { DataTable } from "../ui/DataTable";
import { EditForm } from "../ui/EditForm";
import { ManagedBadge, driftNote } from "../ui/ManagedBadge";
import { useAdminMutation } from "../ui/useAdminMutation";
import { DestFields, destBody, type DestValues } from "./DestFields";

export const destsKey = ["admin", "destinations"];
const enc = encodeURIComponent;
const mono = (s: string) => s ? <Text size="sm" ff="monospace">{s}</Text> : <Text size="sm" c="dimmed">—</Text>;

// deleteDestConfirm is a destination's Delete confirm.
export const deleteDestConfirm = (name: string, usedBy: number, drift: string) =>
  `Delete destination ${name}?${usedBy ? ` ${usedBy} role(s) list it.` : ""}${drift ? " " + drift : ""}`;

// DestinationsPage is Config › Destinations: the upstreams the broker routes
// agent traffic to, and New destination.
export function DestinationsPage() {
  const { data, error, isPending } = useQuery<DestinationsResponse, ApiError>({ queryKey: destsKey, queryFn: () => adminApi.get("destinations") });
  const here = useLocation();
  const navigate = useNavigate();
  const [v, setV] = useState<DestValues>({ name: "", route: "", upstream: "", identityIn: "bearer", apply: "bearer", credName: "", env: "", allowPaths: "",
    note: "", logHeadersIn: "none", logHeadersOut: "none", git: false });
  const create = useAdminMutation(() => adminApi.post<Created>("destinations", destBody(v)), { invalidate: [destsKey], onSuccess: (c) => void navigate(nodeHref(c.path)) });
  const remove = useAdminMutation((name: string) => adminApi.del(`destinations/${enc(name)}`), { invalidate: [destsKey] });
  return (
    <Stack>
      <div>
        <Title order={2}>Destinations</Title>
        <Text size="sm" c="dimmed">Upstreams the broker routes agent traffic to — open one to edit it</Text>
      </div>
      <details>
        <summary style={{ cursor: "pointer" }}>New destination</summary>
        <Card withBorder mt="xs">
          <EditForm submit="Create destination" onSubmit={() => create.mutateAsync(undefined)}>
            <DestFields value={v} onChange={setV} methods={data?.methods ?? ["bearer"]} />
          </EditForm>
        </Card>
      </details>
      {isPending ? <Loader size="sm" /> : error ? <Problem error={error} here={here.pathname} /> : (
        <DataTable rows={data.destinations} rowKey={(d) => d.name} empty="No destinations." columns={[
          { key: "n", label: "Name", render: (d) => <Group gap={6}><Anchor component={Link} to={nodeHref("config/destinations/" + enc(d.name))} fw={600} ff="monospace">{d.name}</Anchor><AttnFlag flag={d.attn} /><ManagedBadge managed={d.managed} /></Group> },
          { key: "r", label: "Route", render: (d) => mono(d.route) },
          { key: "u", label: "Upstream", render: (d) => mono(d.upstream) },
          { key: "a", label: "Auth", render: (d) => mono(d.identityIn || d.apply ? `${d.identityIn} → ${d.apply}` : "") },
          { key: "c", label: "Credential", render: (d) => mono(d.credName) },
          { key: "e", label: "Agent connector", render: (d) => d.envKeys.length || d.gitRouted
            ? <Group gap={4}>{d.envKeys.map((k) => <Badge key={k} size="sm" variant="light" color="gray" tt="none">{k}</Badge>)}{d.gitRouted && <Badge size="sm" variant="light">git</Badge>}</Group>
            : <Text size="sm" c="dimmed">—</Text> },
          { key: "x", label: "Roles", render: (d) => d.usedBy || <Text span c="dimmed">0</Text> },
          { key: "d", label: "", render: (d) => <ConfirmAction label="Delete" confirm={deleteDestConfirm(d.name, d.usedBy, driftNote(d.managed))} onConfirm={() => remove.mutateAsync(d.name)} /> },
        ]} />
      )}
    </Stack>
  );
}
