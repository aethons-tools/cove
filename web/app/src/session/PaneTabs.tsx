import { Anchor, Badge, Image, Loader, SimpleGrid, Stack, Text, Title, UnstyledButton } from "@mantine/core";
import { lazy, Suspense, type ReactNode } from "react";
import type { SessionPane } from "../api/types";
import type { PaneTab } from "./layout";
import { usePaneTab } from "./useSession";

const ASK_COLOR: Record<string, string> = { open: "yellow", answered: "green", withdrawn: "gray" };
const enc = encodeURIComponent;

function Section({ title, children }: { title: string; children: ReactNode }) {
  return <Stack gap={4}><Title order={4} size="xs" tt="uppercase" c="dimmed" mt="sm">{title}</Title>{children}</Stack>;
}

function Previews({ p }: { p: SessionPane }) {
  if (p.previewsOff) return <Text size="sm" c="dimmed">Previews aren't available to you here.</Text>;
  if (p.previews.length === 0) return <Text size="sm" c="dimmed">No previews exposed. A session exposes one with its <code>expose</code> tool.</Text>;
  return (
    <Stack gap={4}>
      {p.previews.map((v) => (
        <Anchor key={v.name} href={v.url} target="_blank" rel="noopener" size="sm">{v.name} <Text span size="xs" c="dimmed">port {v.port} ↗</Text></Anchor>
      ))}
    </Stack>
  );
}

function Artifacts({ p, channel, onAsk }: { p: SessionPane; channel: string; onAsk: (id: string) => void }) {
  if (!p.boards.length && !p.files.length && !p.asks.length && !p.links.length) return <Text size="sm" c="dimmed">Nothing shared here yet.</Text>;
  return (
    <Stack gap="xs">
      {p.boards.length > 0 && <Section title="Whiteboards">
        <SimpleGrid cols={2} spacing="xs">
          {/* Boards open in the current Inbox until the app has them (slice 7). */}
          {p.boards.map((b) => (
            <Anchor key={b.id} href={`/app/user/?c=${enc(b.channel || channel)}&board=${enc(b.id)}`} size="xs">
              <Image loading="lazy" src={`/app/user/boards/${enc(b.id)}/thumb.png`} alt="" radius="sm" fit="contain" h={90} bg="white" />
              {b.title}
            </Anchor>
          ))}
        </SimpleGrid>
      </Section>}
      {p.files.length > 0 && <Section title="Files">
        {p.files.map((f) => f.image
          ? <Anchor key={f.id} href={`/app/user/blobs/${enc(f.id)}`} target="_blank" rel="noopener"><Image loading="lazy" src={`/app/user/blobs/${enc(f.id)}`} alt={f.name} mah={120} w="auto" fit="contain" /></Anchor>
          : <Anchor key={f.id} href={`/app/user/blobs/${enc(f.id)}`} download size="sm">📄 {f.name} <Text span size="xs" c="dimmed">{f.size}</Text></Anchor>)}
      </Section>}
      {p.asks.length > 0 && <Section title="Asks">
        {p.asks.map((a) => (
          <UnstyledButton key={a.id} fz="sm" onClick={() => onAsk(a.id)}>{a.question} <Badge size="xs" variant="light" color={ASK_COLOR[a.state]}>{a.state}</Badge></UnstyledButton>
        ))}
      </Section>}
      {p.links.length > 0 && <Section title="Links">
        {p.links.map((l) => <Anchor key={l.url} href={l.url} target="_blank" rel="noopener" title={l.url} size="sm" truncate="end">{l.label}</Anchor>)}
      </Section>}
    </Stack>
  );
}

function Status({ p }: { p: SessionPane }) {
  if (p.status.length === 0) return <Text size="sm" c="dimmed">No status yet.</Text>;
  return (
    <dl style={{ display: "grid", gridTemplateColumns: "auto 1fr", gap: "4px 12px", margin: "6px 0", fontSize: 13 }}>
      {p.status.map((r) => (
        <div key={r.name} style={{ display: "contents" }}>
          <dt style={{ color: "var(--mantine-color-dimmed)" }}>{r.name}</dt><dd style={{ margin: 0 }}>{r.value}</dd>
        </div>
      ))}
    </dl>
  );
}

function Loaded({ id, channel, tab, onAsk }: { id: string; channel: string; tab: "previews" | "artifacts" | "status"; onAsk: (id: string) => void }) {
  const q = usePaneTab(id, tab);
  if (q.isPending) return <Loader size="sm" />;
  if (!q.data) return <Text size="sm" c="red">{q.error?.message}</Text>;
  if (tab === "previews") return <Previews p={q.data} />;
  if (tab === "artifacts") return <Artifacts p={q.data} channel={channel} onAsk={onAsk} />;
  return <Status p={q.data} />;
}

// ThinkingTab is its own chunk, loaded when the tab first opens.
const ThinkingTab = lazy(() => import("./thinking/ThinkingTab"));

// PaneBody is one tab of the session pane. Studio comes to the app in slice
// 6b; until then it links to the current Inbox, on that tab.
export function PaneBody({ id, channel, tab, busy, onAsk }: { id: string; channel: string; tab: PaneTab; busy: boolean; onAsk: (id: string) => void }) {
  if (tab === "thinking") return <Suspense fallback={<Loader size="sm" m="sm" />}><ThinkingTab id={id} busy={busy} /></Suspense>;
  if (tab === "studio") {
    return (
      <Text size="sm">This tab shows in the current Inbox for now.{" "}
        <Anchor href={`/app/user/?c=${enc(channel)}&tab=${tab}`}>Open it in the current Inbox</Anchor>
      </Text>
    );
  }
  return <Loaded id={id} channel={channel} tab={tab} onAsk={onAsk} />;
}
