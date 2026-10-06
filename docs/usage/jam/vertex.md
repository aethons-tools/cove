---
summary: Running cove `claude` on Google Vertex AI through Jam's broker — the GCP credential stays on the Jam host as an `exchange: gcp` credential, each region is one path-guarded destination, and the cove holds only its Jam identity.
read_when: You want a role's coves to run Claude on Vertex AI under Jam, are adding or changing a Vertex region or GCP project, or a Vertex cove gets 403 "path not allowed" / 502 "credential unavailable".
owns: the Vertex brokering recipe — the per-region vertex destination (route, upstream, client env, allow-paths), the model-spec it pairs with, and why the cove needs no GCP credential or Google egress
prereqs: serve.md#destinations for the destination verb; credentials.md for `exchange: gcp`; model-specs.md for `claude.provider`
tier: leaf
updated: 2026-10-06
---

# Claude on Vertex AI through Jam

A cove on a `provider: vertex` model-spec reaches Vertex **only through the
broker**. The Google credentials JSON stays on the Jam host as an
[`exchange: gcp`](credentials.md#demand-and-supply) credential. The broker
injects a short-lived access token that it refreshes itself, so the cove holds
nothing but its Jam identity. This is the same model as the
[anthropic destination](serve.md#destinations). Plain at-cove (no Jam) is
separate: it still seeds the operator's ADC into the VM
([at-cove-secrets.md](../at-cove-secrets.md)).

## 1. The credential

Give a GCP service account `roles/aiplatform.user` and nothing more, since the
broker's token carries that account's whole IAM reach. Supply its key (or a
workload-identity-federation config) in the credentials file, and demand it with
`exchange: gcp`:

```yaml
# serve config
credentials:
  vertex-gcp: { exchange: gcp }
# ~/.config/at-jam/credentials.yml
credentials:
  vertex-gcp: { command: ["cat", "/run/secrets/vertex-sa.json"] }
```

`serve` parses the JSON at startup and fails closed if it is missing or of an
unsupported type. The first token is minted on the first request.

**Your own login (ADC) works too, with a re-login now and then.** Use
`command: ["cat", "<home>/.config/gcloud/application_default_credentials.json"]`.
If your org enforces Google Cloud *session control*, that login's refresh token
lapses after the configured session length, wherever it is held. Once it lapses,
the broker logs one WARN `GCP credential unavailable` (with Google's error code,
typically `invalid_grant`) and vertex requests fail with 502. To recover, run
`gcloud auth application-default login` on the Jam host. The broker re-reads the
supply within 10 seconds and logs `GCP credential recovered`, with no restart.
Rotating a service-account key recovers the same way. A service account (or
workload identity federation) avoids session limits entirely. The Jam host
(not the cove) needs egress to `oauth2.googleapis.com` and the region's
`<region>-aiplatform.googleapis.com` (`aiplatform.googleapis.com` for `global`).

## 2. One destination per region

Claude Code's gateway mode (`CLAUDE_CODE_SKIP_VERTEX_AUTH=1` plus
`ANTHROPIC_VERTEX_BASE_URL`) skips GCP auth and calls
`{base}/projects/P/locations/R/publishers/anthropic/models/M:{rawPredict,streamRawPredict}`.
It sends `ANTHROPIC_CUSTOM_HEADERS` on every request, which is how the identity
travels. So both sides use the `bearer` presets:

```
at-jam destination add --name vertex-us-east5 --route /vertex/us-east5/ \
  --upstream https://us-east5-aiplatform.googleapis.com \
  --identity-in bearer --cred-name vertex-gcp --apply bearer \
  --env 'ANTHROPIC_VERTEX_BASE_URL={url}/v1' \
  --env 'CLAUDE_CODE_SKIP_VERTEX_AUTH=1' \
  --env 'ANTHROPIC_CUSTOM_HEADERS=Authorization: Bearer {token}' \
  --allow-path '/v1/projects/my-proj/locations/us-east5/publishers/anthropic/models/*:rawPredict' \
  --allow-path '/v1/projects/my-proj/locations/us-east5/publishers/anthropic/models/*:streamRawPredict'
```

- **Always set `--allow-path`.** A `cloud-platform` token would otherwise
  turn the route into a general Vertex AI proxy holding the service account's
  powers. The patterns pin the project, the region and Anthropic's
  publisher models. Token counting (`count-tokens:rawPredict`) is covered by the
  first pattern. See [serve.md](serve.md#destinations) for the pattern rules.
- The cove's `Authorization` carries its identity token, as on the anthropic
  route. The broker strips it and applies the GCP token, so the identity never
  reaches Google.
- Add a second region as a second destination, with its own route, upstream and
  patterns. Grant a role **one** vertex destination: two would both set
  `ANTHROPIC_VERTEX_BASE_URL` to different values, which is a
  [connector conflict](connector.md#assembly-and-conflicts) (fail closed).

## 3. The model-spec and role

Bind the role to a `claude` spec with `provider: vertex`. Put the project and
region in `provider-env`, and make them match the destination's patterns:

```yaml
claude:
  provider: vertex
  provider-env:
    ANTHROPIC_VERTEX_PROJECT_ID: my-proj
    CLOUD_ML_REGION: us-east5
```

Then grant the role the destination in its scope ([roster.md](roster.md)). The
harness sets `CLAUDE_CODE_USE_VERTEX=1`
([model-specs.md](model-specs.md#what-a-cove-applies)). The connector supplies
the routing env above, and `provider-env` can never override it. A spec's
`principal.headers` apply on the vertex destination
([model-spec-headers.md](model-spec-headers.md#where-rules-apply)).

## Troubleshooting

| Symptom | Cause |
|---|---|
| 403 `path not allowed on this destination` | The spec's project/region (or the model path) doesn't match the destination's `allow_paths`. The broker logs `reason="path not in allow_paths"` with the path. |
| 502 `credential unavailable` | The token exchange failed: the supplied JSON is wrong or revoked, a user login's session lapsed (see the `GCP credential unavailable` WARN and [re-login](#1-the-credential)), or the Jam host can't reach `oauth2.googleapis.com`. |
| 401 `missing identity` | The cove lacks `ANTHROPIC_CUSTOM_HEADERS`: the role isn't granted the destination, or the destination's `env` is incomplete. |
| 403 from Google | The service account lacks `roles/aiplatform.user` on the project, or the model isn't enabled in that region. |
