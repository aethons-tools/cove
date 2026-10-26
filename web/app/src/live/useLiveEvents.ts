import { useEffect } from "react";
import { useQueryClient } from "@tanstack/react-query";

// useLiveEvents listens to /app/user/events, which carries only signals,
// never content (spec §2), and refetches what each one names; each refetch
// goes through the API's own checks. Without EventSource, or when the stream
// answers 204 (no live push), the queries' 30 s poll keeps things fresh.
export function useLiveEvents(): void {
  const client = useQueryClient();
  useEffect(() => {
    if (typeof EventSource === "undefined") return;
    const es = new EventSource("/app/user/events");
    es.addEventListener("changed", () => {
      void client.invalidateQueries({ queryKey: ["rail"] });
      void client.invalidateQueries({ queryKey: ["conversation"] });
      void client.invalidateQueries({ queryKey: ["presence"] });
    });
    es.addEventListener("presence", () => void client.invalidateQueries({ queryKey: ["presence"] }));
    return () => es.close();
  }, [client]);
}
