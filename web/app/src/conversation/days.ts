const key = (d: Date) => `${d.getFullYear()}-${d.getMonth()}-${d.getDate()}`;

// sameDay: a and b (RFC 3339) fall on the same local day.
export const sameDay = (a: string, b: string) => key(new Date(a)) === key(new Date(b));

// dayLabel is a day divider's text: Today, Yesterday, or the full local date.
export function dayLabel(iso: string, now = new Date()): string {
  const d = new Date(iso);
  if (key(d) === key(now)) return "Today";
  const y = new Date(now);
  y.setDate(now.getDate() - 1);
  if (key(d) === key(y)) return "Yesterday";
  return d.toLocaleDateString(undefined, { weekday: "long", month: "long", day: "numeric", year: "numeric" });
}

// timeOf is a message's local time of day.
export const timeOf = (iso: string) => new Date(iso).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
