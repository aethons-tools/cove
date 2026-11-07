import type { Plugin } from "vite";

// mockApi answers the app's API routes (/app/user/api/, /app/admin/api/) from
// a module's `mocks` (src/shots/mocks.ts: "METHOD /path[?query]" → JSON),
// loaded through Vite so it reuses the tests' TypeScript fixtures. Anything
// else under those routes answers the API's 404 and is logged.
export function mockApi(module: string): Plugin {
  return {
    name: "jam-mock-api",
    configureServer(server) {
      server.middlewares.use(async (req, res, next) => {
        const url = new URL(req.url ?? "/", "http://localhost");
        if (!/^\/app\/(user|admin)\/api\//.test(url.pathname)) return next();
        const { mocks } = (await server.ssrLoadModule(module)) as { mocks: Record<string, unknown> };
        const key = `${req.method} ${url.pathname}`;
        const body = mocks[key + url.search] ?? mocks[key];
        res.setHeader("Content-Type", "application/json");
        if (body === undefined) {
          server.config.logger.warn(`mock-api: no mock for ${key}${url.search}`);
          res.statusCode = 404;
          res.end(JSON.stringify({ error: "no mock", code: "not_found" }));
          return;
        }
        res.end(JSON.stringify(body));
      });
    },
  };
}
