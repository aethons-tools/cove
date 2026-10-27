import { Text } from "@mantine/core";
import { useEffect } from "react";
import { useLocation, useNavigate, useSearchParams } from "react-router";
import { APP } from "../paths";

// Home is /app/user/. It turns the old Inbox's links (?c=<channel>
// [&board=<id>][&tab=<tab>], from bookmarks, notifications and /me/?c=
// redirects) into the app's routes, else asks for a conversation.
export function Home() {
  const [q] = useSearchParams();
  const { hash } = useLocation();
  const go = useNavigate();
  const c = q.get("c"), board = q.get("board"), tab = q.get("tab");
  useEffect(() => {
    if (board) void go(`${APP}boards/${encodeURIComponent(board)}`, { replace: true });
    else if (c) void go(`${APP}c/${encodeURIComponent(c)}${tab ? `?tab=${encodeURIComponent(tab)}` : ""}${hash}`, { replace: true });
  }, [board, c, tab, hash, go]);
  if (board || c) return null;
  return <Text c="dimmed">Pick a conversation.</Text>;
}
