import { Anchor, Stack, Text, Title } from "@mantine/core";
import { Link } from "react-router";
import { ADMIN } from "./paths";

// NoSuchPage is an admin path the app has no page for (an old page's path
// is redirected by the server before it gets here).
export function NoSuchPage() {
  return (
    <Stack gap="xs">
      <Title order={2}>No such page</Title>
      <Text c="dimmed" size="sm">Nothing lives at this address in the admin console.</Text>
      <Anchor component={Link} to={ADMIN} size="sm">Go to the Dashboard</Anchor>
    </Stack>
  );
}
