import { StrictMode } from "react";
import { registerSW } from "./push/sw";
import { createRoot } from "react-dom/client";
import { MantineProvider } from "@mantine/core";
import { Notifications } from "@mantine/notifications";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createBrowserRouter, RouterProvider } from "react-router";
import { theme } from "./theme";
import { routes } from "./App";
import { loadBootstrap } from "./bootstrap";
import { ApiError, shouldRetry } from "./api/client";
import { Problem } from "./Problem";
import { initSounds } from "./sounds/sounds";

// start renders the app into el once the bootstrap is in hand.
export async function start(el: HTMLElement): Promise<void> {
  const root = createRoot(el);
  initSounds();
  registerSW();
  let boot;
  try {
    boot = await loadBootstrap();
  } catch (e: unknown) {
    // Only the dev server's index lacks the injected bootstrap: show why its
    // fetch failed (Jam not running, not signed in) rather than guess.
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
          <RouterProvider router={createBrowserRouter(routes(boot))} />
        </QueryClientProvider>
      </MantineProvider>
    </StrictMode>,
  );
}
