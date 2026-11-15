import { notifications } from "@mantine/notifications";
import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiGet, apiPut, POLL_MS, type ApiError } from "../api/client";
import type { RailOrderRequest, RailResponse } from "../api/types";
import { ordered, type RailSection } from "./order";

// useRail is the rail, counting the open conversation (selected) as read.
// Keys start with "rail", so a `changed` event refetches every one.
export const useRail = (selected?: string) => useQuery<RailResponse, ApiError>({
  queryKey: ["rail", selected ?? ""],
  queryFn: () => apiGet<RailResponse>(selected ? `rail?c=${encodeURIComponent(selected)}` : "rail"),
  refetchInterval: POLL_MS,
  // A new selection is a new key: keep showing the last rail meanwhile, so
  // the rail neither blinks to a spinner nor loses its scroll on a switch.
  placeholderData: keepPreviousData,
});

// reordered is rail with a section in the order of ids.
export function reordered(rail: RailResponse, section: RailSection, ids: string[]): RailResponse {
  return section === "me"
    ? { ...rail, me: ordered(rail.me, (r) => r.id, ids) }
    : { ...rail, agents: ordered(rail.agents, (a) => a.session, ids) };
}

// useRailOrder saves the viewer's order of a section (PUT api/rail/order).
// Every cached rail takes the order at once (an older rail mustn't land on
// it); the rails refetch when the save settles, so a refused one says so and
// goes back to the saved order.
export function useRailOrder() {
  const client = useQueryClient();
  return useMutation<void, ApiError, { section: RailSection; ids: string[] }>({
    mutationFn: (o) => apiPut("rail/order", o satisfies RailOrderRequest),
    onMutate: async ({ section, ids }) => {
      await client.cancelQueries({ queryKey: ["rail"] });
      client.setQueriesData<RailResponse>({ queryKey: ["rail"] }, (r) => (r ? reordered(r, section, ids) : r));
    },
    onError: (e) => notifications.show({ color: "red", message: `Couldn't save the order: ${e.message}` }),
    onSettled: () => client.invalidateQueries({ queryKey: ["rail"] }),
  });
}
