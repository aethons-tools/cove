import type { SessionCard } from "../api/types";

// card is a status card for tests: a working session with nothing counted.
export const card = (over: Partial<SessionCard> = {}): SessionCard => ({
  session: "ses_1", name: "spider", state: "working", pill: "working", tool: "Bash", mission: "", focus: "", inits: 0,
  counts: { asks: 0, alarms: 0, previews: 0, processes: 0 }, context: { used: 0, window: 0 }, ...over,
});
