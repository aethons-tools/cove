#!/usr/bin/env bash
# cove git credential helper.
#
# Supplies an HTTPS git token from the session environment, memory-only — the
# token is never written to disk (no `store`). GitHub (github.com) authenticates
# with GITHUB_TOKEN; the kit's GitLab host (GITLAB_HOST, defaulted by at-cove for a
# gitlab source-control kit) authenticates with GITLAB_TOKEN. Declare the matching
# token as a collaborator secret in the kit's config.yml; cove injects it into the
# connect session env, and git (run inside that session) invokes this helper for the
# host(s) the system gitconfig scopes to it.
#
# Only answers `get`; `store`/`erase` are no-ops, so nothing is persisted. If the
# relevant token is unset it emits nothing and git fails closed (no prompt under the
# non-interactive sandbox) rather than using stale credentials.
set -euo pipefail

[ "${1:-}" = "get" ] || exit 0

# Git passes the request as key=value lines on stdin, terminated by a blank line.
# Read the target host so one helper can serve GitHub and the kit's GitLab host.
host=
while IFS='=' read -r key value; do
	[ -n "$key" ] || break
	if [ "$key" = host ]; then host=$value; fi
done

case "$host" in
github.com)
	[ -n "${GITHUB_TOKEN:-}" ] || exit 0
	printf 'username=x-access-token\npassword=%s\n' "$GITHUB_TOKEN"
	;;
*)
	# The kit's GitLab host authenticates with GITLAB_TOKEN. GitLab ignores the
	# username for a PAT/OAuth2 token over HTTPS; `oauth2` is the conventional value.
	# Gate on GITLAB_HOST so the token is only ever offered to the kit's own host.
	[ -n "${GITLAB_HOST:-}" ] && [ "$host" = "$GITLAB_HOST" ] || exit 0
	[ -n "${GITLAB_TOKEN:-}" ] || exit 0
	printf 'username=oauth2\npassword=%s\n' "$GITLAB_TOKEN"
	;;
esac
