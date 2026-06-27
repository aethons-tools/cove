* This kit's `config.yml` does not declare a `GITHUB_TOKEN` secret:
  * Without it there is no token for git auth, so `git push origin main` over HTTPS fails with
    `could not read Username for 'https://github.com'`.
  * Add a `GITHUB_TOKEN` secret to `.at-cove/config.yml` (resolved from the host at connect time)
    to enable git over HTTPS. To be configured from the host machine.

* Special `.claude.json` file handling:
  * the `claude` install writes a `.claude.json` file to the home directory; it has valuable information in it
  * in the dockerfile, we should blend it into the `.init-agent-files/.claude.json`.
  * it will still be copied to its final destination by the existing script
  * the existing `.init-agent-files/.claude.json` file should be pruned down to just the entries we need to clean up
    the startup experience.