import { apiGet } from "./api/client";
import type { Bootstrap } from "./api/types";

// loadBootstrap reads the bootstrap webapp injected into index.html, or (the
// Vite dev server's index has none) fetches it.
export async function loadBootstrap(): Promise<Bootstrap> {
  const el = document.getElementById("jam-bootstrap");
  if (el?.textContent) return JSON.parse(el.textContent) as Bootstrap;
  return apiGet<Bootstrap>("bootstrap");
}
