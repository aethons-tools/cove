import { Alert, Anchor, Badge, Button, Code, Group, Loader, SimpleGrid, Stack, Text, Title } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { useIsMutating, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { Link, useLocation } from "react-router";
import type { ApiError } from "../../api/client";
import { Problem } from "../../Problem";
import type { FollowStatus, Plan, Tracking, TrackingResult, TrackingSource } from "../api/types";
import { adminApi, nodeHref } from "../paths";
import { ConfirmAction } from "../ui/ConfirmAction";
import { DataTable } from "../ui/DataTable";
import { Section } from "../ui/Section";
import { useAdminMutation } from "../ui/useAdminMutation";
import { PlanView } from "./PlanView";

const key = ["admin", "tracking"];
const writing = ["admin", "tracking", "write"];

// TrackingPage is Config › Tracking: the config repo this Jam follows, the
// objects it manages, and the plan to apply it.
export function TrackingPage() {
  const { data, error, isPending } = useQuery<Tracking, ApiError>({ queryKey: key, queryFn: () => adminApi.get("tracking") });
  const here = useLocation();
  return (
    <Stack>
      <div>
        <Title order={2}>Tracking</Title>
        <Text size="sm" c="dimmed">This Jam's configuration in git — the repo it follows, the objects the repo manages, and the plan to apply it</Text>
      </div>
      {isPending ? <Loader size="sm" /> : error ? <Problem error={error} here={here.pathname} /> : <TrackingBody t={data} />}
    </Stack>
  );
}

function TrackingBody({ t }: { t: Tracking }) {
  const client = useQueryClient();
  const [plan, setPlan] = useState<Plan | null>(t.plan ?? null);
  // One tracking write at a time: each is a fetch and a plan, seconds long.
  const busy = useIsMutating({ mutationKey: writing }) > 0;
  const done = (r: TrackingResult) => {
    client.setQueryData(key, r.tracking);
    setPlan(r.tracking.plan ?? null);
    if (r.message) notifications.show({ color: "green", message: r.message });
  };
  const write = useAdminMutation((v: { path: string; body?: unknown; del?: boolean }) =>
    v.del ? adminApi.del<TrackingResult>(v.path) : adminApi.post<TrackingResult>(v.path, v.body), { onSuccess: done, mutationKey: writing });
  const diff = useAdminMutation((force: boolean) => adminApi.post<Plan>("tracking/diff", { force }), { onSuccess: setPlan, mutationKey: writing });
  const runDiff = (force: boolean) => {
    if (busy) return;
    diff.mutate(force, { onError: (e) => notifications.show({ color: "red", message: e.message }) });
  };
  return (
    <Stack>
      <SimpleGrid cols={{ base: 1, md: 2 }}>
        <Section title="Source" sub="the config repo this Jam follows">
          {t.source ? (
            <Stack gap="xs">
              <SourceFacts s={t.source} f={t.follow} />
              {t.follow.state === "awaiting-confirm" && <Text size="sm">The first apply from this repo is waiting for you: Diff, review the plan, then apply it. After that the Jam applies each new commit on its own.</Text>}
              {t.follow.error && <Alert color="red" title="The last check failed; the Jam retries every minute">{t.follow.error}</Alert>}
              {t.canGit ? <Group><Button size="xs" onClick={() => runDiff(false)} loading={diff.isPending} disabled={busy}>Diff</Button></Group>
                : <Text size="sm" c="dimmed">Git actions are not available on this serve.</Text>}
            </Stack>
          ) : (
            <Text size="sm" c="dimmed">No config repo is configured: set <Code>config.github</Code> in the serve config{t.lastCommit && `. The objects below were last applied from config @${t.lastCommit} at ${t.lastApplied}`}.</Text>
          )}
        </Section>
      </SimpleGrid>
      <Section title="Managed objects" sub={<>{t.rows.length} from the config repo{t.drifted > 0 && <> · <Badge color="yellow">{t.drifted} differ from the repo</Badge></>}{t.lastCommit && ` · last applied config @${t.lastCommit} at ${t.lastApplied}`}</>}>
        {t.statesErr && <Alert color="red" mb="xs">Could not compare with the live Jam (see the serve log); states are unknown.</Alert>}
        <DataTable rows={t.rows} rowKey={(r) => `${r.kind}/${r.name}`} empty="Nothing is managed yet — apply a config repo, or run jam config apply --dir." columns={[
          { key: "o", label: "Object", render: (r) => <Group gap={4}><Badge variant="outline" color="gray">{r.kind}</Badge>{r.path ? <Anchor component={Link} to={nodeHref(r.path)} ff="monospace">{r.name}</Anchor> : <Text span ff="monospace">{r.name}</Text>}</Group> },
          { key: "f", label: "From", render: (r) => <Text size="sm" ff="monospace">@{r.commit}</Text> },
          { key: "a", label: "Applied", render: (r) => r.applied },
          { key: "s", label: "State", render: (r) => r.state === "in-sync" ? <Text c="dimmed" size="sm">in sync</Text> : r.state ? <Badge color="yellow">{r.state}</Badge> : <Text c="dimmed" size="sm">unknown</Text> },
          { key: "x", label: "", render: (r) => <ConfirmAction label="Release" disabled={busy} confirm={`Stop managing ${r.kind} ${r.name} from the config repo? It stays in Jam, unmanaged.`}
            onConfirm={() => write.mutateAsync({ path: `tracking/managed/${encodeURIComponent(r.kind)}/${r.name.split("/").map(encodeURIComponent).join("/")}`, del: true })} /> },
        ]} />
      </Section>
      {plan && <PlanView plan={plan} busy={busy} onForce={(f) => runDiff(f)}
        onApply={() => write.mutateAsync({ path: "tracking/apply", body: { plan: plan.digest, force: plan.forced } })} />}
    </Stack>
  );
}

const FOLLOW: Record<string, { label: string; color?: string }> = {
  "in-sync": { label: "in sync" }, "awaiting-confirm": { label: "waiting for you", color: "yellow" }, failed: { label: "failed", color: "red" }, off: { label: "not checked yet" },
};

function SourceFacts({ s, f }: { s: TrackingSource; f: FollowStatus }) {
  const row = (k: string, v: ReactNode) => <Group gap="md" wrap="nowrap"><Text size="sm" c="dimmed" w={90}>{k}</Text><Text size="sm" component="div">{v}</Text></Group>;
  const state = FOLLOW[f.state] ?? { label: f.state };
  return (
    <Stack gap={4}>
      {row("Following", <>{state.color ? <Badge color={state.color}>{state.label}</Badge> : <Text span>{state.label}</Text>}{f.checked && <Text span c="dimmed"> · checked {f.checked}</Text>}</>)}
      {row("Repo", s.repoLink ? <Anchor href={s.repoLink} rel="noopener noreferrer" ff="monospace">{s.repo}</Anchor> : <Text span ff="monospace">{s.repo}</Text>)}
      {s.dir && row("Dir", <Text span ff="monospace">{s.dir}</Text>)}
      {row("Branch", <Text span ff="monospace">{s.ref}</Text>)}
      {f.head && row("Head", <Text span ff="monospace">{f.head}</Text>)}
      {row("Last applied", <>{s.commit ? (s.commitLink ? <Anchor href={s.commitLink} rel="noopener noreferrer" ff="monospace">{s.commit}</Anchor> : <Text span ff="monospace">{s.commit}</Text>) : "—"}{s.applied && <Text span c="dimmed"> at {s.applied}</Text>}</>)}
    </Stack>
  );
}
