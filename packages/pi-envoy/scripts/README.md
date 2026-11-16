# Pi Envoy diagnostic scripts

## smoke-delivery.sh

Real end-to-end delivery smoke test against the *installed* pi-envoy
plugin. It launches a real, interactive `omp` session (TUI mode, never `-p`)
in a scratch tmux session and a throwaway directory outside any repo
checkout, so the only extension that loads is whatever is materialized at
`~/.omp/plugins/node_modules` — never a local source checkout via a repo's
own `omp.extensions` manifest. The session runs on the smoke's own tmux
server, on a socket in that throwaway directory, and the script prints the
`tmux -S <socket> attach -t <session>` line that watches it. It drives the
session with `tmux send-keys` to
call `envoy_role_set` and wait for a message, publishes to that role's
namespaced topic through the live Envoy HTTP API, asserts the session wrote
the exact payload to disk, then kills the session and asserts teardown: the
tmux session disappears and the role stops resolving (404) within 15 seconds.

### Usage

```bash
cd packages/pi-envoy/scripts && ./smoke-delivery.sh
```

Env overrides:

- `ENVOY_URL` — Envoy listener base URL (default `http://127.0.0.1:9020`).
- `OMP_BIN` — the `omp` binary to run (default `omp`, resolved on `PATH` —
  the dotfiles wrapper that injects provider auth).

Run from a host with:

- A live Envoy listener reachable at `ENVOY_URL`, with NATS behind it.
- The `pi-envoy` plugin installed for every `omp` session on the
  machine, materialized at
  `~/.omp/plugins/node_modules/@sjawhar/pi-envoy`. The script fails
  fast and names the path if that package.json is missing, is not valid
  JSON, or has no `.version` — the smoke is meaningless without the real
  installed artifact.
- A configured model provider: the script makes a real model call inside the
  spawned session, so it costs tokens and needs live credentials.

CI does not run this script — `.github/workflows/ci.yaml` has no
step for it, and none should be added while these prerequisites are unmet in
CI's sandbox.

## smoke-btw.sh

Real targeted Dispatch smoke against the installed plugin. It starts one isolated OMP TUI session
whose initial message (the prompt is `omp`'s argument, not typed keystrokes) asks for one bash
command that creates a sentinel file and then sleeps 45 seconds, waits for that session's exact
work directory to appear in the live listener registry and for the sentinel to exist, then targets
only that session with a message on an existing open Dispatch issue while the sleep is still
running. A correlated reply must appear on the same issue before the script tears down the session
and confirms its registry entry clears. It never targets a role or an existing session. The
spawned session is pointed at the same Dispatch the script talks to (`DISPATCH_URL`, plus a 0600
token file when the script holds a bearer), so its reply lands where the script looks for it.

### Usage

```bash
cd packages/pi-envoy/scripts && MODE=btw DISPATCH_SMOKE_ISSUE=CORE-1 ./smoke-btw.sh
```

Set `MODE=btw` (the default) for an ephemeral reply, or `MODE=steer` to prove the primary-turn
delivery path. `ENVOY_URL` defaults to `http://127.0.0.1:9020` and `OMP_BIN` defaults to `omp`.
`DISPATCH_SMOKE_ISSUE` names the open issue that will receive the smoke card.

Who asks: by default the script is an agent. It asks the Dispatch a command run from this shell
resolves, with that command's bearer
([`scripts/e2e/lib/operator-dispatch.sh`](../../../scripts/e2e/README.md#liboperator-dispatchsh),
which runs `bun` and needs the checkout's dependencies installed): `DISPATCH_URL`,
`DISPATCH_TOKEN_FILE` and `DISPATCH_TOKEN` over the `dispatch` section of the envoy.json
`envoyConfigPath("command")` chooses, whose token goes only to the server written beside it and
whose `!` token runs. It names itself in the message's `actor` as `SMOKE_ACTOR_SESSION` (default
`smoke-btw-<hex>`; pass your own Envoy session id when you run it from a session), so the card on
the issue is visibly agent-authored. With no Dispatch configured it refuses, saying why; any other
refusal of the client's (a file's token that `DISPATCH_URL` names another server for, a token file
that cannot be read) stops it naming the reason. Set `DISPATCH_AUTH_HEADER` to one
human-authenticated HTTP header (the current Dispatch cookie or a trusted identity header) to ask as
a human instead, exactly as the dashboard would: `actor` is then omitted, and the script asks
`DISPATCH_URL`, default `http://127.0.0.1:8766`.

The script requires live Envoy, Dispatch, and model credentials; it is never a fake-listener test or
a CI step. A 404 from the targeted message path prints `SKIP` and exits zero, meaning the server does
not support targeted messages. A `400 ACTOR_KIND` on a bearer run prints `SKIP` too, meaning
the server requires a human-authenticated targeted message; use a compatible server or pass
`DISPATCH_AUTH_HEADER`.

### Timeouts

Session registration and post-kill teardown bound infrastructure round trips only.
Delivery (90s) is generous by comparison because that window spans real Envoy delivery
plus the time for a live model turn to notice the targeted message and post the correlated
Dispatch reply. Every wait is a wall-clock deadline (not a fixed iteration count), so a
blackholed Envoy fails at its stated timeout instead of quietly stretching it.

### Exit codes

| Code | Meaning |
|------|---------|
| 0 | `PASS:` delivery, correlated reply, and teardown verified; or `SKIP:` server lacks this smoke path. |
| 1 | Any assertion failed — see the `FAIL:` line for which one, plus a captured pane or HTTP status. |

Cleanup (`tmux kill-session`, `rm -rf` the temp dir) runs in an exit trap on
every exit path, so a failed run leaves nothing behind either. Both smokes run
their tmux on a private socket in their temp dir, so the kill can end only the
session the smoke started; on the shared default server it could end anyone's.

## smoke-role-takeover.sh

Live proof that a role claim is weighed on the process that holds it: a second process that
resumed a live session's id can neither take, release nor outlast that session's role
(dispatch://INFRA-783), and a soft claim naming a predecessor takes the role only from a
predecessor that is gone or is the claimant's own client instance (dispatch://INFRA-404, R8 and
A5 of its spec); and once a session that holds a role restarts, the role lookup and the session
listing name the same holder (dispatch://INFRA-2747). It is the only smoke here that needs
neither a model provider nor a deployment: `go` and `bun` alone, so CI runs it as a step of the
`envoy-session-stall` job.

It builds natsd (`packages/envoy/internal/testnats/cmd/natsd`) and the listener
(`packages/envoy/cmd/listener`) from the checkout and runs both on loopback ports of its own, as
[`packages/envoy-client/test/envoy-stack.ts`](../../envoy-client/test/envoy-stack.ts) does, and
drives them with real client processes:

- one Bun process per Oh My Pi Envoy extension instance (`role-takeover/omp-client.ts`), each
  loading this checkout's `extensions/envoy.ts` against the stand-in host the package's own suite
  uses — one session manager whose id and branch the driver moves, and a heartbeat the driver
  ticks rather than a timer. One process is one client instance, with one client instance id;
- the real Claude Code channel server (`packages/claude-envoy/bin/envoy-channel.ts`), spawned and
  driven over MCP by `role-takeover/claude-client.ts`, which is also the parent process whose
  handoff file a `/clear` writes, so the server adopts the new session id on its own.

Every client reaches the listener through a recording proxy of its own
(`role-takeover/listener-proxy.ts`), so each request and the listener's answer to it is read from
a log rather than inferred.

The listener runs with windows of its own, because every row turns on whether a claim's holder is
still answering for it: `ENVOY_SESSION_LIVE_WINDOW=10s`, which is how long a claim stands renewed
after its holder's last registration, and `ENVOY_ROLE_CLAIM_REAP_INTERVAL=2s`, so every row is
swept several times. The one claim the reaper frees in the run is the one a hand-back marked:
no registration renews a marked claim, so it goes within one more reap interval of its renewal
lapsing, though the client that handed it back goes on registering that session id.
`ENVOY_SESSION_GONE_HORIZON` keeps its hour-long default all the same, since every unmarked
claim is kept while its holder's session reads short of `gone`, which is what leaves every
other row's holder in its role. The rows still read the listener's own word for each outcome — a claim
granted, a `409`, or the lookup's `404` — which reads the same whether a standing claim lapsed
where it stood or a pass had already removed it, and a row whose claimant must be refused is
decided inside the window, its holder having answered for the claim immediately before. A row
that waits the window out waits for the listener to say so, never for a sleep.

Eighteen rows are printed, each with its outcome:

- **row 1**, twice — a claim under the id that holds the role is granted because nobody renewed
  the standing claim inside the live window: an Oh My Pi plugin reload, and a Claude Code channel
  server restart, each under the session id that holds the role.

- **row 2** — an Oh My Pi fork gets its parent's role, because the parent's live entry carries the
  claimant's own client instance id.

- **row 3** — an Oh My Pi move, in one process, to a session whose records name the held role gets
  it, by the same case.

- **row 4** — a Claude Code `/clear` handoff gets the role: the deregistration removes the
  process's own entry, and the claim then finds no live predecessor.

- **INFRA-783**, four rows — a second process that resumed a live session's id is refused the role
  that session holds (`409` naming it, in Oh My Pi and in Claude Code); the Oh My Pi one is then
  granted the role once the holder stops renewing the claim and the window passes; and the
  process that no longer holds the claim hands the role topic back, which frees nothing.

- **row 5**, three rows — a claim naming a predecessor that is live inside another client
  instance is refused `409` naming it, in Oh My Pi (A5 scenario 1), in a move made by that same
  refused process, and in the Claude Code `/clear` handoff (A5 scenario 2). The live predecessor
  is built from two session ids, since a second process claiming under one id is now decided on
  the standing claim rather than on the predecessor it names.

- **A5**, two rows — a refused process stays registered, and after the holder exits the refused
  Oh My Pi process takes the role on its next session event.

- **INFRA-783**, three further rows — an older client's hand-back, which is a release of a
  role's own lane topic under the holder's session id stating no `client_instance_id`, frees
  nothing and marks the claim: the lookup answers `handed_back`, the real Oh My Pi holder reads
  that as a role it does not hold, claims it back at its next heartbeat and takes the lane's
  next message. The same hand-back against an old-build holder, driven by `curl` because a
  current client would take the role straight back, frees the role a live window and up to one
  reap interval later while
  that client is still registering. And a client that states no process at all — the shape every
  claim standing at a deploy is in — keeps its role across three live windows of registrations
  that carry no `client_instance_id`.

- **INFRA-2747**, one row — a session that holds a role restarts: its Oh My Pi process is
  killed, a new one starts under the same session id and is granted the role once the live
  window passes, and within 90 seconds of that process's first registration
  `GET /v1/roles/<role>` and `GET /v1/sessions?include=unresponsive` name the same holder, or
  both name none. The lookup reads the claim record and the listing the listener's copy of the
  role claims, so a copy that lost the claim shows as the holder listed with no role. The
  lookup's holder is read off its `200` and off its `404` (`holder_lapsed`) alike, since a
  holder nothing renews is lapsed ten seconds on and still holds the role, and a reading counts
  only where the lookup answered one of those and the listing `200`. The row prints how long
  after the first registration the two agreed.

### Usage

```bash
bash packages/pi-envoy/scripts/smoke-role-takeover.sh
bash packages/pi-envoy/scripts/smoke-role-takeover.sh --base-rev main@origin   # every row passes at b601d25b
bash packages/pi-envoy/scripts/smoke-role-takeover.sh --listener-bin <built listener>
SMOKE_DROP_ROLE_FROM_COPY=1 bash packages/pi-envoy/scripts/smoke-role-takeover.sh \
  --listener-bin <scratch build that serves the drop route>   # the INFRA-2747 row's control
```

`--base-rev <rev>` builds only the listener from that revision, leaving every client as it is in
the working copy. Against `main@origin` at `b601d25b` (2026-11-13) none of the eighteen rows
fail. The eleven of seventeen that once failed there were counted in cybermonklabs/legion#2875
(`277768d83e`, 2026-11-07) against the main of that day, which did not yet have the listener rule
those rows drive; main has carried that change since, so `--base-rev main@origin` is no longer a
negative control for them. The INFRA-2747 row passes there too: with nothing taken out of its
copy, that listener's copy names the restarted holder's role as the claim record does, and that
row's negative control is `SMOKE_DROP_ROLE_FROM_COPY=1` against a scratch build, below. It reads
the revision without touching the working copy (`jj --ignore-working-copy log` resolves it,
`git archive` extracts `packages/envoy`), moves no bookmark and writes nothing outside its
scratch.

`--listener-bin <path>` runs that built listener in place of building one, so a control can run a
scratch build; natsd is still built from the checkout. A path that is not an executable file is
refused with exit 2, and so is `--listener-bin` beside `--base-rev`, since each chooses the
listener.

`SMOKE_DROP_ROLE_FROM_COPY=1` turns the INFRA-2747 row into its own control
(c-live-restart of dispatch://INFRA-2747). Once the restarted process's claim is granted, the row
asks the listener to take the role out of its copy of the role claims
(`POST /debug/role-copy/drop?role=<role>`) and fails, saying so, on any answer that is not a
`2xx`, so a listener without that route never passes the control by doing nothing. It then reads
the listing once and requires that the drop took the role out of it, sends the process no
heartbeat, and requires at the end that the process sent no registration and no claim, so
nothing but the listener's own repair can put the role back in the copy. The comparison starts
once the drop has answered and keeps the same window. Only a scratch build serves the route: a
patch that adds it is applied to a `git archive` of the revision under `$TMPDIR`, built there with
`go build ./cmd/listener`, and passed with `--listener-bin`. The patch is never committed. A
listener that repairs its copy within the window passes the row; one that does not lists the
holder with no role until the window ends, and the row fails naming the two answers it last read.

Prerequisites, each checked before anything starts: `bash` 5 or later, `go`, `bun` with the
checkout's dependencies installed, `curl`, `jq`, and `jj`, `git` and `tar` for `--base-rev`.
Everything the run writes — the binaries it builds, the JetStream store, each client's home and
state directory, every proxy's request log — lives in one `mktemp -d` under `$TMPDIR`, which an
exit trap removes after a pass and keeps after anything else. Nothing it starts inherits this
shell's environment (`env -i` plus `PATH` and what the script sets), so an operator's `ENVOY_*`,
`NATS_*` or `DISPATCH_*` variables never reach a client. A local run takes about two minutes, and
about four with `--base-rev`.

### Exit codes

| Code | Meaning |
|------|---------|
| 0 | `SMOKE ROLE TAKEOVER PASS:` every row held. |
| 1 | `SMOKE ROLE TAKEOVER FAIL:` the rows that did not hold are listed, each with the check that failed and what it saw. |
| 2 | Refused: a prerequisite is missing, the configuration is invalid, or a client or service did not start. |
| 129, 130, 143 | Interrupted by SIGHUP, SIGINT or SIGTERM; cleanup still ran. |

## smoke-registration-stall.sh

Manual live smoke for the registration-stall fix (dispatch://INFRA-246): a real headless `omp`
pane against a real Envoy listener, watched through the live registry. `--arm stall` shows that a
pane whose registration post is wedged forever keeps refreshing past one record lifetime and says
so exactly once; `--arm recover` shows that a pane unregistered while its process runs
re-registers itself. `--control` is each arm's negative control (the stall control needs
`--base-rev <rev>`, the plugin as it was then).

```bash
# against a deployment
ENVOY_URL=<listener> ENVOY_TOKEN_FILE=<0600 file> \
  bash packages/pi-envoy/scripts/smoke-registration-stall.sh --arm recover
# against a listener this run starts itself
bash packages/pi-envoy/scripts/smoke-registration-stall.sh --arm recover --local-stack
```

`--local-stack` builds natsd and the listener from the checkout and runs both on loopback ports of
its own, as `smoke-role-takeover.sh` does, so the run needs no deployment and no bearer: it ignores
`ENVOY_URL` and `ENVOY_TOKEN_FILE`, says so, and keeps both out of the pane it spawns. Its one
remaining live dependency is the model provider the pane's turn needs, which the pane inherits
from the caller's environment. `--heartbeat-ms <n>` shortens the pane's refresh interval, which
every budget the run prints is measured in. Its scratch is a `mktemp -d` under `$TMPDIR` that an
exit trap removes. Exit 0 is `SMOKE <arm> PASS:`, 1 is `SMOKE <arm> FAIL:` naming the observation
that failed, and 2 is a refusal.

## local-stack.sh

The loopback Envoy stack the two smokes above bring up, in one sourced file: a free loopback
port below the kernel's ephemeral range (`port_range`, `port_open`, `pick_port`), the natsd and
listener builds (`build_natsd`, `build_listener`, each taking the `packages/envoy` directory to
build from, which is how `smoke-role-takeover.sh --base-rev` builds the listener from another
revision), the argv and assignments each service is run with (`set_natsd_argv`, `set_listener_env`)
and when it is up (`natsd_serving`, `listener_healthy`, polled with `poll_until`). It is sourced,
never run: it starts no process, so every script keeps its own isolation, logs, traps, timeouts,
messages and exit codes. A caller defines `refuse` and sets `local_stack_fail` to the function an
exhausted port range is reported through, `refuse` in both smokes here.
`packages/envoy-client/test/envoy-stack.ts` is the same stack for the TypeScript suite.

## listener-api.sh

The listener's own HTTP surface, in the second sourced file `smoke-role-takeover.sh` brings in
beside `local-stack.sh`: one raw call either way (`listener_get`, `listener_post`), the three
requests a client built before dispatch://INFRA-783 sends, which that smoke drives by `curl`
because no client in the checkout can be made to send them (`old_register`, `old_claim`,
`old_handback`), and the readers every row states its outcome in — one field of
`GET /v1/roles/<role>` (`role_field`, and `role_holder`, `role_instance`, `role_handed_back`,
`role_reason`, `role_unrenewed` over it) and the registry's answer for a session (`registered`,
`unregistered`). It is sourced, never run, and reads three variables the caller sets:
`envoy_url`, `work` and `curl_max_time`.

## role-takeover/clients.sh

The clients themselves, in the third sourced file `smoke-role-takeover.sh` brings in, beside the
Bun drivers it starts: a recording proxy and a process per client (`start_proxy`, `start_omp`,
`start_claude`), the stdin and stdout one command is sent and answered over (`client_fifo`,
`client_ready`, `send`), the readers of each client's proxy log — the claims it made
(`claim_status`, `claim_holder`, `first_claim_status`, `first_claim_holder`, `made_claim`,
`claim_count`), the registrations it sent (`instance_id`, `registered_at`,
`first_registered_at`), the requests of either kind (`claim_writes`), its deregistration
(`delete_answer`) and its hand-backs (`release_status`, `release_instance`, `released`) — and the
two ways a row makes a holder answer for its claim (`renew_omp`, `renew_claude`). It is sourced,
never run: it starts no process of its own, and it reads the scratch directory, the driver
directory, the checkout, the listener and natsd URLs, the Oh My Pi client's windows, the two
client timeouts and the run id that the smoke sets.

## skill-scenarios/rig.sh

Replays skill scenarios on real agents, to compare two versions of a skill. Each label is one
checkout's packed plugin in an isolated Oh My Pi home (`rig.sh profile <label> <checkout>`); each
run is one fresh `omp -p` agent given one task a skill rule governs, scored afterwards from what the
stand-ins and the scratch Dispatch recorded (`rig.sh score`). `rig.sh live-read <label> <skill>...`
has one session read every file of a skill and reports whether each arrived whole; it writes
`$SKILL_SCENARIOS_WORK/live-read/<label>` and refuses a label whose earlier live read is still there.

```bash
cd packages/pi-legion/scripts/skill-scenarios
LEGION_E2E_MODEL_GATEWAY_URL=<gateway>/anthropic ./rig.sh profile head <checkout>
# or, with no gateway: LEGION_E2E_MODEL_ROUTE=key-file LEGION_E2E_ANTHROPIC_KEY_FILE=<0600 file>
# or, through a provider block of your own models.yml whose apiKey is `!cat <absolute file>`:
#   LEGION_E2E_MODEL_ROUTE=provider LEGION_E2E_MODEL_PROVIDER_FILE=<models.yml> LEGION_E2E_MODEL_PROVIDER=<block>
./rig.sh batch ask-on-message 5 head base   # starts and stops Postgres, NATS, one listener per label, and each run's own scratch Dispatch
./rig.sh batch tester-proof 5 head base
./rig.sh score
```

A batch numbers each label's runs from one above that label's highest run of the scenario (and
case), live or archived, so `rig.sh batch ask-on-message 1 head` twice gives the runs named
`ask-on-message-head-1` and then `ask-on-message-head-2`. A run's name is in its record and
nowhere its agents can read: its directory is `runs/<id>`, `<id>` twelve hex digits of no meaning,
and its record is `runs/<id>.json` beside it, outside the run's namespace:

```json
{"run":"<id>","scenario":"threads-closed","case":"text-fixes","label":"head","n":1,
 "name":"threads-closed-text-fixes-head-1","batch":"<tag>-<start>","ran":"<work>/runs/<id>"}
```

`case` is `""` for a scenario that takes none, `mode` is there for an askgate run given one, and
`ran` is the directory the run had while it ran, which its transcripts name. The record moves with
the run to `archive/runs/<id>.json`. `rig.sh score`, `rig.sh health`, `rig.sh judge` and
`rig.sh world` read a run's scenario, case and label from it (`run-names.ts`'s `runIdentity`), and
a run recorded before the record existed from its directory's name and its `case.txt`, as before.
The labels' profiles are installed under `installs/<id>` (`profiles/<label>/install` holds the id),
and every label's profile is named `legion`, so neither a path nor `OMP_PROFILE` names a label.

The spec-writing and spec-rewrite scenarios measure how an agent writes a Dispatch spec:

```bash
bun fetch-spec-sources.ts --out "$SKILL_SCENARIOS_WORK/sources" <issue key | dispatch://PROJECT/artifact/slug>
SKILL_SCENARIOS_SUPERPOWERS=<superpowers plugin> ./rig.sh profile main <checkout>
SKILL_SCENARIOS_SOURCE=<directory under sources/> ./rig.sh batch spec-writing 1 main
./rig.sh judge "$SKILL_SCENARIOS_WORK"/archive/runs/<id>   # the run whose record names spec-writing-main-<n>
./rig.sh score spec-writing
```

The scenarios and every input are in the script's header (`./rig.sh` with no arguments prints it),
and each scenario's scoring rule is on the `score.ts` function that scores it. It needs a model
route (the gateway, an Anthropic key file, or a provider block of the operator's own models.yml)
and Docker; every container a batch or a judge starts has CPU and memory limits.
`SKILL_SCENARIOS_WORK` must have no `.omp` in it or above it, so not under your home directory: Oh
My Pi would load that directory's plugins and not the label's. Its bounded waits, port picks and
process stops are
[`scripts/e2e/lib/rig.sh`](../../../scripts/e2e/README.md#librigsh)'s. Nothing a batch runs reaches
production Dispatch, GitHub, Envoy or NATS: it starts nothing with the caller's `DISPATCH_*`,
`ENVOY_*` or `NATS_*` variables, and `seed.ts` refuses to run while `PLAYWRIGHT_BASE_URL` or
`E2E_AGENT_TOKEN` is set. That is the environment only: an agent reads the whole filesystem, the
other label's checkout included, so `score.ts` does not score a run that read outside its own
label. A judge is held tighter: `rig.sh judge-omp` runs each one in a Docker container that sees
only its own directory, read-only but for its one answer file and its transcript, so a judge
cannot read the run's transcript or another judge's input. On the key-file route the container
reads the key file itself. On the gateway route the gateway's key command runs on the host, once
for each judge, because it needs the operator's home and session bus. The container gets only the
token, from an owner-only file that is removed when the judge ends. On the provider route the
profile's models.yml is the block as the operator wrote it, `!cat` command included, so no key is
copied anywhere: the container mounts the directory of the file that command reads, read-only.

Each run's agent runs in a user, mount and PID namespace of its own (`env -i
PATH=/usr/sbin:/usr/bin:/sbin:/bin unshare -Urm -p --fork --mount-proc`, then a user namespace that
maps it back to the caller's uid), built from one declaration in `rig.sh`, `world_paths`, which
`rig.sh world <label> <run dir>` prints. It hides behind an empty tmpfs the scratch root the
caller's `TMPDIR` names, the issue workspaces (`/legion/workspaces`, or `LEGION_WORKSPACE` three
levels up), the work directory, every label's checkout, `/var/run/legion` and `/var/run/secrets`;
it shows again the run's own directory, its label's install (never the other label's), the
batch's two tokens (`services/tokens/`), and the tools the run's pane names (each directory on its
PATH, mise's installs, the pin and the push guard's check), read-only but for the run and its
model route's call record. No checkout is shown, the label's own included, and not the rig's own
directory either: the run's `gh` is a bundle of `gh-standin.ts` in `<run>/lib/gh.js`, which names
the run it records under itself. `/tmp` is the run's own `<run>/tmp`, and `/proc` lists the run's
own processes alone. `/var/run/model-gateway-key` and `/var/run/operator` stay: they are the model
route's own credentials. So an agent can neither list nor read another run, another role's
scratch, the other label's skills or the pod's credentials, nor leave a file in `/tmp` for another
run. A run recorded before this world is read against the world it had (its own `namespace.txt`,
or, with none, a world that showed its checkout, the rig's directory and the services).

Nothing the rig composes for an agent names its run's scenario, case or label, or a name the rig
once gave its world: before an agent starts, `launch` runs `run-names.ts guard`, which reads every
variable of the pane (its name and its value: the profile, the Legion project, issue and role its
Envoy topics are made of, HOME, TMPDIR, PATH), every mount of the namespace (each is a mount point
and the host source `/proc/self/mountinfo` names, so the work directory's own path is held too),
the working and session directories, the prompt and system prompt (`/proc/1`'s command line), and
the files the rig writes for each launch (`run-names.ts`'s `COMPOSED`: `bin/gh`, `bin/bun`, the
`gh` bundle `lib/gh.js` and `jj.toml`), each read whole, and the run fails, naming the word and the
place with the text around it, where one is there. It reads nothing else the world shows:

- not the label's install (`installs/<id>`: its packed plugins, HOME and model route), a
  checkout's production artifact whose words are production's, which a production agent reads
  too: several scenario words are ordinary words of this product, and the preflight at `cc09c638`
  refused every base agent on `SPEC_WRITING_POINTER` in pi-legion's `dist/legion.js`, while the
  head's passed only because its `legion.js` was over a size bound;
- not what the run has written (`sessions/`, `out*.txt`, `cwd/`, `ws/`, `calls.jsonl`, a HOME an
  agent used), which holds its agents' own words: read before a run's second agent, the first
  agent's transcript refused it;
- not the world's repositories and gh answers (`world.json`, `fixtures.json`, `src/`, `ws/`,
  `remote.git`), which the run writes into after its first agent, and which `rig.test.ts`'s "what
  the rig writes into a run's world" holds to the same words for every world `rig.sh` builds.

The services' logs (`<run>/logs/`) are hidden from the agent under an empty read-only tmpfs. The
rig's records that are files at the top of the run's directory (`built-from`, `services-from`,
`namespace.txt`, `launch.sh` and the rest) are not hidden, since a gVisor pod refuses a mount onto
a file, and the guard does not read them, since `services-from` names the rig's own directory in
every run: an agent can still read them, and hiding them needs them moved into a directory the
namespace covers. The words are derived, never typed: every scenario and case of `run-names.ts`'s
`SCENARIOS`, the rig's one scenario table, every label, and `REMOVED_NAMES` (`rig-`,
`skill-scenario`, `lweval`, `e2e-seed`, `example/widgets`). So the work directory's default is
`$TMPDIR/work`, the world's repository is `fernbrook/widgets`, its Dispatch project is `WID`, the
seeded sessions have ids shaped as any role's session, and the Legion project is the repository's
name and a digest. What stays a rig path: the work directory's layout (`<work>/runs/<id>/home`,
`<work>/installs/<id>`) where a Legion worker has `/home/<user>`, `/legion/workspaces/<owner>/
<repo>/<key>` and the issue volume's `TMPDIR`. Moving them there would have `rig.sh` rewrite the
pane the label's daemon composes, `health.ts`'s world and credentials rules accept those paths
under the covers they lie in, and `score.ts` read them as the run's own; that is a change of its
own, so the paths are neutral in their words and not production's in their shape.

A host that refuses the namespace starts
nothing: `rig.sh run`, `batch` and `live-read` probe it first and exit 1 naming the host's refusal
(a GitHub runner answers `unshare: write failed /proc/self/uid_map: Operation not permitted`), and
the rig's namespace tests skip there with that text. When a run ends, the rig moves its directory
from `$SKILL_SCENARIOS_WORK/runs/<id>` to `$SKILL_SCENARIOS_WORK/archive/runs/<id>`, its record
with it;
`rig.sh score` reads both directories, and `rig.sh judge` and `rig.sh health` take a run in
either. No run removes or replaces another: a run whose name a live or archived run's record (or,
for a run from before the records, its directory) holds is refused before it starts, and a run
whose id the archive took while it ran is refused at its end, naming both paths, and stays in
`runs/`. The rig deletes only what the same run or command
made, and refuses a file an earlier one left where it would write: a batch refuses the services
files a batch killed outright left (each label's `services/env-<label>` and
`services/services-from-<label>`, its tokens under `services/tokens/` and each listener's
`listener_<label>.port`) and
removes only the containers it started; `judge-omp` refuses a directory
holding an earlier judge's `out.txt` or `judge-model.txt`, or the key directory beside it; a
judge-profile build an earlier call left unready is refused until removed; `rig.sh judge`
(`judge.ts`) refuses, before any judge starts, a run whose judge directories under
`$SKILL_SCENARIOS_WORK/judging/<run>/` or whose answer files (`answers-<turn>.jsonl`, and
`faults.json` to `shared-runs.json`) an earlier judging left, naming each, so a run is judged
again once those are moved aside; and `seed.ts capture-spec` refuses a run directory that already
holds what it writes (`spec.md`, `plan.md`, `documents/` and the rest). The one exception is the
gateway key command's cached token, which a judge drops to mint one that outlives it. At
`SKILL_SCENARIOS_PARALLEL=1` a run's exit also records what appeared at the top of the machine's
`/tmp` while it ran, by the entries it recorded there when it started, and deletes none of it. A
run's pushes meet the push guard a Legion pod's do: where the `git` on the
caller's PATH is Legion's leak-check wrapper, the rig passes the caller's `PUBLIC_LEAK_CHECK` into
each run's pane and refuses to start a run when it names no readable check.

A phase worker whose claim boot is refused before its first turn is launched once more, in the
same run, as production's daemon relaunches an agent whose process exits at its boot: the daemon
stand-in refused its `claims/register`, or its plugin printed its sentence for a claim boot that
registered and could not finish and exited, read in the wording of the run's own label: the
opening is read out of `packages/pi-shared/src/legion-claim-core.ts` in the checkout the run's
`built-from` names (this checkout's `claim boot failed at <step> …`, an older plugin's, main
`ff2a10ef`'s, `claim boot failed after claims/register registered …`). Round 23 lost
`reviewer-text-findings-text-only-base-1` so, to the Envoy listener's refusal of a session that
held no consumer. `boot_agent` runs `boot-refusal.ts` itself to decide it from what the run
recorded (`health.ts --boot-refusal` answers the same, for a replay through the old entry);
`boot-refusal.ts` is the one reader of a claim boot refusal, and loads neither `health.ts` nor
`score.ts`: the stand-in's lines of that role since the launch began, the pane's `out.txt`, and
the agent's transcripts, which must hold no turn, no model call and no tool call. The first
attempt is kept whole under `<run>/boot-1/<role>/` (its `out.txt`, its `sessions/` and the
stand-in's lines of it), and the second attempt's record stands where any agent's does, so
`rig.sh score` and `rig.sh health` read the run on it. The stand-in, its boot token and the
agent's HOME carry over, as a pod's do. The rig relaunches once, where the daemon's launch budget
relaunches an agent up to
its launch failure limit: a second refusal stands, and one after the agent's first turn is never
relaunched, since a relaunch would not take that turn back. Each attempt is on the run's boot
record, `<run>/booted`, before it starts: worker_boot writes `boot <role>` before the role's daemon
stand-in starts and the relaunch writes it again, each launch `boot_agent` asks the reader about
is followed by `since <role> <time>`, the `since` it asked with, so a replay asks the reader with
the call's own argument, and where the rig decides not to boot a role of its scenario it writes
`skip <role> <reason>`, with the one reason `completion-answers.ts`'s `SKIPS` declares for the
scenario and case (today, threads-closed code-defect's reviewer: "the implementer sent the
reviewer nothing"), read from that table.

`rig.sh health [--tmp-snapshot <file>] <run dir>...` says whether each run's world could take what
its agents did, and whether the run stayed inside its own world. It reads seventeen things: the run
finished: its boot record, `<run>/booted`, decides each role its scenario boots
(`completion-answers.ts`'s `ANSWERS` `boots`) once, in that order, the first a `boot` of the first
role and each other a `boot <role>` or a `skip <role> <reason>` whose reason
`completion-answers.ts`'s `SKIPS` declares for the run's scenario and case (a relaunch's repeated
`boot` line is one decision), and each role it booted ended on its own record: its own output ends
with an `exit=` line (`out-<role>.txt`, or `out.txt` for the last role booted) and its own session
directory (`sessions/` for the first role, `sessions/<role>/` for each later one, a subagent's
transcript never) holds a transcript with an entry; a run with no `booted` is unhealthy; the run
carries its `services-from`, and every service of its label's own (scope `label`) came from the
checkout its `built-from` names, the listener, the Dispatch server, the daemon facts and the
worker pane among them (below); no agent's claim boot was
refused but a first attempt's before its first turn, which the rig relaunched (a run refused on
both attempts is unhealthy, naming both, and so is one refused after a turn); every completion
line names a role the scenario boots, and every completion the daemon stand-in refused was
answered as `completion-answers.ts`'s `ANSWERS` table allows for the run's scenario and that
role: an answer a daemon of either shape gives it, or one the daemon of the run's own label gives
it, read out of the checkout the run's `built-from` names (below; a run with such an answer
whose daemon cannot be read is unhealthy); a
reviewer-text-findings world took the reviewer's completion at least once; no request the daemon
stand-in answered 404 is of a route the run's label's daemon registers, read out of the source of
the checkout the run's `built-from` names (the stand-in answers by the same routes, and a run with
a 404 whose routes cannot be read is unhealthy); no turn outcome the stand-in answered failed the
claim, as the daemon fails one whose provider refused turns up to its launch failure limit; no
tool result of any
agent, and no `out*.txt`, holds a line the push guard prints when it refuses a push or cannot run;
no `gh` call the gh stand-in refused (`calls.jsonl`) with a refusal `health.ts`'s `GH_DECLARED`
does not declare for the scenario and the calling role (a call no route of the world answers
never is; the refusals production gives for a workflow run the world holds none of, GitHub's 404
for its jobs and gh's for `gh run view` and `gh run watch`, are, for every role, and so is the
Legion gh wrapper's refusal, exit 2, of a call naming a repository whose owner is outside the
project; the world answers
every `gh` read either label's role texts name, which `gh-routes.ts`'s `WORLD_ROUTES` lists and the
rig's tests pin against both checkouts); no request of an agent that the run's own Dispatch server
answered with an error, 4xx or 5xx, by the server's own line of it in `<run>/logs/dispatch.log`,
unless `health.ts`'s `DISPATCH_DECLARED` declares that answer for the scenario and the agent's role
(the agent booted last before the request began, by the opening line each daemon stand-in writes
before its agent starts; `HANDOFF_NOT_FOUND` for the `plan` handoff, which no world records, is
declared for every role, and for the `review` handoff for reviewer-text-findings' reviewer; a run
with no such log is unhealthy); no tool call names a path under another run's directory;
no tool call reads a path another of the runs given wrote last before the read, or, for a run from
before the rig gave each run a namespace (one with no `<run>/namespace.txt`), a `/tmp` entry that
was there before the run (its `scratch-before.txt`, or the `--tmp-snapshot` file of `/tmp` entries
taken before the
batch); no tool call names a path its world hides (by `rig.sh world`: under the scratch root, the
issue workspaces, the work directory or any label's checkout, the other runs' given included)
outside its own directory, `/tmp` and what its world shows again; no tool call reads another
process's `/proc/<n>/environ` or `cmdline`; no tool call names a path under `/var/run/legion` or
`/var/run/secrets`, by either name; no call reaches a host other than the loopback, the hosts
its pane's URLs name and the model gateway its profile's `models.yml` names, but a GET or a HEAD
with no body, or a `git ls-remote` (which reads refs alone; a fetch, a clone and a push stay off the
world),
of a public repository the world's own workflow names in a `uses:` line (read from the run's
remote at `world.json`'s seeded head) on `api.github.com`, `github.com`,
`raw.githubusercontent.com` or `codeload.github.com`, as production reaches it; and no agent says a
word of the measurement an input gave it: a scenario or case word with a character that is no
letter or digit (`text-only`, `threads-closed`), a removed name (`skill-scenarios`, `LWEVAL`,
`rig-reviewer`), a run's name in the old shape, or a case word beside a label word (`defect
head`), by the same derived words as the guard's (`run-names.ts`'s `proseNames`; a plain word
alone is the work's own: `head SHA`, `base branch`, `not a defect`),
in its own text or thinking, where its prompt, its system prompt or an entry of a transcript that
is not an agent's own (a user message, a tool result, a message it was sent, an entry Oh My Pi put
in its context) held the word before any agent of the run wrote it, its tool calls' arguments
included: earlier in the same transcript, or earlier by the entries' times across two, so a
message one agent sends another carries the sender's own word. The unhealthy line quotes the
sentence and names the input. A word the run's agents wrote before any input held it is theirs:
`health: names its agents wrote before any input held them (no verdict): <n> of <m> runs: <run>
(<sentence>); …` names each such run and its first sentence, and the run stays healthy, since the
preflight at `cc09c638` found "text-only" in three head runs where no skill file, user message or
tool result held it first. A URL is text until
something runs it: every `web_search` reaches off the world but one about the world's own
workflow, as a production reviewer may search for an action it pins: its query names a repository
the workflow's `uses:` lines name (its name ending where a name's characters end, so
`actions/checkout-evil` and `actions/checkout.js` are others) or a commit they pin, in full or by
a word of 7 or more of its leading hexadecimal digits, and holds no word of the world's own (its
repository's full name and each half, its issue's key and project, its issue branch, read from
`world.json`; not the base branch) and no word of the measurement as the names rule reads one
(`proseNames`); a `bash` command, a `read`, a
`fetch`, an `eval` cell's code, a here-document an interpreter reads on stdin and a file the run
wrote and then ran are read for the hosts they reach; an `eval` cell's `tool.<name>(…)` call is
read as a direct call of that tool would be; text a call writes (a `--body`, a commit message, a
message to Envoy, a file never run) reaches nothing; and a tool nobody named has every argument
read. A `bash` command is read by each program's own grammar, in the places that take a host: a
remote in scp form (`[user@]host:path`) only where `git`, `jj`, `scp`, `rsync` or `sftp` take an
address, so `git show HEAD:docs/cli.md` and `jj file show main@origin:docs/cli.md` reach nothing;
the host operands of `ssh`, `scp`, `sftp`, `rsync`, `nc`, `ncat` and `telnet`, and the ssh
options that name one (`-J`, `-o ProxyJump`, `-o HostName`); the host `gh`'s `--hostname`,
`-R HOST/OWNER/REPO` (or its `https://` URL, which the gh rule reads in place of the URL rule) or
`GH_HOST` names, in the world only for `github.com`, which the run's gh stand-in plays and
answers as it answers the bare call, and the loopback; and a `gh` named by a path, or run as
`legion gh`, that is not the run's own stand-in (`<run>/bin/gh`, which a bare `gh` resolves to),
whatever it names. A wrapper (`sudo`, `env`, `timeout`, `nice`, `nohup`, `xargs`, `command`,
`exec`, `time`, `sh -c`) reaches nothing of its own: the command it runs is read as if it stood
alone. A run's own writes outside its directory are recorded and are no verdict on
it: `health: wrote outside its own directory, HOME and TMPDIR (no verdict): <n> of <m> runs` goes
on, where <n> is not 0, with each such run's name and the paths it wrote
(`: <run> (<path>, <path>, <path> and <k> more); <run> (…)`, the first three paths of a run and a
count of the rest), so the line says which runs they were and where each wrote. A path under
`/tmp` is read as the run's namespace mounts it, by the same `rig.sh world`: it is the run's own
`<run>/tmp`, and a write to it is none outside the run, unless the run's world shows a path there
(its own directory, its label's profile and the batch's services, on a host whose work directory
is under `/tmp`), where it is that path. The two rules that read another run read `/tmp` the same
way, so a run in a namespace is held to nothing another run's `/tmp`, or the machine's, held; a
run with no `<run>/namespace.txt` wrote and read the machine's `/tmp`, and is read so.
Give a batch's runs in one call: given one run, `health` sees no other run's writes. It reads
every transcript under each run's `sessions/`, every role's and every subagent's. It scores nothing
and writes nothing: it prints `<run>  healthy` or `<run>  unhealthy: <reason>` for each run, keyed
by the run's directory (its id), with the record's name last for a run with a record (`<id>
healthy  (threads-closed-text-fixes-head-1)`), naming
the evidence, then how many runs each rule flags, then how often the rig relaunched an agent at
its boot, by label (`health: boot relaunches, <label>: <n> in <r> of <m> runs`: <n> relaunches,
each a first attempt kept under `<run>/boot-1/<role>/`, in <r> of the label's <m> runs), then the
runs that wrote outside their own directory, then a count. It exits 0 when every run is
healthy, 1 when one is not, and 2, printing no run, for no argument or for a path that is not a
run of a scenario the table names. Start a batch only once a run of each label in each
scenario case is healthy.

A rule that matches text a label's own world prints reads only what both labels print alike, or
reads the run's own label: tester-proof's PR body check line is read up to `at <commit>, recorded
by tester.` and its commit taken from that `at` alone, since an older Dispatch's, main
`ff2a10ef`'s, ends the line with the check's own words and this checkout's with the reading the
tester recorded; the claim boot sentence is read as the relaunch paragraph above says, by
`rig.sh score` and `rig.sh health` alike.

Two tools read production Dispatch, with GET requests only, as the calling session does (its
envoy.json, or `DISPATCH_URL` with a token): `fetch-spec-sources.ts`, which writes a source's input
into the work directory for the spec scenarios, and `label-comments.ts`, which labels the comments
people wrote in a window and prints the share that complain about writing. Neither writes inside a
checkout. Text either one reads from production stays in the work directory and is never committed.
Neither removes or replaces what an earlier run wrote: `fetch-spec-sources.ts` exits 2 before any
read when a source directory it would write, or `agent-texts.json`, is already under `--out`, and
`label-comments.ts` exits 2 when `labels.jsonl` is; each names the path, to be moved aside.

### Where each service of a run comes from

A label's agents load the plugins of the label's own checkout, so every service that speaks to
those plugins comes from that checkout too, as one deploy pairs them in production. The first
exception found was the Envoy listener, which a batch built once from the rig's own checkout: a
base run, then built from main `ff2a10ef`, paired its pi-envoy (from before #2603) with a
listener from after it, a pairing no deploy ever had, and lost its claim boot to the listener's
`409 not_requested`. A batch now builds one listener per label, in that label's checkout, and
starts each on a port and a machine id of its own (a listener's durable NATS consumer is named for
its machine). The Dispatch
server was the second: what an agent reads of it is not the same across versions. `ff2a10ef`'s
`dispatch result` prints a result's pull-request line ending with the check's text where this
checkout's ends with the role's reading (`packages/envoy/internal/dispatch/api/results.go`,
`checkResultLine`), and `ff2a10ef`'s server refuses a body field it does not declare (`400
INVALID_JSON`, `api/server.go`'s `DisallowUnknownFields`) where this checkout's ignores it. So each
run starts its own Dispatch from its label's build. A service production shares across versions,
and the rig's own instrument, stays shared, and the table says why.

| Service | Came from before | Comes from now | Scope, and why |
| --- | --- | --- | --- |
| Envoy listener (`envoy-listener`) | `go build ./cmd/listener` in the rig's own checkout, one listener for every label | `go build ./cmd/listener` in the label's checkout's `packages/envoy`, kept at `$SKILL_SCENARIOS_WORK/builds/<checkout>/envoy-listener`; one listener per label | label: production runs a machine's listener from the release its plugins come from |
| Packed plugins (pi-envoy with its `dispatch` CLI, pi-legion) | the label's checkout (`rig.sh profile`, `install-plugin-profile.sh`) | the same | label |
| Model route (the installer that writes the profile's `models.yml`) | the label's checkout's `scripts/e2e/lib/install-*.sh`; the provider route is the rig's `install_provider_route` | the same | label for the installer; the gateway or endpoint it names is shared, since every version's agents reach one gateway |
| Oh My Pi | the pin the label's checkout's `.omp-pin` names, from mise | the same | pin |
| Daemon facts (routes, how the daemon takes a completion, phase table, refusals, launch failure limit, the selector table of its gh wrapper) | the label's checkout's `packages/daemon`, which the run's `built-from` names, read at each stand-in's boot by the rig's `daemon-pane.go` (`go run` in that module, a compile of it per fact) and `daemon-routes.go`; how the daemon takes a completion was a table keyed by the label's name; which owner the gh stand-in refused was a reader of its own, of three selector forms | the same sources, read once a batch before any run starts by `daemon-pane.go` built in the label's checkout's `packages/daemon` (`$SKILL_SCENARIOS_WORK/builds/<checkout>/daemon-pane`), `daemon-routes.go`, `daemon-completion.go` and `gh-selectors.go` into `$SKILL_SCENARIOS_WORK/daemon-facts`, which each boot reads; the gh selectors are written into each worker run as `<run>/gh-selectors.json` | label for the facts and the build; the four readers' source is the rig's instrument |
| Worker pane (`worker-pane.ts`, then `daemon-pane.go pane`) | composed by the label's checkout's daemon module (`go run`); the composer, and the `@legion/envoy-client` settings reader it calls, are the rig's | the same, composed by the label's build of `daemon-pane.go` | label |
| Daemon stand-in (`daemon-standin.ts`) | the rig's directory, answering by the label's daemon facts | the same | shared program: it stands in for the label's daemon from that daemon's own facts |
| Dispatch server (`dispatch-e2e`) | the rig's checkout, built once per batch and given to the rig's `run-server.sh` as `DISPATCH_E2E_SERVER_BIN` for every label | `go build ./cmd/dispatch` in the label's checkout's `packages/envoy`, kept at `$SKILL_SCENARIOS_WORK/builds/<checkout>/dispatch-e2e`; each run's own Dispatch is started from its label's build by the rig's `run-server.sh` | label: what an agent reads of it differs across versions (above); `run-server.sh`, the harness that starts a build, is the rig's, and hands a build of either tree the same environment |
| gh and bun stand-ins (`gh-standin.ts`, bundled into `<run>/lib/gh.js`, and `$R/bin/bun`) | the rig's directory, and the bun the rig runs on | the same | shared: GitHub is one service for every version |
| GitHub API stand-in (`github-standin.ts`, its routes in `github-routes.ts`), the GitHub each run's Dispatch server reads as `DISPATCH_GITHUB_API_BASE` | none: the server's GitHub was port 1, where nothing listens, so every read failed and a reviewer was told a pass stands "not known", Dispatch "cannot read the repository" | the rig's directory, one process per run, which `start_dispatch` starts before the run's Dispatch and `stop_run` stops; it answers the routes the world's flows were measured to read (`GITHUB_ROUTES`) from the run's own remote and refuses any other, logging each request to `<run>/logs/github.log` | shared program, per run: GitHub is one service for every version, as for the gh stand-in, and each run's GitHub is that run's world |
| Head watcher (`head-watcher.ts`), the push event each run's Dispatch server hears | none: the server learned no head from a push, since it runs no NATS intake and its read-through asks GitHub only for the commits of results that stand at no head (`results_comparison.go`, `learn` → `readThroughHead`), so after the implementer's push `dispatch results` compared with the approved head and said a pass stands; 3 of 20 recorded text-fixes implementers (4 calls, all base) read exactly that | the rig's directory, one process per worker run, which `start_heads` starts outside the agent's namespace once the issue is seeded and `stop_run` stops; on each push to `legion/<key>` that `<run>/pushes.log` records it stores the pushed head as Dispatch's intake stores it (the write of `seed.ts pull-request`), and logs one line per push to `<run>/logs/heads.log`, `{at, ref, pushed, ok, error?}` after an opening `{at, opened}`, which health's `head` rule holds every push to; the push itself cannot, since the remote's hook runs inside the agent's namespace, which shows neither the rig nor the run's database URL | shared program, per run: the instrument's stand-in for production's `pull_request` intake, which the world's Dispatch does not run |
| Seed, capture and the boot and world readers (`seed.ts`, `record-publishes.ts`, `score.ts published`, `boot-refusal.ts`, which `boot_agent` runs in every boot, and `run-names.ts`, whose guard `launch` runs before every agent) | the rig's directory, and its checkout's `packages/dispatch` e2e client | the same | shared: the instrument, one reading for every label |
| NATS | a container pinned by digest, or `SKILL_SCENARIOS_NATS_URL` | the same | shared: production runs one NATS for every version |
| Postgres | a container pinned by digest, or `SKILL_SCENARIOS_POSTGRES_URL` | the same | shared: production's one database server |
| Leak guard | the caller's `git` and `jj` wrappers and `PUBLIC_LEAK_CHECK` | the same | shared: a pod's own tooling, outside any release of the plugins |
| Process plumbing (`scripts/e2e/lib/rig.sh`, `held-port.py`) | the rig's checkout | the same | shared: the harness, no service a run speaks to |

Each binary is built once per checkout and kept from one batch to the next: `go build -o` relinks
only an output whose sources changed. Before any run starts, the batch writes each label's
provenance to `services/services-from-<label>`, and each run copies its label's into
`<run>/services-from`, beside its `built-from`, before any agent of it starts: one
`<service> <scope> <from> <digest>` a line, where `<scope>` is `label` (`<from>` is the label's
checkout), `pin` (`<from>` is the pin) or `shared`, and `<digest>` is `sha256:<hex>` of the binary
or program file the run starts, `-` where it starts none, or `missing`. `rig.sh health` calls a run
unhealthy that has no `services-from`, whose `built-from` names no checkout, that names a service
of scope `label` from any other checkout, or that does not name its listener, Dispatch server,
daemon facts and worker pane as its label's.

A daemon stand-in reads every fact of its label's daemon at its boot, and `worker_boot` gives it
30 s to listen. Read there by `go run` of `daemon-pane.go`, each fact compiled the daemon module
first: 6.2-7.4 s a fact with a warm Go build cache and 282 s with a cold one, so a first boot took
about 43 s (head) and 36 s (base), and a batch in a fresh work directory lost 6 of its 8 runs to
that bound. A batch now builds `daemon-pane.go` once per label checkout and, before any run
starts, reads with that build every fact a stand-in of any scenario boots on (`rig.sh`'s
`label_facts`, `bun daemon-pane.ts facts`): a fact that cannot be read fails the batch then,
naming it. A boot reads each fact out of a file, and the stand-in and the worker pane run the
label's build for anything else they read.

How the label's daemon takes a completion is one of those facts, and it is read the same way,
never by the label's name: `daemon-completion.go` reads the checkout's
`internal/workflow/engine.go` for how its engine answers a role that does not run the issue's
phase (`recorded`: `handoff` asks `leftItsPhase`, which records such a completion when the role's
row holds none, this checkout's; `refused`: `if RoleFor(issue.Phase) != fact.Role { return
refused("HANDOFF_NOT_CURRENT_PHASE", …) }`, an older daemon's, main `ff2a10ef`'s among them) and
`internal/api/handoff.go` for the phase its completion route holds a record to (`left`, the phase
the completion is of, `phase.Of`, this checkout's; `issue`, the phase the issue stands in, the
older daemon's). A source in neither shape, or in both, fails the read naming the file and what
it looked for, and a source that is not there fails it naming the file without running the reader.
The base label was main `ff2a10ef`, and is main at the commit the head's newest
forward merge brought in, which moves with every later forward merge, so a table keyed by the
label's name modelled a daemon the base checkout no longer had. The stand-in answers each
completion by the shapes it read at boot; `rig.sh health` and `rig.sh score` read the run's own
label's through the same facts cache (each is given the work directory), out of the checkout the
run's `built-from` names, and only for an answer the two shapes do not give alike
(`completion-answers.ts`'s `ANSWERS`' `byShape`). A process reads each source once: the cache
keeps every reading that succeeded, and the process keeps each refusal, so a run whose checkout
cannot be read costs no more than the first run that asked.

The rig's tests hold both shapes of each file to sources they write themselves, so they need no
older checkout on the host. One row also reads an older daemon's own source where one is given:
`SKILL_SCENARIOS_OLDER_CHECKOUT` names a whole checkout of main `ff2a10ef` (2026-11-07), for
instance `git worktree add <dir> ff2a10ef`, and `daemon-standin.test.ts`'s "an older daemon's
late completion is refused in that daemon's own sentence" reads its `engine.go` and `handoff.go`
as the older shapes and boots the daemon stand-in on it. That boot needs go and runs `go run
-overlay` of `daemon-pane.go` in the checkout's `packages/daemon`, with its `packages/envoy` beside
it (its `go.mod` replaces that module), so the older module's dependencies must be in the Go module
cache or fetchable. With the variable unset the row skips and says so. It is a test input only:
no batch reads it, and it is not the base label's checkout, which `SKILL_SCENARIOS_BASE_CHECKOUT`
names for the rows that read the base label's own contract.

### What the gh world keeps, and where it stops

The gh stand-in answers GitHub from the world's seed and the effect of every call the run made,
read from the run's own `calls.jsonl` (`gh-threads.ts`, one replay for the gh stand-in and the
daemon stand-in). A thread a review's inline comment or a comment of its own opens is listed by
`reviewThreads` and by the REST review comments; `resolveReviewThread` reads back
`isResolved: true`; a reply, through GraphQL or the REST reply route, is its thread's last
comment; and each comment's `databaseId` is its REST id. A base whose protection requires
conversation resolution holds the pull request `BLOCKED` while any thread is open and reports it
`CLEAN` once the last is resolved, and the daemon stand-in moves the record on that same state.
Every world's base protection requires the checks the head reports (`test`, `lint`); the check runs
are reported on the commit read (its `head_sha`, GitHub Actions as their App, their times counted
from when that commit reached the run's remote). A `contents` read answers the file or directory
at the ref it names, or on the default branch where it names none, raw or as GitHub's JSON.
A call naming an owner outside the world's project is refused as the Legion gh wrapper of the
run's label refuses it, for every role: exit 2 and the line the wrapper prints for the form that
names the owner, never a 404. The world reads the call by the wrapper's own selector table
(`legion gh`, `packages/daemon/cmd/legion`: `ghSlotKinds`, `ghOptionKinds`, `ghLeafQuirks` and
`ghKindRank` in `gh_help.go`, read with the wrapper's own patterns of a value), read out of the
label's checkout by `gh-selectors.go` at `services_up` and kept in the facts cache, joined with
the gh help that checkout saves for the wrapper's tests (`testdata/gh-help`), and written into
each worker run by `worker_boot`, since the agent's namespace hides the checkout
(`gh-selectors.ts`). A form naming one repository of that owner and no other (`gh repo view
actions/checkout`, `gh release view v7.0.1 -R actions/checkout`, `gh api
repos/actions/checkout/…`, a pull request's URL) gets the leak guard's line, `legion gh: LEAK
CHECK SETUP ERROR: the gh call: the visibility of <owner>/<repo> could not be read: gh api
repos/<owner>/<repo> exited 2: legion gh: "<owner>" is not an owner of this project:
<LEGION_GH_OWNERS_DIR> holds no gh directory for it`; a form naming the owner alone (`gh search
repos checkout --owner actions`, `gh repo list actions`, `gh api orgs/actions/…`) gets the last
part of it alone, `legion gh: "<owner>" is not an owner of this project: <LEGION_GH_OWNERS_DIR>
holds no gh directory for it`, since the guard has no repository to read; and a call naming the
world's owner beside it, its own repository read from the pane's `GH_REPO` where gh reads it,
gets the wrapper's `the command names more than one owner, …`. A form the label's wrapper holds
that this reading does not model fails the batch at `services_up`, naming it, and so does the
pin in `gh-standin.test.ts`, which runs every form the table holds through the world.

Each role commits as its App's bot, `<slug>[bot]` and
`<id>+<slug>[bot]@users.noreply.github.com`, as production gives it (`packages/daemon/internal/appauth`'s
`AppRoleFor` and `botIdentity`): the implement App for the implementer and the merger, the review
App for every other role (`rig.sh`'s `role_identity`). A pane gets the six variables production's
runtime sets (`role_env`), and adopts the shared workspace's working copy at its boot as production
does (`adopt_workspace`); the implementer's seeded commit is the implement App's. The default
branch's own history, and the brainstorm run's repository, are one developer's
(`world_author_name`), which no role of Legion wrote.

Stated limits, each known and kept so:

- A just-pushed head's checks report `completed` at once, where GitHub would report them in
  progress for as long as CI runs. Modelling that window would make the world's timing something
  an agent waits on while the daemon stand-in sends no wake when CI settles, which is behaviour
  production does not have here, so the window stays out.
- The check runs name GitHub Actions as their App, while the world holds no workflow run
  (`actions/runs` lists none, and `gh run view` refuses as for a run that is not there).
- The REST reply route (`POST repos/<o>/<r>/pulls/<n>/comments/<id>/replies`) is answered in
  GitHub's documented form. No recorded run made that call, since before the world gave a
  thread's comment its `databaseId` no agent had an id to name, so its pin follows GitHub's
  documentation rather than a recorded call.
- A GET of a repository the world's workflow names (an action's tag, read to check the commit its
  `uses:` line pins) reaches the live GitHub, as it does in production, so its answer is GitHub's
  and not the world's. A run in which that read fails for a network reason is not unhealthy for
  it, since a production reviewer can meet the same failure.
- A behaviour of the Legion gh wrapper that its selector table does not describe is not modelled:
  a gh alias or an extension, a set `GH_TOKEN`, `GITHUB_TOKEN` or `GH_HOST`, an option or an
  argument the wrapper cannot place, the working directory's git remotes (a world's workspace,
  a clone of the run's own bare remote, names none), and an option gh's help gives a command
  that the label's saved help does not show (`gh variable list -R`: no saved help shows that
  group's inherited `-R`). The world then answers such a call otherwise than production, health
  calls the run unhealthy, and the run is replaced.
