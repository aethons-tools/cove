// Highlight is text with each case-insensitive occurrence of q marked — as
// React text, so nothing in it is markup.
export function Highlight({ text, q }: { text: string; q: string }) {
  const needle = q.trim().toLowerCase();
  const lower = text.toLowerCase();
  if (!needle || lower.length !== text.length) return <>{text}</>; // a case fold that changes length: don't risk misaligned marks
  const out: React.ReactNode[] = [];
  let at = 0;
  for (let i = lower.indexOf(needle); i >= 0; i = lower.indexOf(needle, at)) {
    if (i > at) out.push(text.slice(at, i));
    out.push(<mark key={i}>{text.slice(i, i + needle.length)}</mark>);
    at = i + needle.length;
  }
  out.push(text.slice(at));
  return <>{out}</>;
}
