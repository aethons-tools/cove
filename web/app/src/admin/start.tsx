import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { MantineProvider } from "@mantine/core";
import { Notifications } from "@mantine/notifications";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createBrowserRouter, RouterProvider } from "react-router";
import { registerSW } from "../push/sw";
import { theme } from "../theme";
import { ApiError, shouldRetry } from "../api/client";
import { Problem } from "../Problem";
import type { Bootstrap } from "./api/types";
import { adminRoutes } from "./AdminApp";
import { adminApi } from "./paths";

// loadBootstrap reads the bootstrap webapp injected into index.html, or (the
// Vite dev server's index has none) fetches it.
async function loadBootstrap(): Promise<Bootstrap> {
  const el = document.getElementById("jam-bootstrap");
  if (el?.textContent) return JSON.parse(el.textContent) as Bootstrap;
  return adminApi.get<Bootstrap>("bootstrap");
}

// start renders the admin area into el once its bootstrap is in hand (the
// user area's start, without its member-only sounds).
export async function start(el: HTMLElement): Promise<void> {
  const root = createRoot(el);
  registerSW();
  let boot;
  try {
    boot = await loadBootstrap();
  } catch (e: unknown) {
    root.render(
      <MantineProvider theme={theme} defaultColorScheme="auto">
        <Problem error={e instanceof ApiError ? e : new ApiError(0, "unavailable", String(e))} here={location.pathname + location.search} />
      </MantineProvider>,
    );
    return;
  }
  const client = new QueryClient({ defaultOptions: { queries: { staleTime: 10_000, retry: shouldRetry } } });
  root.render(
    <StrictMode>
      <MantineProvider theme={theme} defaultColorScheme="auto">
        <Notifications />
        <QueryClientProvider client={client}>
          <RouterProvider router={createBrowserRouter(adminRoutes(boot))} />
        </QueryClientProvider>
      </MantineProvider>
    </StrictMode>,
  );
}
