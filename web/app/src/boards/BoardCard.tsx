import { Anchor, Image, Paper, Text } from "@mantine/core";
import { Link } from "react-router";
import type { MessageRow } from "../api/types";
import { timeOf } from "../conversation/days";

// BoardCard is a board's card where it was started: its thumbnail (the
// server's render) and a link into the board.
export function BoardCard({ m }: { m: MessageRow }) {
  const b = m.board!;
  const href = `/app/next/boards/${encodeURIComponent(b.id)}`;
  return (
    <Paper data-seq={m.seq} data-board={b.id} withBorder p="xs" my={4} maw={360}>
      <Text size="sm" fw={600}>🖍 {b.title} <Text span size="xs" c="dimmed" fw={400}>{m.mine ? "you" : m.from} · {timeOf(m.time)}</Text></Text>
      <Anchor component={Link} to={href} display="block" mt={4}>
        <Image loading="lazy" src={`/app/user/boards/${encodeURIComponent(b.id)}/thumb.png`} alt={b.title} radius="sm" bg="white" fit="contain" mah={200} />
      </Anchor>
      <Anchor component={Link} to={href} size="sm">Open</Anchor>
    </Paper>
  );
}
