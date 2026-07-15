* Special `.claude.json` file handling:
  * the `claude` install writes a `.claude.json` file to the home directory; it has valuable information in it
  * in the dockerfile, we should blend it into the `.init-agent-files/.claude.json`.
  * it will still be copied to its final destination by the existing script
  * the existing `.init-agent-files/.claude.json` file should be pruned down to just the entries we need to clean up
    the startup experience.