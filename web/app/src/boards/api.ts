import { ApiError, apiAt } from "../api/client";
import type { Snap } from "./sync";

const at = (id: string) => apiAt(`/app/user/boards/${encodeURIComponent(id)}/`);
const refuse = (what: string) => (e: unknown): never => {
  throw e instanceof ApiError ? new Error(`${what} failed (${e.status})`) : e;
};

// loadElements is the board's elements Jam has past seq since.
export const loadElements = (id: string, since: number) =>
  at(id).get<{ seq: number; elements: unknown[] }>(`elements?since=${since}`).catch(refuse("load"));

// saveElements sends a batch of element snapshots; Jam answers with the
// stored winners of any that lost.
export const saveElements = (id: string, batch: Snap[]) =>
  at(id).post<{ lost: unknown[] }>("elements", { elements: batch.map((b) => JSON.parse(b.json) as unknown) }).catch(refuse("save"));
