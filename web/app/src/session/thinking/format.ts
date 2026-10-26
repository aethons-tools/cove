// fmtDuration renders a duration: 850ms, 2.1s, 1m 4s (the old meThFmt).
export function fmtDuration(ms: number): string {
  const v = Math.max(0, Math.round(ms) || 0);
  if (v < 1000) return `${v}ms`;
  if (v < 60000) return `${(v / 1000).toFixed(1).replace(/\.0$/, "")}s`;
  return `${Math.floor(v / 60000)}m ${Math.floor((v % 60000) / 1000)}s`;
}
export const usd = (v: number) => "$" + v.toLocaleString("en-US", { minimumFractionDigits: 2, maximumFractionDigits: 2 });
export const num = (n: number) => n.toLocaleString("en-US");
// clock is an event's local time of day (HH:MM:SS); "" when unknown.
export const clock = (ms: number) =>
  ms ? new Date(ms).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false }) : "";
