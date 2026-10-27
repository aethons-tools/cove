import type { RouteObject } from "react-router";
import type { Bootstrap } from "./api/types";
import { Shell } from "./shell/Shell";
import { Home } from "./routes/Home";
import { Conversation } from "./routes/Conversation";
import { NewMessage } from "./compose/NewMessage";
import { BoardPage } from "./boards/BoardPage";
import { NotFound } from "./routes/NotFound";
import { APP } from "./paths";

// routes are the app's client routes under /app/user/ (webapp serves
// index.html for every one of them).
export function routes(boot: Bootstrap): RouteObject[] {
  return [{
    path: APP,
    element: <Shell boot={boot} />,
    children: [
      { index: true, element: <Home /> },
      { path: "c/:channel", element: <Conversation /> },
      { path: "new", element: <NewMessage /> },
      { path: "boards/:id", element: <BoardPage /> },
      { path: "*", element: <NotFound /> },
    ],
  }];
}
