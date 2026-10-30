import { useQueryClient } from "@tanstack/react-query";
import { AppTokenList } from "../../account/ConnectedApps";
import type { AppTokenRow, UserRow } from "../api/types";
import { adminApi } from "../paths";
import { ConfirmAction } from "../ui/ConfirmAction";

// AppTokens is a user's connected apps on their admin page; Revoke goes to
// the operator route and refreshes the user.
export function AppTokens({ user, tokens, refKey }: { user: UserRow; tokens: AppTokenRow[]; refKey: unknown[] }) {
  const client = useQueryClient();
  return (
    <AppTokenList tokens={tokens} empty="No apps are connected." revoke={(t) => (
      <ConfirmAction label="Revoke" confirm={`Revoke ${t.label}? It is signed out of ${user.name} at once.`}
        onConfirm={async () => {
          await adminApi.del(`users/${encodeURIComponent(user.id)}/app-tokens/${encodeURIComponent(t.id)}`);
          await client.invalidateQueries({ queryKey: refKey });
        }} />
    )} />
  );
}
