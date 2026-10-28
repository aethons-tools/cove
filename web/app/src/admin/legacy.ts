// oldHref is the old admin page for a page path under the admin app's mount
// (segments arrive already escaped): where a page that hasn't moved yet links,
// and — inverted — the cutover's redirects.
export function oldHref(path: string): string {
  const old = "/app/admin/";
  const s = path.replace(/^\/+|\/+$/g, "").split("/").filter(Boolean);
  if (s[0] !== "config") return old + s.join("/");
  const [, area, name, section, sub] = s;
  if (area === undefined || area === "tracking") return old + "config";
  if (area !== "projects" || name === undefined) return old + s.slice(1).join("/");
  const project = `${old}projects/${name}`;
  if (section === undefined) return project;
  if (sub === undefined) return `${project}/${section}`;
  if (section === "members") return `${old}users/${sub}`;
  if (section === "agents") return `${project}/roles/${sub}`;
  return `${project}/${section}`;
}
