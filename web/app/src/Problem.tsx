import { Alert, Anchor } from "@mantine/core";
import type { ApiError } from "./api/client";

// Problem shows an API failure; an expired sign-in gets a sign-in link.
export function Problem({ error }: { error: ApiError }) {
  return (
    <Alert color={error.code === "signed_out" ? "yellow" : "red"} m="sm">
      {error.message}{" "}
      {error.code === "signed_out" && <Anchor href="/app/auth/login">Sign in</Anchor>}
    </Alert>
  );
}
