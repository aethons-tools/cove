import { apiAt } from "../api/client";

// ADMIN is the admin area's mount (adminui's appMount).
export const ADMIN = "/app/admin/";

// adminApi is the /app/admin/api client.
export const adminApi = apiAt("/app/admin/api/");

// nodeHref is the page of a Config tree node (TreeNode.path).
export const nodeHref = (path: string) => ADMIN + path;
