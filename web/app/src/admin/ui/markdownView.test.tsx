import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { MarkdownView } from "./MarkdownView";

describe("MarkdownView", () => {
  it("renders headings, lists and code", () => {
    render(<MarkdownView>{"# The manual\n\n- one\n- two\n\nRun `just test`.\n\n```\ngo test ./...\n```\n"}</MarkdownView>);
    expect(screen.getByRole("heading", { level: 1, name: "The manual" })).toBeInTheDocument();
    expect(screen.getAllByRole("listitem").map((li) => li.textContent)).toEqual(["one", "two"]);
    expect(screen.getByText("just test").tagName).toBe("CODE");
    expect(screen.getByText("go test ./...").closest("pre")).not.toBeNull();
  });

  it("opens links in a new tab", () => {
    render(<MarkdownView>{"See [the docs](https://example.com/docs)."}</MarkdownView>);
    const a = screen.getByRole("link", { name: "the docs" });
    expect(a).toHaveAttribute("href", "https://example.com/docs");
    expect(a).toHaveAttribute("target", "_blank");
    expect(a).toHaveAttribute("rel", "noopener noreferrer");
  });

  it("does not render raw HTML", () => {
    const { container } = render(<MarkdownView>{"hi <script>alert(1)</script>\n\n<img src=x onerror=\"alert(2)\">\n\n<b>bold</b>"}</MarkdownView>);
    expect(container.querySelector("script")).toBeNull();
    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector("b")).toBeNull();
    expect(container.querySelector("[onerror]")).toBeNull();
  });

  it("drops a javascript: link", () => {
    render(<MarkdownView>{"[x](javascript:alert(1))"}</MarkdownView>);
    expect(screen.getByText("x").getAttribute("href") ?? "").not.toContain("javascript");
  });
});
