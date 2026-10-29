import { afterEach, describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { setBlobResolver, useBlobUrl } from "./blobs";

function Probe({ path }: { path: string }) { return <span data-testid="u">{useBlobUrl(path) ?? "none"}</span>; }

afterEach(() => setBlobResolver());

describe("useBlobUrl", () => {
  it("returns the path without a resolver", () => {
    render(<Probe path="/app/user/blobs/blb_1" />);
    expect(screen.getByTestId("u")).toHaveTextContent("/app/user/blobs/blb_1");
  });
  it("with a resolver it resolves to the blob URL", async () => {
    setBlobResolver(async (p) => `blob:x/${p.split("/").pop()}`);
    render(<Probe path="/app/user/blobs/blb_1" />);
    expect(screen.getByTestId("u")).toHaveTextContent("none");
    expect(await screen.findByText("blob:x/blb_1")).toBeInTheDocument();
  });
});
