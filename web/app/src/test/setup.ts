import "@testing-library/jest-dom/vitest";
import { afterAll, afterEach, beforeAll } from "vitest";
import { cleanup, configure } from "@testing-library/react";
import { notifications } from "@mantine/notifications";
import { server } from "./server";

// Mantine reads these; jsdom has neither.
window.matchMedia ??= ((query: string) => ({
  matches: false, media: query, onchange: null,
  addListener: () => {}, removeListener: () => {},
  addEventListener: () => {}, removeEventListener: () => {}, dispatchEvent: () => false,
})) as typeof window.matchMedia;
globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} };
// Mantine's autosizing Textarea listens for font loads; jsdom has no FontFaceSet.
if (!("fonts" in document)) {
  Object.defineProperty(document, "fonts", { value: { addEventListener: () => {}, removeEventListener: () => {} } });
}

// findBy*/waitFor wait up to 5 s: a file's first render (cold imports) can
// pass 1 s while the whole suite runs in parallel.
configure({ asyncUtilTimeout: 5000 });

beforeAll(() => server.listen({ onUnhandledFrame: "error" }));
// Notifications live in a global store: a persistent one (a refusal) would
// otherwise carry into the next test.
afterEach(() => { server.resetHandlers(); cleanup(); notifications.clean(); document.getElementById("jam-bootstrap")?.remove(); });
afterAll(() => server.close());
