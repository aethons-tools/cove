import type { Bootstrap } from "./api/types";
import { ADMIN } from "./paths";

const tab = (key: string, label: string, href: string) => ({ key, label, href, badge: 0, badgeRed: false, badgeTitle: "" });

// adminBoot is a loopback admin's bootstrap (no user: no Inbox tab).
export const adminBoot: Bootstrap = {
  brand: "Acme Jam", name: "local admin", initial: "L", roles: "admin", member: false, signOut: false,
  tabs: [tab("dashboard", "Dashboard", ADMIN), tab("config", "Config", ADMIN + "config/tracking"), tab("agents", "Agents", ADMIN + "agents"), tab("health", "Health", ADMIN + "health")],
};
