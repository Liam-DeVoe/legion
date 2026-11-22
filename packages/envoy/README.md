# envoy

Docker-first cross-machine notification routing for AI agent sessions.

## Goals

- Route GitHub, Slack, WhatsApp, and agent-to-agent notifications to the correct agent session
- Keep all envoy-owned code and infrastructure in this repo
- Run every envoy component inside Docker containers
- Avoid host installs except Docker on remote machines that need it
- Inject secrets only at runtime via the `secrets` wrapper from `~/.dotfiles/shims/secrets`

## Repo boundaries

This repo owns:

- NATS cluster config and container packaging
- Envoy shared code
- Envoy listener / router / webhook receivers
- Compose files, deploy scripts, and Dockerfiles

This repo does **not** silently absorb changes to other maintained software.

If envoy needs changes in `@sjawhar/whatsapp-mcp` or another maintained repo:

- make that change in the other repo
- keep it as a separate clear jj change/commit
- do **not** bundle it into an existing `sami` octopus merge
- document the dependency here under `docs/external-repos.md`

## Secrets

Secrets are never stored in this repo. Runtime injection uses:

```bash
secrets ENVOY_SLACK_SIGNING_SECRET ENVOY_GITHUB_WEBHOOK_SECRET -- <command>
```

Current secret names:

- `ENVOY_SLACK_SIGNING_SECRET`
- `ENVOY_GITHUB_WEBHOOK_SECRET`

The secrets broker (`cmd/cipherd`) supports these types for `agent-secrets secret create --type` and `secret retag --type`:

- `github-app`: stores an App id and private key; releases installation tokens for its listed GitHub owners.
- `google-login`: stores an `authorized_user` credential; releases a Google access token, not the refresh token.
- `azure-login`: stores `{"tenant_id":"<tenant>","client_id":"<client>"}`; releases JSON with `tenant_id`, `client_id`, `federated_token` and `expires_at` (RFC3339 UTC, from the token's JWT `exp`).
  Set `BROKER_AZURE_LOGIN_SECRET` to the one agent-secret name allowed to release that token; unset, every Azure release is refused.
  The configured secret must be human tier in the policy snapshot checked for each release, and its grant decision must record a person's approval, never an automatic grant.
  Retagging another secret to `azure-login`, or retagging the configured secret to agent tier, releases no Azure token.
  Each release reads the broker pod's projected ServiceAccount token from `BROKER_AZURE_TOKEN_FILE`, default `/var/run/secrets/azure/tokens/azure-identity-token`.
  Unreadable, malformed or expired tokens refuse the release; token bytes never enter an error or log.
  Mount the token read-only in the broker container alone, with audience `api://AzureADTokenExchange` and a one-hour lifetime.
  Exactly one Entra identity may trust that ServiceAccount subject, since a released token can sign in to every identity that trusts it.
- `google-service-account`: stores `{"service_account":"<id>@<project>.iam.gserviceaccount.com"}`; releases a bare Google access token for that account, minted by impersonating it with the broker's own credential, good for one hour.
  Set `BROKER_SERVICE_ACCOUNT_SECRETS` to whitespace-separated `<SECRET NAME>=<account email>` entries, such as `GCP_ADMIN=gcp-admin@example-project.iam.gserviceaccount.com`; it, and not the stored value, chooses the account the broker mints for, and a secret it does not bind releases nothing (`class=binding`).
  `--scopes "cloud-platform bigquery"` names the OAuth scopes the token carries, each the part of a Google scope after `https://www.googleapis.com/auth/`; without it the token carries `cloud-platform` alone, and the flag is refused on any other type.
  `--admin true` marks a secret whose grant is an administrator's: it is human tier, names its `--requesters`, and takes a `--max-grant` of an hour or less, as a `google-service-account` secret does whether or not it is marked.
  The secret's `--requesters` name its one person owner, and at human tier the registered services that may ask beside them.

Without a type, the broker releases the stored value unchanged.

One decoder reads each type's stored value, `secrets.ParseValue`, which the minters, the broker's policy load and `agent-secrets secret` all call.
`secret create --type`, `secret set` on a typed secret, and `secret retag` that changes the type refuse a value that type’s minter would refuse and write nothing.
A retag whose read of the value the store refuses as access denied writes the tags without the check and says so on standard error.
The broker reads each typed secret’s value once for each new version or type, and refuses a secret whose value the decoder refuses as `value-malformed`.

## Layout

- `internal/contracts/generated.go` — generated Go contract output from `packages/contracts/scripts/gen-go.ts`
- `internal/config` — env parsing
- `internal/webhook` — the GitHub, Slack and Ghost Wispr webhook handlers, and the `ENVOY_WEBHOOKS` read at start-up
- `internal/verify` — GitHub, Slack and Ghost Wispr signature verification
- `cmd/listener` — per-machine listener service, which serves the webhook routes
- `docker/Dockerfile` — the one released image, holding the listener, Dispatch, the broker and `agent-secrets`
- `deploy` — compose files and deploy scripts
- `docs` — architecture, constraints, external repo touchpoints

## Development

Run the Go commands from `packages/envoy`:

```bash
go build ./cmd/dispatch ./cmd/listener
go test -timeout 20m ./...
go test -count=1 -run '^TestName$' ./internal/bus/
gofmt -l .
go vet ./...
```

`internal/dispatch/api` alone takes close to `go test`'s ten-minute default deadline, so a
run that includes it passes `-timeout 20m` (`docs/testing.md` at the repository root, "Go's
package deadline").

`./...` here does not build `infra/`, a Go module of its own that imports packages of this
one (`internal/bus` and `internal/contracts` among them): after a change to one of them,
vet `infra/` too (`infra/README.md`, "Changing the module").

The Postgres-backed tests read `DISPATCH_TEST_DATABASE_URL` (Dispatch) and
`BROKER_TEST_DATABASE_URL` (the secrets broker). Without them most of those tests skip, and
`go test` still prints `ok`. `scripts/dev-postgres.sh` starts a pgvector Postgres and
prints its URL, and `scripts/database-skips.sh` (`scripts/README.md`) fails a run on any
skip.

Build tags hold the slow and live suites, which `go test ./...` does not build: among them
`natslive` (a NATS image, so it needs Docker), `smoke` (the listener image),
`multiprocess` (Dispatch and the broker), and Dispatch's `twobuild` and `memory`
(`cmd/dispatch/README.md`, "Tests"). The `ci-job-*.yaml` files in `.github/workflows/` at
the repository root show how CI runs each.

`internal/dispatch/pmdoc` tests compare with the browser editor through bun scripts in
`pmdoc/gen`: they need `bun` and a `bun install` at the repository root. Without `bun` they
skip, except where `CI` is set, where they fail.

## Local end-to-end

Run the reusable local acceptance driver from the repository root:

```bash
packages/envoy/scripts/e2e-local.sh
```

It builds `cmd/listener` and a throwaway NATS server of its own
(`internal/testnats/cmd/natsd`), so it needs no container runtime, starts both, and
submits signed public GitHub fixtures plus a three-paragraph direct agent
message to a stand-in session that takes every delivery from its own NATS
consumer. The driver asserts the one-warning response for a never-seen GitHub
repository, refuses a registration naming a port, keeps an unheld role's
publish and hands it to the stand-in session that claims that role, keeps a
publish to a role whose holder is gone and hands it to that session when it
claims the role again, refuses a `keep: false` publish to an unheld role,
validates settled pull-request checks,
lifecycle and comment topics, PR-less workflow and Actions-job behavior,
obsolete-topic absence, and the direct-message summary/body contract
`renderInbound` prints.

The NATS server is a process of this checkout's own build, stopped on exit with
the listener and the two fake clients.

Evidence remains in `packages/envoy/out/e2e/` after the run:

- `envelopes.jsonl` captures the raw NATS frames.
- `session-frames.jsonl` captures the envelopes the session's own consumer
  delivered to it.
- `rendered-ts.txt` is every frame rendered by
  `@legion/envoy-client`'s `renderInbound`, the one inbound renderer.

The OS chooses the NATS and listener ports, and the run builds each URL from
the port its own process reports. `E2E_NATS_PORT` or `E2E_PORT` sets a port
explicitly: it is used as given, and one already in use fails the run with
`address already in use`. The session opens no port of its own. CI runs the
driver as the `envoy-e2e-local` job of `.github/workflows/ci.yaml`.

## The listener's session consumers

Each registered session gets a JetStream consumer of its own on `ENVOY_NOTIFICATIONS`, and the
listener binds a durable of its own. How they are named, and the record behind the fresh names,
are in [`consumernames/README.md`](consumernames/README.md).

- **Names.** In the fixed mode a session's consumer is `s-<session id>` and the listener's durable
  is `listener-<machine id>`. In the fresh mode each creation gets a name never made before:
  `session~<nuid>`, carrying the metadata `envoy.logical` and `envoy.session_id`, and
  `listener-<machine id>~<nuid>`, carrying `envoy.logical`. Their records live in the KV bucket
  `envoy_session_consumers`. The start's bind, the monitor's rebuild, the probe, the bound wait and
  `/healthz` all read the durable through its record. The sweep lists only `session~` names that
  carry `envoy.session_id`.
- **The mode setting.** `ENVOY_CONSUMER_NAMES` is `fixed` (also when unset) or `fresh`, read once
  at the start and exactly: any other value, a padded one among them, refuses the start. The start
  logs `session consumer naming mode` with `consumer_names` and `streams_operator_made` (the raw
  `ENVOY_STREAMS_OPERATOR_MADE`, null when unset) before its first NATS connect.
- **A host's listener.** `ENVOY_LISTENER_ROLE=host`, read exactly, marks a listener that connects
  as the NATS user its host's sessions share (`natsgrant.Host`). It runs fixed names for good, reads
  no cutover marker, and refuses `ENVOY_CONSUMER_NAMES=fresh`. The `host:Listener` component
  renders the role only under its input `hostUser: true`.
- **The fixed mode's marker gate.** The session consumers' connection and the main client, on
  which the durable is created and bound, each make no consumer call and no durable create,
  correction or bind until a read of the selection bucket's cutover marker (`envoy.bucket`) on that
  very connection says the cutover did not convert the bucket. A missing bucket counts as no
  marker, and so does a read the connection's grant refuses (the listener beside Dispatch holds no
  such read).
  - Every disconnect shuts the gate, and each reconnect reads the marker again.
  - A converted bucket stops the process, naming `ENVOY_CONSUMER_NAMES`.
  - A read that fails keeps the gate shut and is read again. Meanwhile a registration is answered
    `503 consumer_error` naming the marker.
  - At the start, a marker the listener cannot read makes the start wait rather than run fixed
    names unread.
- **The fresh mode** refuses every session-consumer call until the bucket's sentinel says the
  cutover is done. A registration over lost or unproven selection state is answered
  `503 consumer_error`. A key holding no record of its own is answered naming the key and the
  bucket, for that session alone. An open operation's candidate that is not as the listener creates
  it is answered naming the consumer. A registration whose NATS user may not read the bucket is
  answered `503 no_grant`. The listener's grant (`natsgrant.KVWriteExisting("envoy_session_consumers")`)
  holds the bucket's reads and key writes, and no create or update of its stream.
- **`/healthz`:**
  - The healthy answer carries `consumer_names` (`fixed` or `fresh`). When the durable's read
    fails, it is `UNKNOWN`, with `consumer_names_unknown` naming the failed read and the
    configured mode.
  - In the fresh mode, while the start has not bound its durable over a sentinel that is absent,
    malformed or of another family, the answer is `503` `not_ready`, with `bucket`, `key` and
    `reason` (`absent`, `another family` or `malformed`). The start logs that state once per change.
  - Once serving, a missing bucket, sentinel or cutover marker, or a session's lost key, is
    `200 degraded` naming `session consumer selection bucket envoy_session_consumers`, with every
    field of the healthy answer kept. A refusal clears once a call is admitted.
  - In the fixed mode, a marker read that keeps failing after a reconnect stalls the reconnect
    hooks after the KV watches are rebuilt. Past a minute the answer is `200 degraded` naming the
    `cutover marker read`, with `bucket` and `key`, rather than the `KV watch rebuild`.
- **The counter.** `envoy_session_consumer_names_total{op,outcome}` on `/metrics` counts each
  selection step in the fresh mode. `op` is one of `select`, `create`, `replace`, `delete`,
  `recover`, `missing_current`, `last_floor` or `cleanup`. `outcome` is one of `ok`, `adopted`,
  `retired`, `refused`, `conflict`, `limit` or `error`.
- **Local runs.** `ENVOY_CONSUMER_NAMES=fresh packages/envoy/scripts/e2e-local.sh` makes the
  selection bucket and its sentinel, with the cutover done, before the listener serves, and checks
  the same answers as the default run.

## Contract source of truth

The authoritative event contract lives in `packages/contracts/`.
Run `bun run gen:go` in that package before validating or releasing `packages/envoy`.

Each `legion-envoy-v` release publishes the changelog entries its own tag first shipped: this
package's fragment files (`.changes/unreleased/`) are rendered into that GitHub release's notes.
`CHANGELOG.md` is the history before that and stops where the fragments begin.
