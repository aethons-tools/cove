import type { Plugin } from "vite";

// MockApi is the mock API's record of the requests it had no mock for, as
// "METHOD /path?query", so a screenshot of a page missing one fails
// (scripts/shot.mjs) instead of shooting the page's error state.
export interface MockApi {
  misses: string[];
}

// EventStream is a mock answered as Server-Sent Events, in order.
export interface EventStream {
  eventStream: { event: string; data: string }[];
}

const isStream = (v: unknown): v is EventStream =>
  typeof v === "object" && v !== null && Array.isArray((v as EventStream).eventStream);

// mockApi answers the app's API routes (/app/user/api/, /app/admin/api/) and
// its event streams (/app/user/session/) from a module's `mocks`
// (src/shots/mocks.ts: "METHOD /path[?query]" → JSON), loaded through Vite so
// it reuses the tests' TypeScript fixtures. A mock that is an EventStream
// answers as Server-Sent Events: its events, then the end of the response
// (with a day's retry, so the browser doesn't reconnect during a shot).
// Anything else under those routes answers the API's 404, is logged and is
// recorded in the plugin's api.misses.
export function mockApi(module: string): Plugin & { api: MockApi } {
  const api: MockApi = { misses: [] };
  return {
    name: "jam-mock-api",
    api,
    configureServer(server) {
      server.middlewares.use(async (req, res, next) => {
        const url = new URL(req.url ?? "/", "http://localhost");
        if (!/^\/app\/((user|admin)\/api|user\/session)\//.test(url.pathname)) return next();
        const { mocks } = (await server.ssrLoadModule(module)) as { mocks: Record<string, unknown> };
        const key = `${req.method} ${url.pathname}`;
        const body = mocks[key + url.search] ?? mocks[key];
        if (isStream(body)) {
          res.setHeader("Content-Type", "text/event-stream");
          res.end("retry: 86400000\n\n" + body.eventStream.map((e) => `event: ${e.event}\ndata: ${e.data}\n\n`).join(""));
          return;
        }
        res.setHeader("Content-Type", "application/json");
        if (body === undefined) {
          server.config.logger.warn(`mock-api: no mock for ${key}${url.search}`);
          api.misses.push(key + url.search);
          res.statusCode = 404;
          res.end(JSON.stringify({ error: "no mock", code: "not_found" }));
          return;
        }
        res.end(JSON.stringify(body));
      });
    },
  };
}
