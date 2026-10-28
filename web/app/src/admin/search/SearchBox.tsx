import { TextInput } from "@mantine/core";
import { useEffect, useRef, useState } from "react";
import { useNavigate } from "react-router";
import { nodeHref } from "../paths";

// typing is whether a key event belongs to something being typed in.
function typing(t: EventTarget | null): boolean {
  if (!(t instanceof HTMLElement)) return false;
  return t.isContentEditable || ["INPUT", "TEXTAREA", "SELECT"].includes(t.tagName);
}

// SearchBox is the title bar's search: "/" focuses it from anywhere (but
// not while typing elsewhere); Enter searches, jumping straight to a single
// exact match.
export function SearchBox() {
  const navigate = useNavigate();
  const [q, setQ] = useState("");
  const box = useRef<HTMLInputElement>(null);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "/" || e.ctrlKey || e.metaKey || e.altKey || typing(e.target)) return;
      e.preventDefault();
      box.current?.focus();
      box.current?.select(); // typing replaces the last query
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, []);
  return (
    <form role="search" onSubmit={(e) => {
      e.preventDefault();
      if (q.trim()) void navigate(nodeHref(`search?q=${encodeURIComponent(q.trim())}&go=1`));
    }}>
      <TextInput ref={box} type="search" size="xs" w={{ base: 120, sm: 220 }} aria-label="Search" placeholder="Search everything… ( / )"
        value={q} onChange={(e) => setQ(e.currentTarget.value)} />
    </form>
  );
}
