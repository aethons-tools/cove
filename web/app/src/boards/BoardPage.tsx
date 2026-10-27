import { Anchor, Badge, Group, Loader, Text } from "@mantine/core";
import { useQuery } from "@tanstack/react-query";
import { lazy, Suspense } from "react";
import { Link, useParams } from "react-router";
import { apiGet, type ApiError } from "../api/client";
import type { BoardInfo } from "../api/types";

// BoardEditor is its own chunk (Excalidraw), loaded when a board opens.
const BoardEditor = lazy(() => import("./BoardEditor"));

// BoardPage is /app/next/boards/:id: the board under a bar back to its
// conversation; a board the viewer may not read never loads the editor.
export function BoardPage() {
  const { id = "" } = useParams();
  const q = useQuery<BoardInfo, ApiError>({ queryKey: ["board", id], queryFn: () => apiGet<BoardInfo>(`boards/${encodeURIComponent(id)}`) });
  if (q.isPending) return <Loader m="md" size="sm" />;
  if (!q.data) return <Text m="md">{q.error?.status === 404 ? "This whiteboard isn't available to you." : q.error?.message}</Text>;
  const b = q.data;
  return (
    <div style={{ display: "flex", flexDirection: "column", height: "calc(100dvh - var(--app-shell-header-height) - 2 * var(--app-shell-padding))" }}>
      <Group gap="sm" px="sm" py={6} style={{ borderBottom: "1px solid var(--mantine-color-default-border)" }}>
        <Anchor component={Link} to={`/app/next/c/${encodeURIComponent(b.channel)}`} size="sm">← Back to chat</Anchor>
        <Text fw={700} size="sm">{b.title}</Text>
        {b.readOnly && <Badge size="xs" variant="light">view only</Badge>}
      </Group>
      <div style={{ flex: 1, minHeight: 0, position: "relative" }}>
        <Suspense fallback={<Loader m="md" size="sm" />}><BoardEditor key={b.id} id={b.id} readOnly={b.readOnly} /></Suspense>
      </div>
    </div>
  );
}
