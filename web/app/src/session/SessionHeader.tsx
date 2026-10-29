import { ActionIcon, Badge, Button, Group, Loader, Menu, Modal, Stack, Text } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { apiPost, type ApiError } from "../api/client";
import type { SessionHead } from "../api/types";
import { copyText } from "../conversation/copy";
import { PANE_TABS, TAB_LABEL, type PaneTab } from "./layout";
import { vscodeLink } from "./vscode";

type Action = "interrupt" | "compact" | "clear" | "release" | "restart" | "upgrade";
const STATE_COLOR: Record<string, string | undefined> = { wait: "yellow.8", dim: "dimmed" };

// confirmText is the question an ending (or forgetting) action asks first,
// as the old header's hx-confirm did; null = run at once.
function confirmText(a: Action, h: SessionHead): string | null {
  switch (a) {
    case "clear": return "Clear this session's conversation? The agent keeps its files and memory but forgets the conversation.";
    case "release": return "Release this session? It ends now.";
    case "restart": return `${h.restartLabel} ${h.label}?`;
    case "upgrade": return `Upgrade ${h.label} to the current image?`;
    default: return null;
  }
}
const actionLabel = (a: Action, h: SessionHead) => (a === "restart" ? h.restartLabel : a[0]!.toUpperCase() + a.slice(1));

// SessionHeader is a session's header: who it is, what it's doing, what it
// costs, its pane's tabs (when onTab is given), and its actions. The server re-checks every
// action; a refusal shows its message.
export function SessionHeader({ head: h, tab, onTab }: { head: SessionHead; tab?: PaneTab | null; onTab?: (t: PaneTab) => void }) {
  const client = useQueryClient();
  const [asking, setAsking] = useState<Action | null>(null);
  const run = useMutation<void, ApiError, Action>({
    mutationFn: (a) => apiPost(`sessions/${encodeURIComponent(h.id)}/${a}`, {}),
    onSuccess: () => {
      // An ended session's head now 404s: the view drops it.
      for (const key of ["session", "rail", "conversation", "presence"]) void client.invalidateQueries({ queryKey: [key] });
    },
    onError: (e) => notifications.show({ color: "red", message: e.message }),
  });
  const press = (a: Action) => (confirmText(a, h) ? setAsking(a) : run.mutate(a));
  const tabs = PANE_TABS.filter((t) => t !== "studio" || h.studio);
  const menu = h.ops || h.canRelease || h.canRestart || h.canUpgrade;
  return (
    <Group component="section" aria-label="Session" gap="md" px="sm" py={6} wrap="wrap"
      style={{ borderBottom: "1px solid var(--mantine-color-default-border)" }}>
      <Stack gap={0} maw={360}>
        <Group gap={6} wrap="nowrap">
          <Text fw={700} size="sm">{h.label}</Text>
          <Badge size="xs" variant="light">{h.kind}</Badge>
          {h.ticket && <Badge size="xs" variant="outline">{h.ticket}</Badge>}
        </Group>
        <Text size="xs" c="dimmed">{h.role} · {h.project}{h.owner && ` · for ${h.owner}`}</Text>
        {h.mission && <Text size="xs" truncate="end" title={h.mission}>{h.mission}</Text>}
      </Stack>
      <Group gap={4} wrap="nowrap">
        <Text size="sm" c={STATE_COLOR[h.state.class]}>{h.state.text}{h.state.tool && <> <b>{h.state.tool}</b></>}</Text>
        {h.state.class === "busy" && <Loader type="dots" size="xs" aria-hidden />}
        {h.since && <Text size="xs" c="dimmed">· {h.since}</Text>}
        {h.openAsks > 0 && <Badge size="xs" color="yellow" title="open asks">?{h.openAsks}</Badge>}
      </Group>
      {h.cost && <Text size="sm" title={h.tokens}>{h.cost} <Text span size="xs" c="dimmed">· this turn {h.turnCost}</Text></Text>}
      {h.context && <Text size="xs" c="dimmed" title={h.contextTip}>{h.context}</Text>}
      {h.uptime && <Text size="xs" c="dimmed" title="since it started">up {h.uptime}</Text>}
      <Group gap={4} ml="auto" wrap="nowrap">
        {onTab && <Group gap={2} role="tablist" aria-label="Session pane">
          {tabs.map((t) => (
            <Button key={t} size="compact-xs" role="tab" data-tab={t} aria-selected={tab === t} aria-controls="session-pane-body"
              variant={tab === t ? "light" : "subtle"} onClick={() => onTab(t)}>{TAB_LABEL[t]}</Button>
          ))}
        </Group>}
        {h.ssh && <Button size="compact-xs" variant="default" title={h.ssh}
          onClick={() => void copyText(h.ssh).then((ok) => notifications.show({ message: ok ? "Copied the SSH command" : "Couldn't copy; the command is in the button's tooltip" }))}>Copy SSH</Button>}
        {h.ssh && <Button component="a" href={vscodeLink(h.id)} size="compact-xs" variant="default"
          title="Open this session's studio in VS Code (needs the Jam extension)">Open in VS Code</Button>}
        {h.ops && <Button size="compact-xs" variant="default" title="Stop the agent's current turn" disabled={!h.canInterrupt}
          loading={run.isPending && run.variables === "interrupt"} onClick={() => press("interrupt")}>Interrupt</Button>}
        {menu && (
          <Menu position="bottom-end">
            <Menu.Target><ActionIcon variant="subtle" aria-label="More actions">⋯</ActionIcon></Menu.Target>
            <Menu.Dropdown>
              {h.ops && <Menu.Item disabled={!h.canCompact} onClick={() => press("compact")}>Compact</Menu.Item>}
              {h.ops && <Menu.Item disabled={!h.canClear} onClick={() => press("clear")}>Clear</Menu.Item>}
              {h.canRelease && <Menu.Item onClick={() => press("release")}>Release</Menu.Item>}
              {h.canRestart && <Menu.Item onClick={() => press("restart")}>{h.restartLabel}</Menu.Item>}
              {h.canUpgrade && <Menu.Item onClick={() => press("upgrade")}
                rightSection={h.upgradeState && <Text size="xs" c="dimmed">{h.upgradeState}</Text>}>Upgrade</Menu.Item>}
            </Menu.Dropdown>
          </Menu>
        )}
      </Group>
      <Modal opened={asking !== null} onClose={() => setAsking(null)} title="Confirm" size="sm">
        {asking && (
          <Stack>
            <Text size="sm">{confirmText(asking, h)}</Text>
            <Group justify="flex-end">
              <Button variant="default" onClick={() => setAsking(null)}>Cancel</Button>
              <Button color={asking === "release" || asking === "restart" ? "red" : undefined}
                onClick={() => { run.mutate(asking); setAsking(null); }}>{actionLabel(asking, h)}</Button>
            </Group>
          </Stack>
        )}
      </Modal>
    </Group>
  );
}
