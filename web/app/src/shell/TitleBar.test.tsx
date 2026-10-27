import { describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "../test/server";
import { boot, renderApp } from "../test/render";

const turnOn = vi.fn(async () => true);
const syncOnLoad = vi.fn(async () => false);
const signOutDevice = vi.fn(async () => {});
vi.mock("../push/push", async (orig) => {
  const real = await orig<typeof import("../push/push")>();
  const env = { nav: { serviceWorker: {}, userAgent: "x", platform: "x", maxTouchPoints: 0 }, Notification: { permission: "default" }, PushManager: {}, standalone: () => false };
  return { ...real, realEnv: () => env, syncOnLoad: (...a: unknown[]) => syncOnLoad(...(a as [])), turnOn: (...a: unknown[]) => turnOn(...(a as [])), signOutDevice: (...a: unknown[]) => signOutDevice(...(a as [])) };
});

const rail = () => server.use(http.get("/app/user/api/rail", () => HttpResponse.json({ unreadTotal: 0, groups: [] })));

describe("the principal menu", () => {
  it("offers Notifications when Web Push is on, and turns it on", async () => {
    rail();
    const user = userEvent.setup();
    renderApp({ ...boot, pushKey: "BAAAAAAAAAAAAAA" });
    await user.click(await screen.findByRole("button", { name: "Account" }));
    await user.click(await screen.findByRole("menuitem", { name: "Notifications: Off" }));
    await waitFor(() => expect(turnOn).toHaveBeenCalledWith(expect.anything(), "usr_1", "BAAAAAAAAAAAAAA"));
    expect(await screen.findByRole("menuitem", { name: "Notifications: On" })).toBeInTheDocument();
  });

  it("has no Notifications item without Web Push", async () => {
    rail();
    const user = userEvent.setup();
    renderApp(boot);
    await user.click(await screen.findByRole("button", { name: "Account" }));
    await screen.findByRole("menuitem", { name: "Sounds…" });
    expect(screen.queryByRole("menuitem", { name: /Notifications/ })).toBeNull();
  });

  it("unsubscribes this device before signing out", async () => {
    rail();
    const user = userEvent.setup();
    const assign = vi.fn();
    vi.stubGlobal("location", { ...window.location, set href(v: string) { assign(v); } });
    renderApp({ ...boot, pushKey: "k" });
    await user.click(await screen.findByRole("button", { name: "Account" }));
    await user.click(await screen.findByRole("menuitem", { name: "Sign out" }));
    await waitFor(() => expect(assign).toHaveBeenCalledWith("/app/auth/logout"));
    expect(signOutDevice).toHaveBeenCalled();
    vi.unstubAllGlobals();
  });

  it("re-syncs this device's subscription at page load, without the menu being opened", async () => {
    rail();
    syncOnLoad.mockClear();
    renderApp({ ...boot, pushKey: "k" });
    await screen.findByRole("button", { name: "Account" });
    await waitFor(() => expect(syncOnLoad).toHaveBeenCalledTimes(1));
  });
});
