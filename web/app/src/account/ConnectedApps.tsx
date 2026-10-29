import { Badge, Button, Group, Loader, Modal, Stack, Text } from "@mantine/core";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { API, apiAt, apiGet, type ApiError } from "../api/client";
import type { AppTokenRow, AppTokensResponse } from "../api/types";
import { Problem } from "../Problem";

const rtf = new Intl.RelativeTimeFormat("en", { numeric: "always" });
const UNITS: [Intl.RelativeTimeFormatUnit, number][] = [["day", 86_400_000], ["hour", 3_600_000], ["minute", 60_000]];

// ago renders an instant relative to now ("2 minutes ago", "in 3 days").
export function ago(iso: string, now = Date.now()): string {
  const diff = new Date(iso).getTime() - now;
  for (const [unit, ms] of UNITS) {
    if (Math.abs(diff) >= ms) return rtf.format(Math.round(diff / ms), unit);
  }
  return "just now";
}

// AppTokenList is the connected apps' rows, shared by the account dialog and
// the admin user page; revoke renders each row's Revoke control.
export function AppTokenList({ tokens, empty, revoke }: { tokens: AppTokenRow[]; empty: string; revoke: (t: AppTokenRow) => ReactNode }) {
  if (tokens.length === 0) return <Text size="sm" c="dimmed">{empty}</Text>;
  return (
    <Stack gap="xs">
      {tokens.map((t) => (
        <Group key={t.id} data-token={t.id} justify="space-between" wrap="nowrap" align="flex-start">
          <Stack gap={2}>
            <Group gap="xs">
              <Text size="sm" fw={500}>{t.label}</Text>
              {t.operator && <Badge size="sm" color="yellow">operator rights</Badge>}
            </Group>
            <Text size="xs" c="dimmed">Last used {ago(t.lastUsed)} · Expires {new Date(t.expires).toLocaleDateString()}</Text>
          </Stack>
          {revoke(t)}
        </Group>
      ))}
    </Stack>
  );
}

// ConnectedApps is the account menu's dialog: the apps (VS Code) signed in as
// the viewer, each revocable.
export function ConnectedApps({ opened, onClose }: { opened: boolean; onClose: () => void }) {
  const client = useQueryClient();
  const { data, error, isPending } = useQuery<AppTokensResponse, ApiError>({ queryKey: ["app-tokens"], queryFn: () => apiGet<AppTokensResponse>("app-tokens"), enabled: opened });
  const revoke = useMutation({
    mutationFn: (id: string) => apiAt(API).del(`app-tokens/${encodeURIComponent(id)}`),
    onSettled: () => client.invalidateQueries({ queryKey: ["app-tokens"] }),
  });
  return (
    <Modal opened={opened} onClose={onClose} title="Connected apps">
      {error ? <Problem error={error} here={location.pathname} />
        : isPending ? <Loader size="sm" />
        : <AppTokenList tokens={data.tokens} empty="No apps are connected. Sign in from VS Code with Jam: Sign In." revoke={(t) => (
          <Button size="xs" color="red" variant="light" loading={revoke.isPending && revoke.variables === t.id} onClick={() => revoke.mutate(t.id)}>Revoke</Button>
        )} />}
      {revoke.error && <Text size="sm" c="red" mt="sm">{revoke.error.message}</Text>}
    </Modal>
  );
}
