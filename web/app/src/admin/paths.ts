import { apiAt } from "../api/client";

// ADMIN is the admin area's mount while it is built beside the old admin
// pages (adminui's appMount); the cutover moves it to /app/admin/.
export const ADMIN = "/app/admin/next/";

// adminApi is the /app/admin/api client.
export const adminApi = apiAt("/app/admin/api/");

// nodeHref is the page of a Config tree node (TreeNode.path).
export const nodeHref = (path: string) => ADMIN + path;
