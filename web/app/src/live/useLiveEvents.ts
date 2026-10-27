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
    const changed = () => {
      void client.invalidateQueries({ queryKey: ["rail"] });
      void client.invalidateQueries({ queryKey: ["conversation"] });
      void client.invalidateQueries({ queryKey: ["presence"] });
      void client.invalidateQueries({ queryKey: ["session"] });
    };
    es.addEventListener("changed", changed);
    // EventSource reconnects by itself; signals sent while it was down are
    // lost, so each reconnect (every open after the first) catches up.
    let opened = false;
    es.addEventListener("open", () => {
      if (opened) changed();
      opened = true;
    });
    // The open board's editor listens for these.
    es.addEventListener("board", (e) => window.dispatchEvent(new CustomEvent("jam-board", { detail: (e as MessageEvent<string>).data })));
    es.addEventListener("presence", () => {
      void client.invalidateQueries({ queryKey: ["presence"] });
      // A status change can't change Artifacts (it reads the whole channel).
      void client.invalidateQueries({ queryKey: ["session"], predicate: (q) => q.queryKey[3] !== "artifacts" });
    });
    return () => es.close();
  }, [client]);
}
