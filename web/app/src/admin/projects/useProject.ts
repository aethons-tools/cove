import { useQuery, useQueryClient } from "@tanstack/react-query";
import type { ApiError } from "../../api/client";
import type { ProjectDetail } from "../api/types";
import { adminApi } from "../paths";
import { useAdminMutation } from "../ui/useAdminMutation";

const enc = encodeURIComponent;

// projectsKey is the projects list's query.
export const projectsKey = ["admin", "projects"];

// useProject is a project's detail; section "intercom" carries its recent log.
export function useProject(name: string, section = "") {
  return useQuery<ProjectDetail, ApiError>({
    queryKey: ["admin", "project", name, section],
    queryFn: () => adminApi.get(`projects/${enc(name)}${section ? `?section=${section}` : ""}`),
  });
}

// useProjectWrite runs one section write on project; its answer (the
// project) replaces the page's data.
export function useProjectWrite(name: string, section = "") {
  const client = useQueryClient();
  return useAdminMutation((v: { path: string; body?: unknown; del?: boolean }) => {
    const url = `projects/${enc(name)}/${v.path}${section ? (v.path.includes("?") ? "&" : "?") + `section=${section}` : ""}`;
    return v.del ? adminApi.del<ProjectDetail>(url) : adminApi.post<ProjectDetail>(url, v.body);
  }, {
    invalidate: [projectsKey],
    onSuccess: (d) => {
      // The answer is this page's data; the project's other pages refetch
      // when next shown.
      void client.invalidateQueries({ queryKey: ["admin", "project", name], refetchType: "none" });
      client.setQueryData(["admin", "project", name, section], d);
    },
    // A 404 may mean the project itself is gone (removed in another tab):
    // re-read it, and the page turns into "Project not found" if so.
    onError: (e) => { if (e.code === "not_found") void client.invalidateQueries({ queryKey: ["admin", "project", name] }); },
  });
}
