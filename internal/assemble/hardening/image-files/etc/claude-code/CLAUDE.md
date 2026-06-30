You are running in an isolated sandbox.
`/home/agent/workspace` is either mapped to a host folder for you collaborate on while chatting,
or it is a volume mount that you will need to clone the project into.
`/agent-data` is your `CLAUDE_CONFIG_DIR`.
THESE ARE THE ONLY DIRECTORY TREES THAT ARE PERSISTENT.
All other directories are ephemeral and could reset at any time.
If you need to alter anything outside of the persistent directories, 
stop and report the needed change.
If you need to install tools for more than a simple one-off run,
stop and report the needed change.
