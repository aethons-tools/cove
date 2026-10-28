import { render } from "@testing-library/react";
import { MantineProvider } from "@mantine/core";
import { Notifications } from "@mantine/notifications";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryRouter, RouterProvider } from "react-router";
import type { Bootstrap } from "../api/types";
import { theme } from "../../theme";
import { adminRoutes } from "../AdminApp";
import { ADMIN } from "../paths";

const tab = (key: string, label: string, href: string) => ({ key, label, href, badge: 0, badgeRed: false, badgeTitle: "" });

// adminBoot is a loopback admin's bootstrap (no user: no Inbox tab).
export const adminBoot: Bootstrap = {
  brand: "Acme Jam", name: "local admin", initial: "L", roles: "admin", member: false, signOut: false,
  tabs: [tab("dashboard", "Dashboard", ADMIN), tab("config", "Config", ADMIN + "config/tracking"), tab("agents", "Agents", ADMIN + "agents"), tab("health", "Health", ADMIN + "health")],
};

export function renderAdmin(b: Bootstrap, path = ADMIN, client = new QueryClient({ defaultOptions: { queries: { retry: false } } })) {
  const router = createMemoryRouter(adminRoutes(b), { initialEntries: [path] });
  return Object.assign(render(
    <MantineProvider theme={theme} env="test">
      <Notifications />
      <QueryClientProvider client={client}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </MantineProvider>,
  ), { router });
}
