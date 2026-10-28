import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { ApiError } from "../../api/client";

// useAdminMutation is an admin write: on success it refreshes the Config tree
// (names and badges may have changed) and the listed queries.
export function useAdminMutation<V, R>(fn: (v: V) => Promise<R>, opts: { invalidate?: unknown[][]; onSuccess?: (r: R, v: V) => void; onError?: (e: ApiError) => void; mutationKey?: unknown[] } = {}) {
  const client = useQueryClient();
  return useMutation<R, ApiError, V>({
    mutationKey: opts.mutationKey,
    mutationFn: fn,
    onSuccess: (r, v) => {
      for (const key of [["admin", "tree"], ...(opts.invalidate ?? [])]) void client.invalidateQueries({ queryKey: key });
      opts.onSuccess?.(r, v);
    },
    onError: opts.onError,
  });
}
