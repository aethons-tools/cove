import { ActionIcon, Anchor, Avatar, Badge, Group, Menu, Text, useMantineColorScheme } from "@mantine/core";
import { useDisclosure } from "@mantine/hooks";
import type { ReactNode } from "react";
import { Link } from "react-router";
import type { Bootstrap, NavTab } from "../api/types";
import { APP } from "../paths";
import { SoundsSettings } from "../sounds/SoundsSettings";
import { PushToggle } from "../push/PushToggle";
import { usePush } from "../push/usePush";
import { realEnv, signOutDevice } from "../push/push";

export type TitleBarProps = {
  brand: string;
  tabs: NavTab[];
  mount: string; // the area's mount: a tab under it is a client-side link
  current?: string; // the open tab's key
  name: string;
  initial: string;
  roles: string;
  menu: ReactNode; // the principal menu's items
  start?: ReactNode; // before the brand (a narrow screen's navbar toggle)
  search?: ReactNode; // before the theme toggle
};

// TitleBar is both areas' header: brand, tabs, theme toggle, principal menu.
export function TitleBar({ brand, tabs, mount, current, name, initial, roles, menu, start, search }: TitleBarProps) {
  const { colorScheme, setColorScheme } = useMantineColorScheme();
  const next = colorScheme === "dark" ? "light" : colorScheme === "light" ? "auto" : "dark";
  return (
    <Group h="100%" px="md" justify="space-between" wrap="nowrap">
      <Group gap="lg" wrap="nowrap">
        {start}
        <Text fw={600}>{brand}</Text>
        <Group gap="xs" component="nav" aria-label="Tabs" wrap="nowrap">
          {tabs.map((t) => {
            const inner = (
              <>
                {t.label}
                {t.badge > 0 && (
                  <Badge ml={6} size="sm" color={t.badgeRed ? "red" : "yellow"} aria-label={t.badgeTitle || `${t.badge}`}>{t.badge}</Badge>
                )}
              </>
            );
            const here = t.key === current ? "page" : undefined;
            return t.href.startsWith(mount)
              ? <Anchor key={t.key} component={Link} to={t.href} size="sm" underline="never" fw={here ? 600 : undefined} aria-current={here}>{inner}</Anchor>
              : <Anchor key={t.key} href={t.href} size="sm" underline="never" aria-current={here}>{inner}</Anchor>;
          })}
        </Group>
      </Group>
      <Group gap="sm" wrap="nowrap">
        {search}
        <ActionIcon variant="subtle" aria-label={`Theme: ${colorScheme} (switch to ${next})`} onClick={() => setColorScheme(next)}>
          {colorScheme === "dark" ? "☾" : colorScheme === "light" ? "☀" : "◐"}
        </ActionIcon>
        <Menu position="bottom-end">
          <Menu.Target>
            <ActionIcon variant="subtle" radius="xl" aria-label="Account"><Avatar size="sm" radius="xl">{initial}</Avatar></ActionIcon>
          </Menu.Target>
          <Menu.Dropdown>
            <Menu.Label>{name} · {roles}</Menu.Label>
            {menu}
          </Menu.Dropdown>
        </Menu>
      </Group>
    </Group>
  );
}

// UserTitleBar is the user area's: its menu has this device's Notifications
// and Sounds, and Sign out unsubscribes the device from push first.
export function UserTitleBar({ boot }: { boot: Bootstrap }) {
  const [sounds, soundsDialog] = useDisclosure(false);
  const push = usePush(boot);
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
    <>
      <TitleBar brand={boot.brand} tabs={boot.tabs} mount={APP} current="inbox" name={boot.user.name} initial={boot.user.initial} roles={boot.roles} menu={
        <>
          <PushToggle push={push} />
          <Menu.Item onClick={soundsDialog.open}>Sounds…</Menu.Item>
          {boot.signOut && <Menu.Item component="a" href="/app/auth/logout" onClick={(e) => {
            if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return; // a new tab: just the link
            e.preventDefault();
            void signOut();
          }}>Sign out</Menu.Item>}
        </>
      } />
      <SoundsSettings opened={sounds} onClose={soundsDialog.close} />
    </>
  );
}
