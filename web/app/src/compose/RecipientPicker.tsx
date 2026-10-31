import { Badge, Group, Modal, ScrollArea, Stack, Text, TextInput, UnstyledButton } from "@mantine/core";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useNavigate } from "react-router";
import { apiGet, type ApiError } from "../api/client";
import type { RecipientsResponse } from "../api/types";
import { APP } from "../paths";

// RecipientPicker lists whom the viewer may start a conversation with,
// grouped by project; picking one opens the New message composer (onPick:
// the caller's instead, as the VS Code rail does).
export function RecipientPicker({ opened, onClose, onPick }: { opened: boolean; onClose: () => void; onPick?: (o: { to: string; label: string }) => void }) {
  const [q, setQ] = useState("");
  const nav = useNavigate();
  const { data } = useQuery<RecipientsResponse, ApiError>({ queryKey: ["recipients"], queryFn: () => apiGet("recipients"), enabled: opened });
  const shown = (data?.options ?? []).filter((o) => o.label.toLowerCase().includes(q.trim().toLowerCase()));
  const projects = [...new Set(shown.map((o) => o.project))];
  return (
    <Modal opened={opened} onClose={onClose} title="New message">
      <TextInput data-autofocus placeholder="Search people, sessions, rooms…" value={q} onChange={(e) => setQ(e.currentTarget.value)} mb="sm" />
      <ScrollArea.Autosize mah={360}>
        {projects.map((p) => (
          <Stack key={p} gap={2} mb="xs">
            <Text size="xs" fw={700} c="dimmed" tt="uppercase">{p}</Text>
            {shown.filter((o) => o.project === p).map((o) => (
              <UnstyledButton key={o.to} px="xs" py={4} onClick={() => {
                onClose();
                if (onPick) onPick({ to: o.to, label: o.label });
                else nav(`${APP}new?to=${encodeURIComponent(o.to)}&label=${encodeURIComponent(o.label)}`);
              }}>
                <Group gap="xs"><Text size="sm">{o.label}</Text><Badge size="xs" variant="light">{o.kind}</Badge></Group>
              </UnstyledButton>
            ))}
          </Stack>
        ))}
        {data && shown.length === 0 && <Text size="sm" c="dimmed">No one matches.</Text>}
      </ScrollArea.Autosize>
    </Modal>
  );
}
