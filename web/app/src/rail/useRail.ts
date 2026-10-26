import { useQuery } from "@tanstack/react-query";
import { apiGet, POLL_MS, type ApiError } from "../api/client";
import type { RailResponse } from "../api/types";

// useRail is the rail, counting the open conversation (selected) as read.
// Keys start with "rail", so a `changed` event refetches every one.
export const useRail = (selected?: string) => useQuery<RailResponse, ApiError>({
  queryKey: ["rail", selected ?? ""],
  queryFn: () => apiGet<RailResponse>(selected ? `rail?c=${encodeURIComponent(selected)}` : "rail"),
  refetchInterval: POLL_MS,
});
