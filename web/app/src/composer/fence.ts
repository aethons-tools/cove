// fenceFor is a backtick fence longer than any backtick run in s (at least
// three), so pasted text with its own fences stays inside the block.
export function fenceFor(s: string): string {
  let longest = 0, run = 0;
  for (const ch of s) {
    if (ch === "`") { run++; longest = Math.max(longest, run); } else run = 0;
  }
  return "`".repeat(Math.max(3, longest + 1));
}

// asCodeBlock is text as a fenced block on its own lines, given the box's
// text before the cursor.
export function asCodeBlock(text: string, before: string): string {
  const f = fenceFor(text);
  return (before && !before.endsWith("\n") ? "\n" : "") + f + "\n" + text + (text.endsWith("\n") ? "" : "\n") + f + "\n";
}
