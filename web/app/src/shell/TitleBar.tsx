import { ActionIcon, Anchor, Avatar, Badge, Group, Menu, Text, useMantineColorScheme } from "@mantine/core";
import { useDisclosure } from "@mantine/hooks";
import type { Bootstrap } from "../api/types";
import { SoundsSettings } from "../sounds/SoundsSettings";
import { PushToggle } from "../push/PushToggle";
import { realEnv, signOutDevice } from "../push/push";

// TitleBar mirrors the admin's app-titlebar: brand, tabs, principal menu.
export function TitleBar({ boot }: { boot: Bootstrap }) {
  const { colorScheme, setColorScheme } = useMantineColorScheme();
  const [sounds, soundsDialog] = useDisclosure(false);
  const next = colorScheme === "dark" ? "light" : colorScheme === "light" ? "auto" : "dark";
  // Sign out on a shared device: unsubscribe it from push first, so the next
  // person here doesn't get this user's notifications (3 s at most).
  const signOut = async () => {
    let gone = false;
    const go = () => { if (!gone) { gone = true; location.href = "/app/auth/logout"; } };
    const timer = setTimeout(go, 3000);
    const env = realEnv();
    try { env.nav.clearAppBadge?.().catch(() => {}); } catch { /* unsupported */ }
    await signOutDevice(env, boot.user.id);
    clearTimeout(timer);
    go();
  };
  return (
    <Group h="100%" px="md" justify="space-between" wrap="nowrap">
      <Group gap="lg" wrap="nowrap">
        <Text fw={600}>{boot.brand}</Text>
        <Group gap="xs" component="nav" wrap="nowrap">
          {boot.tabs.map((t) => (
            <Anchor key={t.key} href={t.href} size="sm" underline="never">
              {t.label}
              {t.badge > 0 && (
                <Badge ml={6} size="sm" color={t.badgeRed ? "red" : "yellow"} aria-label={t.badgeTitle || `${t.badge}`}>{t.badge}</Badge>
              )}
            </Anchor>
          ))}
        </Group>
      </Group>
      <Group gap="sm" wrap="nowrap">
        <ActionIcon variant="subtle" aria-label={`Theme: ${colorScheme} (switch to ${next})`} onClick={() => setColorScheme(next)}>
          {colorScheme === "dark" ? "☾" : colorScheme === "light" ? "☀" : "◐"}
        </ActionIcon>
        <Menu position="bottom-end">
          <Menu.Target>
            <ActionIcon variant="subtle" radius="xl" aria-label="Account"><Avatar size="sm" radius="xl">{boot.user.initial}</Avatar></ActionIcon>
          </Menu.Target>
          <Menu.Dropdown>
            <Menu.Label>{boot.user.name} · {boot.roles}</Menu.Label>
            {boot.pushKey && <PushToggle boot={boot} />}
            <Menu.Item onClick={soundsDialog.open}>Sounds…</Menu.Item>
            <Menu.Item component="a" href="/app/user/">Classic Inbox</Menu.Item>
            {boot.signOut && <Menu.Item component="a" href="/app/auth/logout" onClick={(e) => { e.preventDefault(); void signOut(); }}>Sign out</Menu.Item>}
          </Menu.Dropdown>
        </Menu>
        <SoundsSettings opened={sounds} onClose={soundsDialog.close} />
      </Group>
    </Group>
  );
}
