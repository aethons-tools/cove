import Markdown, { type Components } from "react-markdown";
import "../../conversation/body.css";

// Links leave the app: a new tab, without an opener. In-page #fragment links
// stay in the tab, and a link whose href urlTransform dropped (javascript:, …)
// is plain text. Images are not loaded (a remote one is a tracking pixel):
// each is a link to it, labelled by its alt text; inside a link, just the alt
// text (a link can't hold a link).
const external = { target: "_blank", rel: "noopener noreferrer" };
const components: Components = {
  a: ({ href, children, node }) => {
    if (node?.children.some((c) => c.type === "element" && c.tagName === "img")) {
      children = node.children.map((c) =>
        c.type === "text" ? c.value : c.type === "element" && c.tagName === "img" ? String(c.properties.alt ?? "") : "");
    }
    if (!href) return <>{children}</>;
    return <a href={href} {...(href.startsWith("#") ? {} : external)}>{children}</a>;
  },
  img: ({ src, alt }) => {
    const label = alt || (typeof src === "string" ? src : "") || "image";
    if (typeof src !== "string" || !src) return <>{label}</>;
    return <a href={src} {...external}>{label}</a>;
  },
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
