import { Navigate, type RouteObject } from "react-router";
import type { Bootstrap } from "./api/types";
import { AdminShell } from "./AdminShell";
import { NotMovedYet } from "./NotMovedYet";
import { ADMIN } from "./paths";

// adminRoutes are the admin area's client routes under ADMIN (webapp serves
// index.html for every one of them). Pages not ported yet are NotMovedYet.
export function adminRoutes(boot: Bootstrap): RouteObject[] {
  return [{
    path: ADMIN,
    element: <AdminShell boot={boot} />,
    children: [
      { index: true, element: <NotMovedYet /> },
      { path: "config", element: <Navigate to="tracking" replace /> },
      { path: "config/*", element: <NotMovedYet /> },
      { path: "agents/*", element: <NotMovedYet /> },
      { path: "health/*", element: <NotMovedYet /> },
      { path: "*", element: <NotMovedYet /> },
    ],
  }];
}
