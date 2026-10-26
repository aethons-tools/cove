import { AppShell } from "@mantine/core";
import { Outlet } from "react-router";
import type { Bootstrap } from "../api/types";
import { TitleBar } from "./TitleBar";
import { Rail } from "../rail/Rail";
import { useLiveEvents } from "../live/useLiveEvents";

export function Shell({ boot }: { boot: Bootstrap }) {
  useLiveEvents();
  return (
    <AppShell header={{ height: 48 }} navbar={{ width: 300, breakpoint: "sm" }} padding="md">
      <AppShell.Header><TitleBar boot={boot} /></AppShell.Header>
      <AppShell.Navbar><Rail /></AppShell.Navbar>
      <AppShell.Main><Outlet /></AppShell.Main>
    </AppShell>
  );
}
