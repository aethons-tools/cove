import { defineConfig, mergeConfig } from "vite";
import base from "./vite.config.ts";
import { mockApi } from "./build/mockApi.ts";

// The app on mock data, for manual screenshots (`just manual-shot`,
// scripts/shot.mjs): the dev server answers the API from src/shots/mocks.ts
// instead of proxying it to a Jam.
const config = mergeConfig(base, defineConfig({ plugins: [mockApi("/src/shots/mocks.ts")] }));
config.server.proxy = undefined;
export default config;
