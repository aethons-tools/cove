import { render } from "@testing-library/react";
import { MantineProvider } from "@mantine/core";
import { Notifications } from "@mantine/notifications";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryRouter, RouterProvider } from "react-router";
import type { Bootstrap } from "../api/types";
import { theme } from "../theme";
import { routes } from "../App";

export const boot: Bootstrap = {
  brand: "Acme Jam", user: { id: "usr_1", name: "alice", initial: "A" }, roles: "member", admin: false, signOut: true,
  tabs: [{ key: "inbox", label: "Inbox", href: "/app/user/", badge: 0, badgeRed: false, badgeTitle: "" }],
  attachMax: 0, pushKey: "", boards: false, pushFocus: false,
};

export function renderApp(b: Bootstrap, path = "/app/user/", client = new QueryClient({ defaultOptions: { queries: { retry: false } } })) {
  const router = createMemoryRouter(routes(b), { initialEntries: [path] });
  return render(
    <MantineProvider theme={theme} env="test">
      <Notifications />
      <QueryClientProvider client={client}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </MantineProvider>,
  );
}
