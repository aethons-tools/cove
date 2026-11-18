import { useParams } from "react-router";
import { WorkPage } from "../work/WorkPage";

export function Work() {
  const { key = "" } = useParams();
  return <WorkPage key={key} unitKey={key} />;
}
