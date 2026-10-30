import { createContext } from "react";
import type { PreviewLink } from "./api/types";

// Host is what an embedding shell (the VS Code pane) offers the shared
// components; the web app provides none.
export type Host = { signIn?: () => void; openPreview?: (session: string, v: PreviewLink, inBrowser: boolean) => void };
export const HostContext = createContext<Host>({});
