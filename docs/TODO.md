* Git credential helper for pushing from the dev sandbox is not working:
  * `git push origin main` over HTTPS fails with `could not read Username for 'https://github.com'`
    — no credential helper / `GITHUB_TOKEN` is configured on the host, and there is no interactive prompt.
  * SSH (port 22) is blocked by the egress lock, so the SSH remote is not an option either.
  * Need a working push path from here: e.g. a credential helper that feeds a PAT via the HTTPS proxy,
    or document that pushes happen from a machine with normal network access.

* Special `.claude.json` file handling:
  * the `claude` install writes a `.claude.json` file to the home directory; it has valuable information in it
  * in the dockerfile, we should blend it into the `.init-agent-files/.claude.json`.
  * it will still be copied to its final destination by the existing script
  * the existing `.init-agent-files/.claude.json` file should be pruned down to just the entries we need to clean up
    the startup experience.