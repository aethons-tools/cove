import { describe, expect, it } from "vitest";
import { excalidrawFonts } from "./excalidrawFonts";

const ID = "/x/node_modules/@excalidraw/excalidraw/dist/prod/chunk-1.js";
const SRC = 'P(jn,"ASSETS_FALLBACK_URL",`https://esm.sh/${M.PKG_NAME?`${M.PKG_NAME}@${M.PKG_VERSION}`:"@excalidraw/excalidraw"}/dist/prod/`);var Xo=jn;';

// run feeds the plugin's hooks as a build would: transform each module,
// then check the emitted chunks.
function run(modules: [string, string][], chunks: Record<string, string>) {
  const p = excalidrawFonts();
  const t = p.transform as (code: string, id: string) => { code: string } | null;
  const out = modules.map(([id, code]) => t.call({}, code, id)?.code ?? code);
  const bundle = Object.fromEntries(Object.entries(chunks).map(([k, code]) => [k, { type: "chunk", code }]));
  (p.generateBundle as (o: unknown, b: unknown) => void).call({ error: (m: string) => { throw new Error(m); } }, {}, bundle);
  return out;
}

describe("the Excalidraw font plugin", () => {
  it("points the CDN fallback at the app's own bundle", () => {
    const [out] = run([[ID, SRC]], { "BoardEditor.js": "fine" });
    expect(out).toContain("`${location.origin}/app/next/assets/excalidraw/`");
    expect(out).not.toContain("esm.sh");
  });

  it("fails the build if Excalidraw's fallback changed shape", () => {
    expect(() => run([[ID, 'P(jn,"ASSETS_FALLBACK_URL",`https://cdn.example/x/`)']], {})).toThrow(/changed shape/);
  });

  it("fails the build if nothing was rewritten while Excalidraw is in it (a rename)", () => {
    expect(() => run([[ID, 'Q(jn,"FALLBACK",`https://esm.sh/x/`)']], {})).toThrow(/rewrote 0/);
  });

  it("fails the build if any chunk still names esm.sh", () => {
    expect(() => run([[ID, SRC]], { "x.js": "fetch('https://esm.sh/thing')" })).toThrow(/esm\.sh/);
  });

  it("leaves a build without Excalidraw alone", () => {
    expect(run([["/x/src/main.ts", "console.log(1)"]], { "index.js": "x" })).toEqual(["console.log(1)"]);
  });
});
