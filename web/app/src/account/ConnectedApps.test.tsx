import { describe, expect, it } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MantineProvider } from "@mantine/core";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { http, HttpResponse } from "msw";
import { server } from "../test/server";
import { theme } from "../theme";
import type { AppTokenRow } from "../api/types";
import { ConnectedApps } from "./ConnectedApps";

const now = Date.now();
const iso = (ms: number) => new Date(now + ms).toISOString();
const rows: AppTokenRow[] = [
  { id: "apt_1", label: "VS Code on mac", created: iso(-3_600_000), lastUsed: iso(-120_000), expires: iso(29 * 86_400_000), operator: true },
  { id: "apt_2", label: "VS Code on linux", created: iso(-86_400_000), lastUsed: iso(-86_400_000), expires: iso(20 * 86_400_000), operator: false },
];

function show() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <MantineProvider theme={theme} env="test">
      <QueryClientProvider client={client}>
        <ConnectedApps opened onClose={() => {}} />
      </QueryClientProvider>
    </MantineProvider>,
  );
}

describe("Connected apps", () => {
  it("ConnectedApps lists tokens with operator badge", async () => {
    server.use(http.get("/app/user/api/app-tokens", () => HttpResponse.json({ tokens: rows })));
    show();
    const dialog = await screen.findByRole("dialog", { name: "Connected apps" });
    const mac = await within(dialog).findByText("VS Code on mac");
    const row = mac.closest("[data-token]") as HTMLElement;
    expect(within(row).getByText("operator rights")).toBeInTheDocument();
    expect(within(row).getByText(/Last used 2 minutes ago/)).toBeInTheDocument();
    expect(within(row).getByText(/Expires /)).toBeInTheDocument();
    const linux = within(dialog).getByText("VS Code on linux").closest("[data-token]") as HTMLElement;
    expect(within(linux).queryByText("operator rights")).toBeNull();
  });

  it("Revoke deletes and refreshes", async () => {
    let tokens = rows;
    const deleted: string[] = [];
    server.use(
      http.get("/app/user/api/app-tokens", () => HttpResponse.json({ tokens })),
      http.delete("/app/user/api/app-tokens/:id", ({ params }) => {
        deleted.push(String(params.id));
        tokens = tokens.filter((t) => t.id !== params.id);
        return new HttpResponse(null, { status: 204 });
      }),
    );
    const user = userEvent.setup();
    show();
    const mac = (await screen.findByText("VS Code on mac")).closest("[data-token]") as HTMLElement;
    await user.click(within(mac).getByRole("button", { name: "Revoke" }));
    await waitFor(() => expect(screen.queryByText("VS Code on mac")).toBeNull());
    expect(deleted).toEqual(["apt_1"]);
    expect(screen.getByText("VS Code on linux")).toBeInTheDocument();
  });

  it("shows the empty state", async () => {
    server.use(http.get("/app/user/api/app-tokens", () => HttpResponse.json({ tokens: [] })));
    show();
    expect(await screen.findByText("No apps are connected. Sign in from VS Code with Jam: Sign In.")).toBeInTheDocument();
  });
});
