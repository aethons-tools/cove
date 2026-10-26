import { ActionIcon, Anchor, Avatar, Badge, Group, Menu, Text, useMantineColorScheme } from "@mantine/core";
import type { Bootstrap } from "../api/types";

// TitleBar mirrors the admin's app-titlebar: brand, tabs, principal menu.
export function TitleBar({ boot }: { boot: Bootstrap }) {
  const { colorScheme, setColorScheme } = useMantineColorScheme();
  const next = colorScheme === "dark" ? "light" : colorScheme === "light" ? "auto" : "dark";
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
            <Menu.Item component="a" href="/app/user/">Classic Inbox</Menu.Item>
            {boot.signOut && <Menu.Item component="a" href="/app/auth/logout">Sign out</Menu.Item>}
          </Menu.Dropdown>
        </Menu>
      </Group>
    </Group>
  );
}
