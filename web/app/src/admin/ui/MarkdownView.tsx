import Markdown, { type Components } from "react-markdown";
import "../../conversation/body.css";

// Links leave the app: a new tab, without an opener.
const components: Components = {
  a: ({ href, children }) => <a href={href} target="_blank" rel="noopener noreferrer">{children}</a>,
};

// MarkdownView shows markdown formatted, for reading (it is edited as source).
// Raw HTML in it is not rendered: react-markdown's default, so never add
// rehype-raw here. Styled as a message body (body.css).
export function MarkdownView({ children }: { children: string }) {
  return (
    <div className="jam-body">
      <Markdown components={components}>{children}</Markdown>
    </div>
  );
}
