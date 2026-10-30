import { Anchor, Badge, Group, Loader, Text } from "@mantine/core";
import { useQuery } from "@tanstack/react-query";
import { lazy, Suspense, useEffect, type JSX } from "react";
import { apiGet, type ApiError } from "../api/client";
import type { BoardInfo } from "../api/types";
import { APP } from "../paths";
import { Problem } from "../Problem";
import type { Bridge } from "./bridge";

// BoardEditor is its own chunk (Excalidraw), loaded once the board is.
const BoardEditor = lazy(() => import("../boards/BoardEditor"));

// BoardTab is a whiteboard in its own editor tab: the board under a bar
// with its title and a way to the web app; a board the viewer may not read
// never loads the editor. The tab's title is the board's.
export function BoardTab({ bridge, id }: { bridge: Bridge; id: string }): JSX.Element {
  const path = `${APP}boards/${encodeURIComponent(id)}`;
  const q = useQuery<BoardInfo, ApiError>({ queryKey: ["board", id], queryFn: () => apiGet<BoardInfo>(`boards/${encodeURIComponent(id)}`) });
  const title = q.data?.title;
  useEffect(() => {
    if (title !== undefined) bridge.post({ type: "title", title });
  }, [bridge, title]);
  if (q.isPending) return <Loader m="md" size="sm" />;
  if (!q.data) {
    return q.error?.status === 404 ? <Text m="md" size="sm">This whiteboard isn't available to you.</Text> : <Problem error={q.error!} here={path} />;
  }
  const b = q.data;
  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100vh" }}>
      <Group gap="sm" px="sm" py={6} wrap="nowrap" style={{ borderBottom: "1px solid var(--mantine-color-default-border)" }}>
        <Text fw={700} size="sm" truncate="end">{b.title}</Text>
        {b.readOnly && <Badge size="xs" variant="light">view only</Badge>}
        <Anchor component="button" size="sm" ml="auto" style={{ whiteSpace: "nowrap" }}
          onClick={() => bridge.post({ type: "open", intent: { kind: "web", path } })}>Open in the web app</Anchor>
      </Group>
      <div style={{ flex: 1, minHeight: 0, position: "relative" }}>
        <Suspense fallback={<Loader m="md" size="sm" />}><BoardEditor key={b.id} id={b.id} readOnly={b.readOnly} /></Suspense>
      </div>
    </div>
  );
}
