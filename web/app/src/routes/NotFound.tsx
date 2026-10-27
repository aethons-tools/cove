import { Anchor, Stack, Text } from "@mantine/core";
import { Link } from "react-router";
import { APP } from "../paths";

// NotFound is any /app/user/ path the app has no route for (webapp serves
// the app for every page path, so a stale link lands here).
export function NotFound() {
  return (
    <Stack gap={4}>
      <Text c="dimmed">Nothing here.</Text>
      <Anchor component={Link} to={APP} size="sm">Back to the inbox</Anchor>
    </Stack>
  );
}
