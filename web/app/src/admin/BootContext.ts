import { createContext, useContext } from "react";
import type { Bootstrap } from "./api/types";

// BootContext carries the admin bootstrap to the admin area's components.
export const BootContext = createContext<Bootstrap | null>(null);

export function useAdminBoot(): Bootstrap {
  const b = useContext(BootContext);
  if (!b) throw new Error("useAdminBoot outside AdminShell");
  return b;
}
