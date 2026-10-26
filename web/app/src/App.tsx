import type { RouteObject } from "react-router";
import type { Bootstrap } from "./api/types";
import { Shell } from "./shell/Shell";
import { Home } from "./routes/Home";
import { Conversation } from "./routes/Conversation";

// routes are the app's client routes under /app/next/ (webapp serves
// index.html for every one of them).
export function routes(boot: Bootstrap): RouteObject[] {
  return [{
    path: "/app/next/",
    element: <Shell boot={boot} />,
    children: [
      { index: true, element: <Home /> },
      { path: "c/:channel", element: <Conversation /> },
    ],
  }];
}
