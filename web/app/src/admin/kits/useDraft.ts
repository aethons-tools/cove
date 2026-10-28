import { notifications } from "@mantine/notifications";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import type { ApiError } from "../../api/client";
import type { DraftPage, DraftResult } from "../api/types";
import { adminApi } from "../paths";
import { useAdminMutation } from "../ui/useAdminMutation";
import { kitsKey } from "./KitsPage";

const enc = encodeURIComponent;
export const draftKey = (kit: string) => ["admin", "draft", kit];

// useDraft is a kit's Draft section showing file (in the editor, or with
// diff its diff against the base).
export function useDraft(kit: string, file: string, diff: boolean) {
  const q = new URLSearchParams();
  if (file) q.set("file", file);
  if (diff) q.set("diff", "1");
  const qs = q.toString();
  return useQuery<DraftPage, ApiError>({
    queryKey: [...draftKey(kit), file, diff], queryFn: () => adminApi.get(`kits/${enc(kit)}/draft${qs ? `?${qs}` : ""}`),
  });
}

export type DraftWrite = { path: string; body?: unknown; form?: FormData; open?: string };

// useDraftWrite runs a draft write (JSON, or an upload's FormData). Its
// answer is the section after it, opened on `open` (the file it wrote) —
// the caller moves the page there; the kit page and list refresh (a draft
// chip, a publish).
export function useDraftWrite(kit: string, onDone: (r: DraftResult, w: DraftWrite) => void) {
  const client = useQueryClient();
  return useAdminMutation((w: DraftWrite) => {
    const url = `kits/${enc(kit)}/draft${w.path ? `/${w.path}` : ""}`;
    return w.form ? adminApi.postForm<DraftResult>(url, w.form) : adminApi.post<DraftResult>(url, w.body ?? {});
  }, {
    invalidate: [kitsKey, ["admin", "kit", kit]],
    onSuccess: (r, w) => {
      // Every other view of the draft is stale now; this one is the answer.
      void client.invalidateQueries({ queryKey: draftKey(kit), refetchType: "none" });
      client.setQueryData([...draftKey(kit), w.open ?? "", false], r.page);
      if (r.message) notifications.show({ color: "green", message: r.message });
      onDone(r, w);
    },
  });
}
