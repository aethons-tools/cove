import { Alert, Anchor, Loader, Stack, Text, TextInput, Title } from "@mantine/core";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { Link, useLocation, useNavigate, useSearchParams } from "react-router";
import type { ApiError } from "../../api/client";
import { Problem } from "../../Problem";
import type { SearchResult } from "../api/types";
import { adminApi, nodeHref } from "../paths";
import { Section } from "../ui/Section";
import { Highlight } from "./highlight";

// SearchPage searches every page's objects and the squawks at once. Its box
// updates ?q= as it's typed; ?go=1 (Enter in the title bar) jumps straight
// to a single exact match.
export function SearchPage() {
  const here = useLocation();
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const q = params.get("q") ?? "";
  const go = params.get("go") === "1";
  const [text, setText] = useState(q);
  const [seen, setSeen] = useState(q);
  if (seen !== q) { setSeen(q); setText(q); } // ?q= changed (the title bar searched again)
  const { data, error, isPending } = useQuery<SearchResult, ApiError>({
    queryKey: ["admin", "search", q], queryFn: () => adminApi.get(`search?q=${encodeURIComponent(q)}`), placeholderData: keepPreviousData,
  });
  useEffect(() => { // type, pause, search
    if (text === q) return;
    const t = setTimeout(() => setParams(text ? { q: text } : {}, { replace: true }), 200);
    return () => clearTimeout(t);
  }, [text, q, setParams]);
  useEffect(() => {
    if (!go || !data || data.q !== q) return;
    if (data.jump) void navigate(nodeHref(data.jump), { replace: true });
    else setParams({ q }, { replace: true });
  }, [go, data, q, navigate, setParams]);
  return (
    <Stack>
      <Title order={2}>Search</Title>
      <Text size="sm" c="dimmed">agents, roles, projects, kits, destinations, model-specs, users, rooms and squawks</Text>
      <TextInput type="search" size="md" aria-label="Search everything" placeholder="Search everything…" autoFocus value={text} onChange={(e) => setText(e.currentTarget.value)} />
      {isPending ? <Loader size="sm" /> : error ? <Problem error={error} here={here.pathname} /> : <Results r={data} q={q} />}
    </Stack>
  );
}

function Results({ r, q }: { r: SearchResult; q: string }) {
  if (r.tooShort) return <Alert color="gray">Type at least 2 characters.</Alert>;
  if (!q.trim()) return <Text size="sm" c="dimmed">Search by name, id, unit, handle, route, prompt or message text.</Text>;
  if (r.groups.length === 0) return <Alert color="gray">No matches for <Text span ff="monospace">{q}</Text>.</Alert>;
  return (
    <Stack>
      <Text size="sm" c="dimmed"><span>{r.total} match(es) for</span> <Text span ff="monospace">{q}</Text></Text>
      {r.groups.map((g) => (
        <Section key={g.key} title={g.name} sub={String(g.count)} actions={g.more
          ? <Anchor component={Link} to={nodeHref(g.more)} size="sm">{g.count > g.hits.length ? `all ${g.count} in Intercom` : "open in Intercom"}</Anchor>
          : g.count > g.hits.length ? <Text size="sm" c="dimmed">showing {g.hits.length}</Text> : undefined}>
          <Stack gap={0}>
            {g.hits.map((h, i) => (
              <Anchor key={i} component={Link} to={nodeHref(h.path)} underline="never" c="inherit" py={6} style={{ borderBottom: "1px solid var(--mantine-color-default-border)" }}>
                <Text fw={600} size="sm"><Highlight text={h.title} q={q} /></Text>
                {h.sub && <Text size="xs" c="dimmed" style={{ overflowWrap: "anywhere" }}><Highlight text={h.sub} q={q} /></Text>}
              </Anchor>
            ))}
          </Stack>
        </Section>
      ))}
    </Stack>
  );
}
