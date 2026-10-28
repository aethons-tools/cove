import type { DraftView, Published } from "../api/types";
import type { DraftWrite } from "./useDraft";

// DraftPublish publishes the draft (Task 4).
// eslint-disable-next-line @typescript-eslint/no-unused-vars -- filled in by Task 4
export function DraftPublish(_: { kit: string; d: DraftView; run: (w: DraftWrite) => Promise<unknown> }) {
  return null;
}

// PublishedCard is a just-published draft (Task 4).
// eslint-disable-next-line @typescript-eslint/no-unused-vars -- filled in by Task 4
export function PublishedCard(_: { kit: string; p: Published }) {
  return null;
}
