// The rail's saved order of Me and Agents
// (docs/modules/ROOT/pages/usage/jam/rail-api.adoc#order).

// RailSection is a section with a saved order.
export type RailSection = "me" | "agents";

// moveTo is ids with id moved to target's place, as a drop puts it: before
// the target when moving up, after it when moving down. An unknown id or
// target leaves the order alone.
export function moveTo(ids: string[], id: string, target: string): string[] {
  const from = ids.indexOf(id);
  const to = ids.indexOf(target);
  if (from < 0 || to < 0 || from === to) return ids;
  const next = ids.filter((x) => x !== id);
  next.splice(to, 0, id);
  return next;
}

// ordered is items in the order of ids; the items ids doesn't name keep
// their order after them.
export function ordered<T>(items: T[], key: (item: T) => string, ids: string[]): T[] {
  const at = new Map(ids.map((id, i) => [id, i]));
  const rank = (t: T) => at.get(key(t)) ?? ids.length;
  return items.map((t, i) => ({ t, i })).sort((a, b) => rank(a.t) - rank(b.t) || a.i - b.i).map(({ t }) => t);
}
