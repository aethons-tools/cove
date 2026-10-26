import "@testing-library/jest-dom/vitest";
import { afterAll, afterEach, beforeAll } from "vitest";
import { cleanup } from "@testing-library/react";
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

beforeAll(() => server.listen({ onUnhandledFrame: "error" }));
afterEach(() => { server.resetHandlers(); cleanup(); document.getElementById("jam-bootstrap")?.remove(); });
afterAll(() => server.close());
