import { render } from "@testing-library/react";
import { MantineProvider } from "@mantine/core";
import { Notifications } from "@mantine/notifications";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryRouter, RouterProvider } from "react-router";
import type { Bootstrap } from "../api/types";
import { theme } from "../../theme";
import { adminRoutes } from "../AdminApp";
import { ADMIN } from "../paths";

export { adminBoot } from "../fixtures";

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
