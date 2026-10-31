import { useEffect } from "react";
import { useQueryClient } from "@tanstack/react-query";

// useLiveEvents listens to /app/user/events, which carries only signals,
// never content (spec §2), and refetches what each one names; each refetch
// goes through the API's own checks. Without EventSource, or when the stream
// answers 204 (no live push), the queries' 30 s poll keeps things fresh.
export type LiveSource = (on: (event: string, data: string) => void) => () => void;

// eventSourceLive is the browser's own stream: open, changed, presence, board.
export const eventSourceLive: LiveSource = (on) => {
  const es = new EventSource("/app/user/events");
  for (const ev of ["open", "changed", "presence", "board"]) {
    es.addEventListener(ev, (e) => on(ev, (e as MessageEvent<string> | undefined)?.data ?? ""));
  }
  return () => es.close();
};

let source: LiveSource = eventSourceLive;
export function setLiveSource(s?: LiveSource): void { source = s ?? eventSourceLive; }

export function useLiveEvents(): void {
  const client = useQueryClient();
  useEffect(() => {
    if (source === eventSourceLive && typeof EventSource === "undefined") return;
    const changed = () => {
      void client.invalidateQueries({ queryKey: ["rail"] });
      void client.invalidateQueries({ queryKey: ["conversation"] });
      void client.invalidateQueries({ queryKey: ["presence"] });
      void client.invalidateQueries({ queryKey: ["session"] });
    };
    // Signals sent while the stream was down are lost, so each reconnect
    // (every open after the first) catches up.
    let opened = false;
    return source((event, data) => {
      switch (event) {
        case "changed": changed(); break;
        case "open":
          if (opened) changed();
          opened = true;
          break;
        // The open board's editor listens for these.
        case "board": window.dispatchEvent(new CustomEvent("jam-board", { detail: data })); break;
        case "presence":
          // A status change, or one to what a session's status card shows.
          void client.invalidateQueries({ queryKey: ["rail"] });
          void client.invalidateQueries({ queryKey: ["presence"] });
          // A status change can't change Artifacts (it reads the whole channel).
          void client.invalidateQueries({ queryKey: ["session"], predicate: (q) => q.queryKey[3] !== "artifacts" });
          break;
      }
    });
  }, [client]);
}
