import { useParams } from "react-router";
import { SessionPage } from "../session/SessionPage";

export function Session() {
  const { session = "" } = useParams();
  return <SessionPage key={session} id={session} />;
}
