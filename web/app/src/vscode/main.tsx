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
import { interceptLinks } from "./links";
import { Pane } from "./Pane";

// The VS Code webview's entry (vite.vscode.config.ts): every byte goes
// through the extension host. No service worker and no sounds.
declare function acquireVsCodeApi(): VsApi;

const root = document.getElementById("root")!;
const b = new Bridge(acquireVsCodeApi(), window);
const signIn = () => b.post({ type: "signIn" });
install(b, { signIn });
let channel = "";
b.onState((s) => { channel = s.target && "channel" in s.target ? s.target.channel : ""; });
interceptLinks(document, () => channel, (intent) => b.post({ type: "open", intent }));
createRoot(root).render(<StrictMode><Pane bridge={b} /></StrictMode>);
b.post({ type: "ready", pane: root.dataset.pane as PaneName });
