import { apiAt } from "../api/client";
import { branchHeaders } from "./branch/branch";

// ADMIN is the admin area's mount (adminui's appMount).
export const ADMIN = "/app/admin/";

// adminApi is the /app/admin/api client; it names the open config branch.
export const adminApi = apiAt("/app/admin/api/", branchHeaders);

// nodeHref is the page of a Config tree node (TreeNode.path).
export const nodeHref = (path: string) => ADMIN + path;
