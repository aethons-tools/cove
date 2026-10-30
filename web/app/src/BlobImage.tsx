import { Image, type ImageProps } from "@mantine/core";
import { useBlobUrl } from "./blobs";

// BlobImage is an image of a user-area blob path, resolved by the shell's
// blob resolver when it has one (see blobs.ts).
export function BlobImage({ path, ...rest }: { path: string } & Omit<ImageProps, "src"> & { alt: string; loading?: "lazy" }) {
  return <Image {...rest} src={useBlobUrl(path)} />;
}
