import { Anchor, Stack, Text, Title } from "@mantine/core";
import { useLocation } from "react-router";
import { ADMIN } from "./paths";
import { oldHref } from "./legacy";

// NotMovedYet stands in for an admin page not yet ported to the app: it
// links the same page in the old admin.
export function NotMovedYet() {
  const { pathname, search } = useLocation();
  const path = pathname.startsWith(ADMIN) ? pathname.slice(ADMIN.length) : "";
  return (
    <Stack gap="xs">
      <Title order={3}>This page hasn't moved yet</Title>
      <Text c="dimmed" size="sm">It is still served by the current admin.</Text>
      <Anchor href={oldHref(path) + search} size="sm">Open it in the current admin</Anchor>
    </Stack>
  );
}
