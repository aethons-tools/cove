#!/bin/sh
# Reference per-task GitHub App minter. at-cove runs this on the HOST as the
# AT_TASK_GIT_TOKEN resolver, once before each git step, with COVE_RUN_* in the env.
# It mints a short-lived installation token scoped to COVE_RUN_REPO (contents + PRs).
#
# Provision (operator): a GitHub App with contents:write + pull_requests:write,
# installed on the org; then export on the at-cove host:
#   COVE_GH_APP_ID, COVE_GH_INSTALL_ID, COVE_GH_APP_KEY (path to the App .pem)
# Requires: openssl, curl, jq. Fail-closed: any error exits non-zero (aborts dispatch).
set -eu
: "${COVE_RUN_REPO:?COVE_RUN_REPO not set (run under at-cove dispatch)}"
: "${COVE_GH_APP_ID:?export COVE_GH_APP_ID}"
: "${COVE_GH_INSTALL_ID:?export COVE_GH_INSTALL_ID}"
: "${COVE_GH_APP_KEY:?export COVE_GH_APP_KEY (path to the App private key .pem)}"

repo_name=${COVE_RUN_REPO#*/}   # owner/name -> name (installation is org-scoped)

b64() { openssl base64 -A | tr '+/' '-_' | tr -d '='; }
now=$(date +%s)
header=$(printf '{"alg":"RS256","typ":"JWT"}' | b64)
payload=$(printf '{"iat":%d,"exp":%d,"iss":"%s"}' "$((now - 60))" "$((now + 540))" "$COVE_GH_APP_ID" | b64)
sig=$(printf '%s.%s' "$header" "$payload" | openssl dgst -sha256 -sign "$COVE_GH_APP_KEY" | b64)
jwt="$header.$payload.$sig"

curl -fsS -X POST \
  -H "Authorization: Bearer $jwt" \
  -H "Accept: application/vnd.github+json" \
  "https://api.github.com/app/installations/$COVE_GH_INSTALL_ID/access_tokens" \
  -d "{\"repositories\":[\"$repo_name\"],\"permissions\":{\"contents\":\"write\",\"pull_requests\":\"write\"}}" \
  | jq -er '.token'
