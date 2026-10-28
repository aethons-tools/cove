import { notifications } from "@mantine/notifications";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import type { ApiError } from "../../api/client";
import type { RoleDetail, RoleResult } from "../api/types";
import { adminApi } from "../paths";
import { useAdminMutation } from "../ui/useAdminMutation";

const enc = encodeURIComponent;
export const roleKey = (p: string, r: string) => ["admin", "role", p, r];

// useRole is a role's page.
export function useRole(p: string, r: string) {
  return useQuery<RoleDetail, ApiError>({ queryKey: roleKey(p, r), queryFn: () => adminApi.get(`projects/${enc(p)}/roles/${enc(r)}`) });
}

// useRoleWrite runs one section write on a role: its answer replaces the
// page, and its message (a standing reset or upgrade) shows.
export function useRoleWrite(p: string, r: string) {
  const client = useQueryClient();
  return useAdminMutation((v: { path: string; body?: unknown; del?: boolean }) => {
    const url = `projects/${enc(p)}/roles/${enc(r)}/${v.path}`;
    return v.del ? adminApi.del<RoleResult>(url) : adminApi.post<RoleResult>(url, v.body ?? {});
  }, {
    invalidate: [["admin", "project-agents", p]],
    onSuccess: (res) => {
      client.setQueryData(roleKey(p, r), res.role);
      if (res.message) notifications.show({ color: "green", message: res.message });
    },
    onError: (e) => { if (e.code === "not_found") void client.invalidateQueries({ queryKey: roleKey(p, r) }); },
  });
}
