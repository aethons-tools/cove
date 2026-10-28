import { Navigate, type RouteObject } from "react-router";
import type { Bootstrap } from "./api/types";
import { AdminShell } from "./AdminShell";
import { NotMovedYet } from "./NotMovedYet";
import { ADMIN } from "./paths";
import { UsersPage } from "./users/UsersPage";
import { TrackingPage } from "./tracking/TrackingPage";
import { ProjectsPage } from "./projects/ProjectsPage";
import { ProjectPage } from "./projects/ProjectPage";
import { MembersPage } from "./projects/MembersPage";
import { MemberPage } from "./projects/MemberPage";
import { IntercomPage } from "./projects/IntercomPage";
import { EscalationPage } from "./projects/EscalationPage";
import { AgentsPage } from "./roles/AgentsPage";
import { RolePage } from "./roles/RolePage";
import { UserPage } from "./users/UserPage";

// adminRoutes are the admin area's client routes under ADMIN (webapp serves
// index.html for every one of them). Pages not ported yet are NotMovedYet.
export function adminRoutes(boot: Bootstrap): RouteObject[] {
  return [{
    path: ADMIN,
    element: <AdminShell boot={boot} />,
    children: [
      { index: true, element: <NotMovedYet /> },
      { path: "config", element: <Navigate to="tracking" replace /> },
      { path: "config/tracking", element: <TrackingPage /> },
      { path: "config/projects", element: <ProjectsPage /> },
      { path: "config/projects/:project", element: <ProjectPage /> },
      { path: "config/projects/:project/members", element: <MembersPage /> },
      { path: "config/projects/:project/members/:user", element: <MemberPage /> },
      { path: "config/projects/:project/intercom", element: <IntercomPage /> },
      { path: "config/projects/:project/escalation", element: <EscalationPage /> },
      { path: "config/projects/:project/agents", element: <AgentsPage /> },
      { path: "config/projects/:project/agents/:role", element: <RolePage /> },
      { path: "config/users", element: <UsersPage /> },
      { path: "config/users/:user", element: <UserPage /> },
      { path: "config/*", element: <NotMovedYet /> },
      { path: "agents/*", element: <NotMovedYet /> },
      { path: "health/*", element: <NotMovedYet /> },
      { path: "*", element: <NotMovedYet /> },
    ],
  }];
}
