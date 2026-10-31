import "@mantine/core/styles.css";
import "@mantine/notifications/styles.css";
import "@fontsource/ibm-plex-sans/400.css";
import "@fontsource/ibm-plex-sans/600.css";
import "@fontsource/ibm-plex-mono/400.css";
import "./pane.css";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import type { PaneName } from "../bridge/protocol";
import { Bridge, install, type VsApi } from "./bridge";
import { Pane, wirePane } from "./Pane";

// The VS Code webview's entry (vite.vscode.config.ts): every byte goes
// through the extension host. No service worker and no sounds. The
// extension's HTML names the pane (data-pane), a board tab's board
// (data-board) and Excalidraw's font directory (data-assets).
declare function acquireVsCodeApi(): VsApi;

const root = document.getElementById("root")!;
const name = root.dataset.pane as PaneName;
const board = root.dataset.board;
// Before any render, so it is in place before the editor chunk (and
// Excalidraw's font faces) load. Not boards/assetPath.ts: that is the web app's.
if (root.dataset.assets) window.EXCALIDRAW_ASSET_PATH = root.dataset.assets;
const api = acquireVsCodeApi();
// A tab's state is its board or channel, so a window reload restores the tab.
if (name === "board") api.setState({ board });
else if (root.dataset.channel) api.setState({ channel: root.dataset.channel });
const b = new Bridge(api, window);
install(b, { signIn: () => b.post({ type: "signIn" }) });
wirePane(b, document, name);
createRoot(root).render(<StrictMode><Pane bridge={b} name={name} board={board} /></StrictMode>);
b.post({ type: "ready", pane: name });
