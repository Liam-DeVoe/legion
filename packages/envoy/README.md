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

Secrets are never stored in this repo.
Runtime injection uses:

```bash
secrets ENVOY_SLACK_SIGNING_SECRET ENVOY_GITHUB_WEBHOOK_SECRET -- <command>
```

Current secret names:

- `ENVOY_SLACK_SIGNING_SECRET`

- `ENVOY_GITHUB_WEBHOOK_SECRET`

The secrets broker (`cmd/broker`) supports these types for `agent-secrets secret create --type` and `secret retag --type`:

- `github-app`: stores an App id and private key; releases installation tokens for its listed GitHub owners.

- `google-login`: stores an `authorized_user` credential; releases a Google access token, not the refresh token.

- `azure-login`: stores `{"tenant_id":"<tenant>","client_id":"<client>"}`;
  releases JSON with `tenant_id`, `client_id`, `federated_token` and `expires_at` (RFC3339 UTC, from the token's JWT `exp`).
  Set `BROKER_AZURE_LOGIN_SECRET` to the one agent-secret name allowed to release that token; unset, every Azure release is refused.
  The configured secret must be human tier in the policy snapshot checked for each release, and its grant decision must record a person's approval, never an automatic grant.
  Retagging another secret to `azure-login`, or retagging the configured secret to agent tier, releases no Azure token.
  Each release reads the broker pod's projected ServiceAccount token from `BROKER_AZURE_TOKEN_FILE`, default `/var/run/secrets/azure/tokens/azure-identity-token`.
  Unreadable, malformed or expired tokens refuse the release; token bytes never enter an error or log.
  Mount the token read-only in the broker container alone, with audience `api://AzureADTokenExchange` and a one-hour lifetime.
  Exactly one Entra identity may trust that ServiceAccount subject, since a released token can sign in to every identity that trusts it.

- `google-service-account`: stores `{"service_account":"<id>@<project>.iam.gserviceaccount.com"}`;
  releases a bare Google access token for that account, minted by impersonating it with the broker's own credential, good for one hour.
  Set `BROKER_SERVICE_ACCOUNT_SECRETS` to whitespace-separated `<SECRET NAME>=<account email>` entries, such as `GCP_ADMIN=gcp-admin@example-project.iam.gserviceaccount.com`;
  it, and not the stored value, chooses the account the broker mints for, and a secret it does not bind releases nothing (`class=binding`).
  `--scopes "cloud-platform bigquery"` names the OAuth scopes the token carries, each the part of a Google scope after `https://www.googleapis.com/auth/`;
  without it the token carries `cloud-platform` alone, and the flag is refused on any other type.
  `--admin true` marks a secret whose grant is an administrator's:
  it is human tier, names its `--requesters`, and takes a `--max-grant` of an hour or less, as a `google-service-account` secret does whether or not it is marked.
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

## The edge listener

`ENVOY_EDGE_PROJECTS` makes a listener the edge listener, the one an outside person's agents reach Envoy through.
It names the projects the edge serves: project keys separated by commas or white space, read as a broker token's own list is (`parseEdgeProjects`, `internal/config/config.go`).
A list that no token could carry refuses the start, naming the variable, and so does a set value that names no project, such as a lone comma or white space.
Unset or empty, the listener serves as every other listener does, and nothing in this section applies.

The edge holds each session to the projects of the broker token that admitted it, also in a deployment where each project is public.
It follows, stores and shows a session no topic of no project, except the GitHub topics of its projects' repositories, and it releases at each registration every claim the session holds of another project's role.
A role message the role lane forwarded to the session's own agent subject before that registration, or after a later claim another writer made for the session, travels on that subject: the relay's check of each frame's topic, which the edge needs before it opens, is what holds those frames to the token's projects.
The header of `cmd/listener/project_scope.go` lists the rules and the function that holds each; `cmd/listener/edge_mode_test.go` and `cmd/listener/edge_repositories_test.go` test each:

- **Start**: the edge refuses to start beside `ENVOY_API_TOKEN`, any `ENVOY_OIDC_*` setting, `ENVOY_WEBHOOKS` or a webhook secret, and without the broker-token settings (`ENVOY_AGENT_SECRETS_URL`, `ENVOY_AGENT_SECRETS_ACCOUNT_TOKEN_FILE` or `ENVOY_AGENT_SECRETS_GOOGLE_AUDIENCE`, and `ENVOY_PUBLIC_URL`).
  Each refusal names the variable (`validateEdgeMode`).

- **Admission**: `/v1` admits a broker token alone, of project scope `named` or `person`, that lists at least one project and only projects the edge serves (`EdgeAdmits`).
  Any other session's broker token gets `403` with the reason `token_not_admitted`, logged as `listener: edge listener refused a broker token`.
  A read-only token, and a request with no broker token, get `401` before any handler runs: the listener's verifier refuses a read-only token as no session's.

- **Topics**: a registration follows only the session's own agent subject, the topics of the token's projects, and the GitHub topics of one repository each that the repository answer of those projects names.
  The `403` of a refused registration lists each refused topic, with the reason `repository_not_in_project` when each is a GitHub topic and `topic_not_in_project` otherwise, and nothing of the registration is stored (`edgeTopicHold`).
  The topics the session's interest record already holds, which another listener may have stored under a token of other projects, are held to the same rule at each registration, a registration planned again after a conflict included.
  Each one the rule refuses is removed from the record and from the session's consumer, and the answer's `warnings` name it.

- **GitHub repositories**: the edge asks the secrets broker which repositories each project's issues resolve to, `GET <ENVOY_AGENT_SECRETS_URL>/v1/project-repositories?project=<KEY>`, with its own broker account.
  It asks at each registration that follows a GitHub topic, and every 30 seconds for each project it serves, each project's read on its own (`cmd/listener/edge_repositories.go`), and it compares owner and name ignoring case.
  A registration for which it gets no whole answer gets `503` with the reason `repositories_unknown`, whose text is the listener's own: what the broker answered goes to the log alone.
  The relay delivers a GitHub event only while an answer no older than 60 seconds names the repository the event's own payload names.
  A subject writes a dot in a name as `_`, so `acme/a.b` and `acme/a_b` publish on one subject, and an answer that names one admits only that one's events.
  It acknowledges any other GitHub event undelivered and counts it in `envoy_edge_github_withheld_total`:
  under `reason="not_in_project"`, or under `reason="answer_unknown"` while a project of the session has no fresh answer.
  In that second case the session's next frame carries `Envoy-Gap: true` with `Envoy-Gap-Reason: repository_withheld`, and every frame that is not GitHub's is delivered as before.
  The mark is kept for the session until a frame carrying it is published, also across the session's relay runs on this listener (a new delivery epoch, a pull or publish error, a receipt pause).
  A gap mark with no reason is the stream's own retention gap, and a frame that marks both carries no reason.
  A refresh removes a session's interest in a repository that left its projects.

- **Publishes**: `POST /v1/messages/publish` takes only a role subject in the form that names the project, `notifications.role.<KEY>.<role>`, of one of the token's projects and for a role that works for that project;
  anything else gets `403 topic_not_published` (`edgePublishRefusal`).

- **Senders**: `POST /v1/messages/send` and `POST /v1/messages/publish` take only a message from the session itself: `source` `agent` or left out, and `source_session` the token's session.
  Any other gets `403 sender_not_session` (`edgeSenderRefusal`), so no edge session sends a message under another source, such as a Dispatch event or a person's message, or under no session.

- **Roles**: `POST /v1/roles/set` and `GET /v1/roles/{role}` take only a role that works for one of the token's projects, else `403 role_not_in_project` (`edgeRoleRefusal`).
  `GET /v1/roles/{role}` names a holder whose registering token listed none of the token's projects without its directory, machine, host, title, client instance and capabilities (`edgeRoleHolder`).
  A registration renews only the session's claims of roles of its token's projects: a claim the session holds of another project's role, which another listener granted it or a token that listed more did, is released, not renewed, and the answer's `warnings` name it (`store.Registry.RenewRoleClaims`).
  A claimant made live again is handed the kept messages of its token's projects' roles alone.

- **Sends**: `POST /v1/messages/send` reaches only a session whose registering token listed one of the sender's projects.
  Any other target gets the `404` of a session no listener has heard of, and no wake is reported (`sessionOfProjects`).

- **Reads**: `GET /v1/sessions`, `GET /v1/registry/{session_id}` and the three `/v1/legion/agents` reads answer only the sessions and Legion agents of the token's projects, each session with only those projects' roles and topics (`edgeShownTopics`);
  any other session gets the `404` of an unknown one.
  A session's own `GET /v1/interests/{session_id}` shows the topics of its token's projects alone.
  `GET /v1/interests/` and another session's `GET /v1/interests/{session_id}` get `403 session_not_in_project`, and a write of the Legion agent registry gets `403 legion_registry_not_written`.

Every edge refusal's body is `listenerwire.EdgeRefusal`, whose comment in `listenerwire/listenerwire.go` names each reason and says how a client reads a refused registration's `refused` list.

Every listener that checks broker tokens records the registering token's projects on the session's entry in `envoy_sessions` (`session.SessionEntry.Projects`).
The edge's send and read rules read that record, and no answer of the API carries it.

On every listener, a role claim's key is `store.RoleClaimKeyOf` of the name it was claimed under, and that function's comment says which key each name gets.
A free-form role under a project (`PRIV.lead-reviewer`) is that project's own claim, keyed `<project>.<role>`, beside the claim of the same name under no project;
every other name keeps its bare role name as its key.
`POST /v1/roles/set` refuses with `400`, naming both projects, a Legion role under any project but the one its own name gives (`contracts.ClaimProject`), and the edge's role and publish rules read a role's project by that function too.
A role message on a lane in the form that names a project, for a Legion role of another project (`notifications.role.PRIV.legion-infra-reviewer`), reaches no holder: the role lane drops it, and `POST /v1/messages/publish` refuses it with `400` naming the role's own project (`roleLaneProjectError`).

## Local end-to-end

Run the reusable local acceptance driver from the repository root:

```bash
packages/envoy/scripts/e2e-local.sh
```

It builds `cmd/listener` and a throwaway NATS server of its own (`internal/testnats/cmd/natsd`), so it needs no container runtime, starts both, and submits signed public GitHub fixtures plus a three-paragraph direct agent message to a stand-in session that takes every delivery from its own NATS consumer.
The driver asserts the one-warning response for a never-seen GitHub repository, refuses a registration naming a port, keeps an unheld role's publish and hands it to the stand-in session that claims that role, keeps a publish to a role whose holder is gone and hands it to that session when it claims the role again, refuses a `keep: false` publish to an unheld role, validates settled pull-request checks, lifecycle and comment topics, PR-less workflow and Actions-job behavior, obsolete-topic absence, and the direct-message summary/body contract `renderInbound` prints.

The NATS server is a process of this checkout's own build, stopped on exit with the listener and the two fake clients.

Evidence remains in `packages/envoy/out/e2e/` after the run:

- `envelopes.jsonl` captures the raw NATS frames.

- `session-frames.jsonl` captures the envelopes the session's own consumer delivered to it.

- `rendered-ts.txt` is every frame rendered by `@legion/envoy-client`'s `renderInbound`, the one inbound renderer.

The OS chooses the NATS and listener ports, and the run builds each URL from the port its own process reports.
`E2E_NATS_PORT` or `E2E_PORT` sets a port explicitly: it is used as given, and one already in use fails the run with `address already in use`.
The session opens no port of its own.
CI runs the driver as the `envoy-e2e-local` job of `.github/workflows/ci.yaml`.

## Contract source of truth

The authoritative event contract lives in `packages/contracts/`.
Run `bun run gen:go` in that package before validating or releasing `packages/envoy`.

Each `legion-envoy-v` release publishes the changelog entries its own tag first shipped:
this package's fragment files (`.changes/unreleased/`) are rendered into that GitHub release's notes.
`CHANGELOG.md` is the history before that and stops where the fragments begin.
