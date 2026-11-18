// APP is the Jam app's mount: the user area (webapp serves index.html for
// every page path under it; Vite builds for it, see vite.config.ts's base).
export const APP = "/app/user/";

// workPath is the page of the unit of work with issue key (a GitHub key,
// owner/repo#<n>, is one escaped segment).
export const workPath = (key: string) => `${APP}w/${encodeURIComponent(key)}`;
