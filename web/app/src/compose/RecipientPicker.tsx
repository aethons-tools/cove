import { Badge, Button, Checkbox, Group, Modal, ScrollArea, Stack, Text, TextInput, UnstyledButton } from "@mantine/core";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useNavigate } from "react-router";
import { apiGet, type ApiError } from "../api/client";
import type { NewMessageOption, RecipientsResponse } from "../api/types";
import { APP } from "../paths";

// RecipientPicker lists whom the viewer may start a conversation with,
// grouped by project: people (one for a DM; tick several for a group DM),
// agents (in the agent's own channel), units of work (Log: its work log),
// and rooms. Picking one opens the New message composer
// (onPick: the caller's instead, as the VS Code rail does).
export function RecipientPicker({ opened, onClose, onPick, returnFocus = true }: { opened: boolean; onClose: () => void; onPick?: (o: { to: string; label: string }) => void; returnFocus?: boolean }) {
  const [q, setQ] = useState("");
  const [group, setGroup] = useState<NewMessageOption[]>([]);
  const nav = useNavigate();
  const { data } = useQuery<RecipientsResponse, ApiError>({ queryKey: ["recipients"], queryFn: () => apiGet("recipients"), enabled: opened });
  const shown = (data?.options ?? []).filter((o) => o.label.toLowerCase().includes(q.trim().toLowerCase()));
  const projects = [...new Set(shown.map((o) => o.project))];
  const close = () => {
    setGroup([]);
    onClose();
  };
  const pick = (to: string, label: string) => {
    close();
    if (onPick) onPick({ to, label });
    else nav(`${APP}new?to=${encodeURIComponent(to)}&label=${encodeURIComponent(label)}`);
  };
  // A group DM is of people in one project.
  const toggle = (o: NewMessageOption) => setGroup((g) => (g.some((x) => x.to === o.to) ? g.filter((x) => x.to !== o.to) : [...g, o]));
  const groupLabel = group.map((o) => o.label).join(", ");
  const groupTo = group.length === 1 ? group[0]!.to : `chat:${group.map((o) => o.to).join(",")}`;
  return (
    <Modal opened={opened} onClose={close} title="New message" returnFocus={returnFocus}>
      <TextInput data-autofocus placeholder="Search people, agents, work, rooms…" value={q} onChange={(e) => setQ(e.currentTarget.value)} mb="sm" />
      <ScrollArea.Autosize mah={360}>
        {projects.map((p) => (
          <Stack key={p} gap={2} mb="xs">
            <Text size="xs" fw={700} c="dimmed" tt="uppercase">{p}</Text>
            {shown.filter((o) => o.project === p).map((o) => {
              const name = <Group gap="xs"><Text size="sm">{o.label}</Text><Badge size="xs" variant="light">{o.kind}</Badge></Group>;
              if (o.kind === "work") {
                return (
                  <Group key={o.to} px="xs" py={4} gap="xs" justify="space-between" wrap="nowrap">
                    {name}
                    <Button size="compact-xs" variant="light" aria-label={`Log of ${o.label}`} onClick={() => pick(o.to, `the log of ${o.label}`)}>Log</Button>
                  </Group>
                );
              }
              const row = <UnstyledButton key={o.to} px="xs" py={4} onClick={() => pick(o.to, o.label)}>{name}</UnstyledButton>;
              if (o.kind !== "person") return row;
              return (
                <Group key={o.to} gap={0} wrap="nowrap">
                  <Checkbox size="xs" pl="xs" aria-label={`Add ${o.label} to a group DM`} checked={group.some((x) => x.to === o.to)}
                    disabled={group.length > 0 && group[0]!.project !== o.project} onChange={() => toggle(o)} />
                  {row}
                </Group>
              );
            })}
          </Stack>
        ))}
        {data && shown.length === 0 && <Text size="sm" c="dimmed">No one matches.</Text>}
      </ScrollArea.Autosize>
      {group.length > 0 && (
        <Group justify="flex-end" mt="sm">
          <Button size="xs" onClick={() => pick(groupTo, groupLabel)}>Message {groupLabel}</Button>
        </Group>
      )}
    </Modal>
  );
}
