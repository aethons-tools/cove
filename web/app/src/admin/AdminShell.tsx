import { AppShell, Burger, Menu } from "@mantine/core";
import { useDisclosure } from "@mantine/hooks";
import { useEffect } from "react";
import { Outlet, useLocation } from "react-router";
import { BootContext } from "./BootContext";
import type { Bootstrap } from "./api/types";
import { TitleBar } from "../shell/TitleBar";
import { ConfigTree } from "./config/ConfigTree";
import { ADMIN } from "./paths";

// tabOf is the title-bar tab a page path (under ADMIN) sits under.
function tabOf(path: string): string {
  const first = path.split("/")[0] ?? "";
  return first === "" ? "dashboard" : first;
}

// AdminShell is the admin area's frame: the title bar, and on Config pages
// the Config tree as the navbar (a drawer on narrow screens). Dashboard,
// Agents and Health are full width.
export function AdminShell({ boot }: { boot: Bootstrap }) {
  const { pathname } = useLocation();
  const path = pathname.startsWith(ADMIN) ? pathname.slice(ADMIN.length) : "";
  const tab = tabOf(path);
  const tree = tab === "config";
  const [opened, drawer] = useDisclosure(false);
  const close = drawer.close;
  useEffect(() => close(), [pathname, close]); // a navigation closes the drawer
  return (
    <BootContext value={boot}>
      <AppShell header={{ height: 48 }} navbar={tree ? { width: 300, breakpoint: "sm", collapsed: { mobile: !opened } } : undefined} padding="md">
        <AppShell.Header>
          <TitleBar brand={boot.brand} tabs={boot.tabs} mount={ADMIN} current={tab} name={boot.name} initial={boot.initial} roles={boot.roles}
            start={tree ? <Burger opened={opened} onClick={drawer.toggle} hiddenFrom="sm" size="sm" aria-label="Config tree" /> : undefined}
            menu={boot.signOut ? <Menu.Item component="a" href="/app/auth/logout">Sign out</Menu.Item> : null} />
        </AppShell.Header>
        {tree && <AppShell.Navbar><ConfigTree /></AppShell.Navbar>}
        <AppShell.Main><Outlet /></AppShell.Main>
      </AppShell>
    </BootContext>
  );
}
