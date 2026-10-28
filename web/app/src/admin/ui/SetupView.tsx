import { Alert, Badge, Button, Code, Group, Loader, Stack, Text } from "@mantine/core";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useState } from "react";
import type { ApiError } from "../../api/client";
import type { SetupResponse, SetupSection } from "../api/types";
import { adminApi } from "../paths";
import { DiffView } from "./DiffView";

// SetupView is a studio's or a role's setup, read from path under the admin
// API: one collapsible block per section, with a section filter and (two or
// more managers) the image's manager. Every text in it is the server's
// SetupDisplayText.
export function SetupView({ path }: { path: string }) {
  const [only, setOnly] = useState("");
  const [manager, setManager] = useState("");
  const q = new URLSearchParams();
  if (only) q.set("sections", only);
  if (manager) q.set("manager", manager);
  const qs = q.toString();
  // An inspection reads images on the launcher host and is audit-logged:
  // only when asked (opened, or a selector changed) — never on focus, never
  // retried — keeping the chips while the next one loads.
  const { data, error, isPending, isPlaceholderData } = useQuery<SetupResponse, ApiError>({
    queryKey: ["admin", "setup", path, only, manager], queryFn: () => adminApi.get(path + (qs ? `?${qs}` : "")),
    staleTime: Infinity, refetchOnWindowFocus: false, retry: false, placeholderData: keepPreviousData,
  });
  const chip = (label: string, on: boolean, pick: () => void) =>
    <Button key={label} size="compact-xs" variant={on ? "filled" : "light"} color={on ? "jam" : "gray"} onClick={pick}>{label}</Button>;
  return (
    <Stack gap="xs">
      {data && data.managers.length > 1 && (
        <Group gap={4} aria-label="Image manager"><Text size="sm" c="dimmed">image on</Text>
          {data.managers.map((m) => chip(m, m === data.manager, () => setManager(m)))}
        </Group>
      )}
      <Group gap={4}>
        {chip("All", only === "", () => setOnly(""))}
        {(data?.sectionNames ?? []).map((n) => chip(n, only === n, () => setOnly(n)))}
      </Group>
      {isPending || isPlaceholderData ? <Group gap="xs"><Loader size="xs" /><Text size="sm" c="dimmed">Loading the setup… (a first read of the image on the launcher host can take a few seconds)</Text></Group>
        : error ? <Alert color="red">{error.message}</Alert>
        : <Sections s={data} />}
    </Stack>
  );
}

function Sections({ s }: { s: SetupResponse }) {
  return (
    <Stack gap="xs">
      {s.note && <Alert color="gray">{s.note}</Alert>}
      {s.stale > 0 && <Text size="sm" c="dimmed">{s.stale} section(s) stale vs role — a raise now would deliver something else.</Text>}
      {s.sections.map((sec) => <Section key={sec.name} sec={sec} />)}
    </Stack>
  );
}

function Section({ sec: initial }: { sec: SetupSection }) {
  const [sec, setSec] = useState(initial);
  const [loading, setLoading] = useState(false);
  // A stale section's diff loads in place (a studio's; a role has none).
  const loadDiff = async () => {
    setLoading(true);
    try { setSec(await adminApi.get<SetupSection>(sec.diffPath)); } finally { setLoading(false); }
  };
  return (
    <details open={sec.open} style={{ border: "1px solid var(--mantine-color-default-border)", borderRadius: 8, padding: "6px 12px" }}>
      <summary style={{ cursor: "pointer" }}>
        <Group gap={6} display="inline-flex">
          <Text span fw={600} ff="monospace">{sec.name}</Text>
          <Badge size="sm" variant="light" color={sec.source === "unavailable" ? "gray" : "blue"}>{sec.source}</Badge>
          {sec.diffPath ? <Button size="compact-xs" color="yellow" variant="light" loading={loading} onClick={(e) => { e.preventDefault(); void loadDiff(); }}
            title="Show this section's diff against a raise of its role now">stale vs role</Button>
            : sec.stale && <Badge size="sm" color="yellow" variant="light">stale vs role</Badge>}
          {sec.at && <Text span size="xs" c="dimmed">{sec.at}</Text>}
        </Group>
      </summary>
      <Stack gap={6} mt={6}>
        {sec.reason && <Text size="sm" c="dimmed">{sec.reason}</Text>}
        {sec.files.length > 0 ? (
          <>
            <Code block>{sec.head}</Code>
            {sec.files.map((f) => (
              <details key={f.path}><summary style={{ cursor: "pointer" }}><Text span size="sm" ff="monospace">{f.path}</Text> <Text span size="xs" c="dimmed">{f.bytes.toLocaleString()} B</Text></summary>
                <Code block>{f.body}</Code></details>
            ))}
          </>
        ) : sec.body && <Code block>{sec.body}</Code>}
        {sec.diffNote && <Text size="sm" c="dimmed">{sec.diffNote}</Text>}
        {sec.diffed && (
          <>
            <Text size="xs" fw={600}>This studio (−) against a raise of its role now (+)</Text>
            {sec.diffTooLarge ? <Text size="sm" c="dimmed">Too large to diff here.</Text> : sec.diffSame ? <Text size="sm" c="dimmed">Same as a raise now.</Text> : <DiffView lines={sec.diff} />}
          </>
        )}
      </Stack>
    </details>
  );
}
