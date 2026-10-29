import { createContext } from "react";

// Host is what an embedding shell (the VS Code pane) offers the shared
// components; the web app provides none.
export type Host = { signIn?: () => void };
export const HostContext = createContext<Host>({});
