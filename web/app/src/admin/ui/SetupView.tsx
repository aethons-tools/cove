// SetupView is a studio's or a role's setup (Task 4).
export function SetupView({ path }: { path: string; managers?: string[] }) {
  return <div data-path={path} />;
}
