import "@mantine/core/styles.css";
import "@fontsource/ibm-plex-sans/400.css";
import "@fontsource/ibm-plex-sans/600.css";
import "@fontsource/ibm-plex-mono/400.css";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { MantineProvider } from "@mantine/core";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createBrowserRouter, RouterProvider } from "react-router";
import { theme } from "./theme";
import { routes } from "./App";
import { loadBootstrap } from "./bootstrap";

const client = new QueryClient({ defaultOptions: { queries: { staleTime: 10_000 } } });
const root = createRoot(document.getElementById("root")!);

loadBootstrap().then(
  (boot) => root.render(
    <StrictMode>
      <MantineProvider theme={theme} defaultColorScheme="auto">
        <QueryClientProvider client={client}>
          <RouterProvider router={createBrowserRouter(routes(boot))} />
        </QueryClientProvider>
      </MantineProvider>
    </StrictMode>,
  ),
  () => { location.href = "/app/auth/login"; },
);
