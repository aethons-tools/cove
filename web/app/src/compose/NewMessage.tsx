import { Stack, Text, Title } from "@mantine/core";
import { useNavigate, useSearchParams } from "react-router";
import { useBoot } from "../BootContext";
import { Composer } from "../composer/Composer";
import { APP } from "../paths";

// NewMessage starts a conversation with ?to=; the first send lands in it.
export function NewMessage() {
  const [params] = useSearchParams();
  const to = params.get("to") ?? "";
  const label = params.get("label") ?? to;
  const nav = useNavigate();
  const boot = useBoot();
  return (
    <Stack gap={0} h="calc(100dvh - var(--app-shell-header-height) - 2 * var(--app-shell-padding))">
      <div style={{ padding: "var(--mantine-spacing-xs) var(--mantine-spacing-sm)", borderBottom: "1px solid var(--mantine-color-default-border)" }}>
        <Title order={2} size="h4">New message</Title>
        <Text size="xs" c="dimmed">to {label}</Text>
      </div>
      <Text c="dimmed" flex={1} p="md">New conversation with {label}.</Text>
      <Composer key={to} to={to} label={label} attachMax={boot.attachMax}
        onSent={(s) => nav(`${APP}c/${encodeURIComponent(s.channel)}`, { replace: true })} />
    </Stack>
  );
}
