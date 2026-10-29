import type { BridgeRequest, BridgeResponse, ExtToPane, PaneState, PaneToExt } from "../bridge/protocol";
import type { VsApi } from "./bridge";

// A Route answers one request: a response, or a function of the request.
export type Route = BridgeResponse | ((req: BridgeRequest) => BridgeResponse | Promise<BridgeResponse>);

export const json = (body: unknown, status = 200): BridgeResponse => ({ kind: "http", status, contentType: "application/json", json: body });

// fakeHost is the test stand-in for the extension host: acquireVsCodeApi()
// is its VsApi, win the window the pane listens on (a bare EventTarget, so
// no test leaves listeners on the real window), and routes (keyed
// "METHOD path", the path without its query) answer requests. Unrouted
// requests are a JSON 404.
export function fakeHost(routes: Record<string, Route> = {}) {
  const posted: PaneToExt[] = [];
  const requests: BridgeRequest[] = [];
  const win = new EventTarget() as unknown as Window;
  const send = (m: ExtToPane) => win.dispatchEvent(new MessageEvent("message", { data: m }));
  const answer = async (id: number, req: BridgeRequest) => {
    const r = routes[`${req.method} ${req.path.split("?")[0]}`];
    const res = r === undefined ? json({ error: "no route", code: "not_found" }, 404) : typeof r === "function" ? await r(req) : r;
    send({ type: "response", id, res });
  };
  const api: VsApi = {
    postMessage(m) {
      posted.push(m);
      if (m.type === "request") {
        requests.push(m.req);
        setTimeout(() => void answer(m.id, m.req), 0);
      }
    },
  };
  return {
    acquireVsCodeApi: () => api,
    win,
    posted,
    requests,
    routes,
    send,
    state: (state: PaneState) => send({ type: "state", state }),
    live: (event: "changed" | "presence" | "board" | "open", data = "") => send({ type: "live", event, data }),
  };
}
export type FakeHost = ReturnType<typeof fakeHost>;
