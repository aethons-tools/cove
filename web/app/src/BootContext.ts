import { createContext, useContext } from "react";
import type { Bootstrap } from "./api/types";

// BootContext carries the bootstrap to the routes (Shell provides it).
export const BootContext = createContext<Bootstrap | null>(null);

export function useBoot(): Bootstrap {
  const b = useContext(BootContext);
  if (!b) throw new Error("useBoot outside the Shell");
  return b;
}
