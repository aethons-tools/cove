import type { DraftEditor as Editor, DraftView } from "../api/types";
import type { DraftWrite } from "./useDraft";

// DraftEditor is a draft file, opened (Task 3).
// eslint-disable-next-line @typescript-eslint/no-unused-vars -- filled in by Task 3
export function DraftEditor(_: { kit: string; d: DraftView; e: Editor; fileHref: (path: string, diff?: boolean) => string; run: (w: DraftWrite) => Promise<unknown> }) {
  return null;
}
