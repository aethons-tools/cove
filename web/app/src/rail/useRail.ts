import { useQuery } from "@tanstack/react-query";
import { apiGet, type ApiError } from "../api/client";
import type { RailResponse } from "../api/types";

// useRail is the rail (query key ["rail"]: the live stream invalidates it from slice 2).
export const useRail = () => useQuery<RailResponse, ApiError>({ queryKey: ["rail"], queryFn: () => apiGet<RailResponse>("rail") });
