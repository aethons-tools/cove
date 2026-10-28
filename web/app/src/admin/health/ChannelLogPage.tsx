import { Alert, Anchor, Button, Card, Group, Loader, NativeSelect, SimpleGrid, Stack, Text, TextInput, Title } from "@mantine/core";
import { useQuery } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { Link, useLocation, useSearchParams } from "react-router";
import type { ApiError } from "../../api/client";
import { Problem } from "../../Problem";
import type { ChannelLog } from "../api/types";
import { adminApi, nodeHref } from "../paths";
import { SquawkTable } from "../ui/SquawkTable";
import { TypeAhead } from "../ui/TypeAhead";

const FIELDS = ["project", "participant", "q", "since", "until"] as const;
type Field = (typeof FIELDS)[number];

// ChannelLogPage is every squawk in every channel (or the frozen legacy
// log), newest first, filtered by the URL's fields.
export function ChannelLogPage() {
  const here = useLocation();
  const [params, setParams] = useSearchParams();
  const search = params.toString();
  const { data, error, isPending, refetch, isFetching } = useQuery<ChannelLog, ApiError>({
    queryKey: ["admin", "intercom", search], queryFn: () => adminApi.get(`intercom${search ? "?" + search : ""}`),
  });
  return (
    <Stack>
      <Group gap="sm" align="baseline">
        <Title order={2}>Channel log</Title>
        <Text size="sm" c="dimmed">{params.get("log") === "legacy" ? "The legacy log (before the channel log), frozen" : "Every squawk in every channel"}, newest first</Text>
        <Anchor component={Link} to={nodeHref("health")} size="sm">Health</Anchor>
      </Group>
      <Filters key={search} params={params} onApply={setParams} onRefresh={() => void refetch()} refreshing={isFetching} />
      {isPending ? <Loader size="sm" /> : error ? <Problem error={error} here={here.pathname} /> : !data.configured ? (
        <Alert color="gray">The message log is unavailable.</Alert>
      ) : (
        <>
          {data.badDate && <Alert color="red">Ignored an unparseable date (expected YYYY-MM-DD).</Alert>}
          <SquawkTable rows={data.rows} filtered={data.filtered} legacy={data.legacy} />
        </>
      )}
    </Stack>
  );
}

// Filters is the log's filter form, seeded from the URL (remounted on each
// change of it).
function Filters({ params, onApply, onRefresh, refreshing }: { params: URLSearchParams; onApply: (p: URLSearchParams) => void; onRefresh: () => void; refreshing: boolean }) {
  const [log, setLog] = useState(params.get("log") === "legacy" ? "legacy" : "");
  const [f, setF] = useState(() => Object.fromEntries(FIELDS.map((k) => [k, params.get(k) ?? ""])) as Record<Field, string>);
  const set = (k: Field) => (v: string) => setF({ ...f, [k]: v });
  const apply = (e: FormEvent) => {
    e.preventDefault();
    const p = new URLSearchParams();
    if (log) p.set("log", log);
    for (const k of FIELDS) if (f[k].trim()) p.set(k, f[k].trim());
    onApply(p);
  };
  const reset = () => onApply(new URLSearchParams(log ? { log } : {}));
  const legacy = log === "legacy";
  return (
    <Card withBorder component="form" onSubmit={apply}>
      <SimpleGrid cols={{ base: 1, sm: 3, lg: 6 }}>
        <NativeSelect label="Log" value={log} onChange={(e) => setLog(e.currentTarget.value)} data={[{ value: "", label: "Channels" }, { value: "legacy", label: "Legacy" }]} />
        <TypeAhead label="Project" kind="projects" value={f.project} onChange={set("project")} />
        <TypeAhead label="Participant" description={legacy ? "kind:ref" : "an id: a user, agent, account or channel"} kind="participants"
          placeholder={legacy ? "channel:eng" : "usr_…"} value={f.participant} onChange={set("participant")} />
        <TextInput label="Body contains" value={f.q} onChange={(e) => set("q")(e.currentTarget.value)} />
        <TextInput label="Since" type="date" value={f.since} onChange={(e) => set("since")(e.currentTarget.value)} />
        <TextInput label="Until" type="date" value={f.until} onChange={(e) => set("until")(e.currentTarget.value)} />
      </SimpleGrid>
      <Group justify="flex-end" mt="sm" gap="xs">
        <Button variant="default" size="xs" onClick={reset}>Reset</Button>
        <Button variant="default" size="xs" onClick={onRefresh} loading={refreshing}>Refresh</Button>
        <Button type="submit" size="xs">Filter</Button>
      </Group>
    </Card>
  );
}
