// shot.mjs — screenshot one view of the Jam app on mock data, for the manual
// (`just manual-shot`; docs/manual.md). It serves the app with Vite on
// vite.shot.config.ts (the API answered from src/shots/mocks.ts), drives the
// kit's headless Chrome over the DevTools protocol, and writes a PNG.
//
//   node scripts/shot.mjs <route> --out <file.png> [--selector <css>]
//     [--pad 8] [--width 1280] [--height 800] [--scale 2]
//
// route is an app path ("/app/admin/config/projects/acme", or without the
// leading /app/). The browser is $JAM_BROWSER, else
// /opt/chrome-headless-shell/chrome-headless-shell, else the first of the
// usual names on PATH. It resolves no host but loopback, so a shot never
// reaches the network.
import { spawn } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { createServer as netServer } from "node:net";
import { tmpdir } from "node:os";
import { delimiter, dirname, join, resolve } from "node:path";
import { parseArgs } from "node:util";
import { createServer } from "vite";

const { values: opt, positionals } = parseArgs({
  allowPositionals: true,
  options: {
    out: { type: "string" },
    selector: { type: "string" },
    pad: { type: "string", default: "8" },
    width: { type: "string", default: "1280" },
    height: { type: "string", default: "800" },
    scale: { type: "string", default: "2" },
  },
});
if (positionals.length !== 1 || !opt.out) {
  console.error("usage: shot.mjs <route> --out <file.png> [--selector <css>] [--pad 8] [--width 1280] [--height 800] [--scale 2]");
  process.exit(2);
}
const route = positionals[0].startsWith("/") ? positionals[0] : "/app/" + positionals[0];
const out = resolve(process.env.SHOT_CWD ?? process.cwd(), opt.out);
const width = Number(opt.width), height = Number(opt.height), scale = Number(opt.scale), pad = Number(opt.pad);

function browserPath() {
  if (process.env.JAM_BROWSER) return process.env.JAM_BROWSER;
  const kit = "/opt/chrome-headless-shell/chrome-headless-shell";
  if (existsSync(kit)) return kit;
  for (const name of ["chrome-headless-shell", "headless-shell", "chromium", "google-chrome"]) {
    for (const dir of (process.env.PATH ?? "").split(delimiter)) {
      if (dir && existsSync(join(dir, name))) return join(dir, name);
    }
  }
  throw new Error("no headless browser found (set JAM_BROWSER or install chrome-headless-shell)");
}

function freePort() {
  return new Promise((ok, fail) => {
    const s = netServer().listen(0, "127.0.0.1", () => {
      const { port } = s.address();
      s.close(() => ok(port));
    }).on("error", fail);
  });
}

// launch starts Chrome and answers its browser-level DevTools WebSocket URL.
function launch(profile) {
  const chrome = spawn(browserPath(), [
    "--headless", "--no-sandbox", "--disable-gpu", "--hide-scrollbars", "--mute-audio",
    "--no-first-run", "--disable-extensions", "--remote-debugging-port=0", `--user-data-dir=${profile}`,
    // Deterministic pixels: no hinting differences, one colour profile.
    "--font-render-hinting=none", "--force-color-profile=srgb",
    // Loopback only, never the proxy.
    "--no-proxy-server", "--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE 127.0.0.1",
    "about:blank",
  ], { stdio: ["ignore", "ignore", "pipe"] });
  return new Promise((ok, fail) => {
    let err = "";
    chrome.stderr.on("data", (d) => {
      err += d;
      const m = /DevTools listening on (ws:\/\/\S+)/.exec(err);
      if (m) ok({ chrome, ws: m[1] });
    });
    chrome.on("exit", (code) => fail(new Error(`chrome exited (${code}): ${err}`)));
  });
}

// cdp is a minimal DevTools-protocol client: send(method, params, session)
// and on(event, fn).
async function cdp(url) {
  const ws = new WebSocket(url);
  await new Promise((ok, fail) => { ws.onopen = ok; ws.onerror = () => fail(new Error("DevTools connection failed")); });
  let id = 0;
  const pending = new Map(), handlers = new Map();
  ws.onmessage = (e) => {
    const m = JSON.parse(e.data);
    if (m.id !== undefined) {
      const p = pending.get(m.id);
      pending.delete(m.id);
      if (m.error) p.fail(new Error(`${p.method}: ${m.error.message}`));
      else p.ok(m.result);
    } else {
      for (const fn of handlers.get(m.method) ?? []) fn(m.params);
    }
  };
  return {
    send: (method, params = {}, sessionId) => new Promise((ok, fail) => {
      pending.set(++id, { ok, fail, method });
      ws.send(JSON.stringify({ id, method, params, sessionId }));
    }),
    on: (method, fn) => handlers.set(method, [...(handlers.get(method) ?? []), fn]),
    close: () => ws.close(),
  };
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// No transitions, animations or blinking caret: every shot of a view is the same.
const still = `document.addEventListener("DOMContentLoaded", () => {
  const s = document.createElement("style");
  s.textContent = "*, *::before, *::after { transition: none !important; animation: none !important; caret-color: transparent !important; }";
  document.head.append(s);
});`;

async function shoot(base, ws) {
  const c = await cdp(ws);
  try {
    const { targetId } = await c.send("Target.createTarget", { url: "about:blank" });
    const { sessionId: s } = await c.send("Target.attachToTarget", { targetId, flatten: true });
    const send = (m, p) => c.send(m, p, s);
    let inflight = 0, quietSince = Date.now();
    c.on("Network.requestWillBeSent", () => { inflight++; });
    const done = () => { inflight = Math.max(0, inflight - 1); quietSince = Date.now(); };
    c.on("Network.loadingFinished", done);
    c.on("Network.loadingFailed", done);
    await send("Network.enable");
    await send("Page.enable");
    await send("Emulation.setDeviceMetricsOverride", { width, height, deviceScaleFactor: scale, mobile: false });
    await send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-color-scheme", value: "light" }, { name: "prefers-reduced-motion", value: "reduce" }] });
    await send("Page.addScriptToEvaluateOnNewDocument", { source: still });
    await send("Page.navigate", { url: base + route });

    const evaluate = async (expression) => {
      const r = await send("Runtime.evaluate", { expression, awaitPromise: true, returnByValue: true });
      if (r.exceptionDetails) throw new Error(r.exceptionDetails.exception?.description ?? r.exceptionDetails.text);
      return r.result.value;
    };
    const sel = JSON.stringify(opt.selector ?? "#root > *");
    // Ready: the element is there, the network has been quiet for 500 ms
    // (the mock API answered), and the fonts are loaded.
    const deadline = Date.now() + 30_000;
    for (;;) {
      if (Date.now() > deadline) throw new Error(`timed out waiting for ${sel} on ${route}`);
      const found = await evaluate(`document.readyState === "complete" && !!document.querySelector(${sel})`);
      if (found && inflight === 0 && Date.now() - quietSince > 500) break;
      await sleep(100);
    }
    await evaluate("document.fonts.ready.then(() => true)");
    // Grow the viewport to the whole page, so nothing scrolls or overlaps.
    const full = await evaluate("document.documentElement.scrollHeight");
    if (full > height) {
      await send("Emulation.setDeviceMetricsOverride", { width, height: full, deviceScaleFactor: scale, mobile: false });
    }
    await evaluate("new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(() => r(true))))");
    const clip = await evaluate(opt.selector
      // The element and pad CSS pixels around it, within the page.
      ? `(() => {
          const r = document.querySelector(${sel}).getBoundingClientRect(), d = document.documentElement;
          const x = Math.max(0, r.left + scrollX - ${pad}), y = Math.max(0, r.top + scrollY - ${pad});
          const right = Math.min(d.scrollWidth, r.right + scrollX + ${pad}), bottom = Math.min(d.scrollHeight, r.bottom + scrollY + ${pad});
          return { x, y, width: right - x, height: bottom - y, scale: 1 };
        })()`
      : `({ x: 0, y: 0, width: ${width}, height: document.documentElement.scrollHeight, scale: 1 })`);
    const { data } = await send("Page.captureScreenshot", { format: "png", clip, captureBeyondViewport: true });
    mkdirSync(dirname(out), { recursive: true });
    writeFileSync(out, Buffer.from(data, "base64"));
    console.log(`wrote ${out} (${Math.round(clip.width * scale)}×${Math.round(clip.height * scale)})`);
  } finally {
    c.close();
  }
}

const port = await freePort();
const server = await createServer({ configFile: "vite.shot.config.ts", logLevel: "warn", server: { host: "127.0.0.1", port, strictPort: true } });
await server.listen();
const profile = mkdtempSync(join(tmpdir(), "jam-shot-"));
let chrome;
try {
  const b = await launch(profile);
  chrome = b.chrome;
  await shoot(`http://127.0.0.1:${port}`, b.ws);
} finally {
  chrome?.kill();
  await server.close();
  rmSync(profile, { recursive: true, force: true });
}
