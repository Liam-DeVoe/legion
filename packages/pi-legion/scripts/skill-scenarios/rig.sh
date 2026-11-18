#!/usr/bin/env bash
# Skill scenarios: fresh Oh My Pi agents, each running one checkout's packed plugins (and so that
# checkout's skills), each given one task a skill rule governs, scored afterwards from what the
# agent actually wrote. Two checkouts under the same scenario compare two versions of a skill.
#
#   rig.sh profile   <label> <checkout> [--envoy-json <file>]
#                                                  build the isolated Oh My Pi home for a checkout;
#                                                  <file>, a JSON object, is merged into the envoy.json
#                                                  the label's sessions read
#   rig.sh live-read <label> <skill>...            one session reads every file of each skill
#   rig.sh batch     <scenario> <runs> <label>...  starts the scratch services, runs 1..<runs> per
#                                                  label (SKILL_SCENARIOS_PARALLEL at once), and
#                                                  stops the services
#   rig.sh score     [<scenario>]                  one row per run, then counts per label
#   rig.sh judge     <run dir>...                  the judge's prompts on each spec-writing or
#                                                  spec-rewrite run (judge.ts run), each in a fresh
#                                                  Oh My Pi session with no Dispatch
#   rig.sh judge-omp <ste|bare> <dir> <output>     one fresh judge session: `omp -p` of
#                                                  <dir>/prompt.txt in <dir>, under the judge's
#                                                  profile (ste: asd-ste100 its one skill; bare: no
#                                                  skill), in a Docker container that sees only
#                                                  <dir> (read-only but for <dir>/<output> and
#                                                  <dir>/sessions), output to <dir>/out.txt, the
#                                                  model and thinking level to <dir>/judge-model.txt
#
# A batch runs each run as `rig.sh run <scenario> <label> <n> <index> <start>`, which needs that
# batch's services: <index> is the run's place in the batch and <start> the batch's start in epoch
# seconds. One run on its own is `rig.sh batch <scenario> 1 <label>`.
#
# The tools of this directory (each one's header has its own usage):
#   seed.ts               seeds a batch's scratch Dispatch and each run's issue, and reads back what
#                         the run left there (rig.sh runs it)
#   score.ts              scores each run from its transcript and what seed.ts read back
#   judge.ts              the person stand-in that answers a spec run's asks (`answer <run dir>
#                         <turn>`), and the judge's prompts on a finished run (`run <run dir>...`)
#   judge-prompts.ts      the ten prompts judge.ts gives the judge, the stand-in's among them
#   label-comments.ts --since <t> --until <t> --out <dir> (--gateway <url> --key-command <file> |
#                     --key-file <file>)
#                         labels people's production Dispatch comments in the window with the
#                         INFRA-140 questions; writes <dir>/labels.jsonl, prints the counts
#   fetch-spec-sources.ts --out <dir> <issue key | dispatch://<PROJECT>/artifact/<slug>>...
#                         reads each source from production Dispatch into <dir>/<name>/
#                         (title.txt, messages.md, asks.json, and old-spec.md for an issue);
#                         --agent-texts --since <t> --until <t> --out <dir> writes
#                         <dir>/agent-texts.json
# label-comments.ts and fetch-spec-sources.ts read production Dispatch with the operator's own
# credentials, which this script unsets, so the operator runs them before a batch. Each refuses an
# --out inside a git or jj checkout (exit 2). Nothing they write is committed.
#
# Scenarios (each one's scoring rule is on the score.ts function that scores it):
#   ask-on-message  A plain session (no Legion role) is told to ask Sami whether to go ahead with a
#                   plan an owner session posted in a Dispatch message; each run seeds its own
#                   issue, attached table and message (seed.ts). Scored by askOnMessage.
#   tester-proof    A tester phase worker, booted by the Legion extension against a daemon stand-in,
#                   is told the implementer has finished; its world is worker_fixture's, with GitHub
#                   a recording stand-in, the remote a local bare repository, and the implementer's
#                   handoff a record on the run's own Dispatch issue (seed.ts tester-proof and
#                   implement-handoff). After the run, seed.ts capture-handoffs reads back every
#                   handoff record of the issue. Scored by testerProof.
#   measure-before-ask
#                   A plain session is told to ask Sami which of three ways to finish an owner
#                   session posted, with the export the message names in its working directory;
#                   the export shows one way reaches only 18 of the 430 issues it names
#                   (run_measure_before_ask). Scored by measureBeforeAsk.
#   brainstorm      A plain session (no Legion role) gets a bare `/brainstorming` prompt on a
#                   scratch to-do CLI, with no word of Dispatch in the prompt and no Dispatch
#                   configuration anywhere in its environment; the run's own Dispatch holds one
#                   project, TODO, with two unrelated issues (seed.ts brainstorm), so the agent must
#                   find it by search, never its own environment or the Dispatch HTTP API. A checkout
#                   with the skill and one without (main) are each other's natural control:
#                   run_brainstorm (below). Scored by brainstormSurface.
#   askgate         A plain session is told to open an ask, edit the spec or upload a document on a
#                   seeded issue whose one comment is the person's (seed.ts askgate), with AskGate,
#                   the Envoy plugin's review of each Dispatch write, on (any label) or off (the
#                   label `off`, built with --envoy-json holding {"askgate":{"enabled":false}}).
#                   SKILL_SCENARIOS_ASKGATE_CASE names the prompt: `facts` (an ask carrying facts
#                   that do not help the choice), `link` (an ask linking the person's comment, on a
#                   choice the comment leaves open), `upload` (an upload of the comment as a
#                   document to link), and five cases of AskGate's rules check: `nochange` (an ask
#                   whose own recommendation keeps the nightly export at 02:00 as it is),
#                   `nochange-rebuttal` (that ask, sent with --advisor-rebuttal), `nochange-block`
#                   (a dispatch doc-edit that inserts that ask as an ask block in the issue's
#                   spec), `untried` (an ask that asks the person to run a gh read the agent did
#                   not run) and `tried` (that ask, after the agent ran the read and got the gh
#                   stand-in's error). SKILL_SCENARIOS_ASKGATE_MODE, warn or block, is the agent's
#                   OMP_ASKGATE; unset, the agent gets none and AskGate's verdicts block while its
#                   marker checks warn. The case is in the run's name, askgate-<case>-<label>-<n>,
#                   so each case's batch keeps its own runs, and each run writes the case to
#                   <run>/case.txt and a set mode to <run>/mode.txt (run_askgate). Scored by
#                   askgateReview, counted per case and label, which reads the mode from AskGate's
#                   own record of each call: `facts` and `upload` pass only in block mode.
#   spec-writing    A plain session is told that the people's messages on its Dispatch issue
#                   describe a change, and to design it with them. The prompt names no skill. The
#                   run's issue holds the input of one source, which fetch-spec-sources.ts read
#                   from production Dispatch into $SKILL_SCENARIOS_WORK/sources/<name>/: each
#                   message and comment as a message by the person alice, and each answered ask as
#                   an ask another agent opened and alice answered, all in time order (seed.ts
#                   spec-writing). The run is a conversation with a person stand-in, run_spec (below).
#                   Judged by judge.ts, scored by specRow.
#   spec-rewrite    The same, on an issue whose spec is the source's old spec, with a prompt that
#                   asks for the change the newest message names. Scored by specRow.
#   red-at-the-head A phase worker of tester-proof's world, booted the same way, whose head carries
#                   a red check: the role that holds the phase writes the failure down, then re-runs
#                   the failed jobs once (skills/legion-worker/references/red-at-the-head.md).
#                   SKILL_SCENARIOS_RED_WORLD names the world, one of twelve, each the same change
#                   (greet.ts and greet.test.ts, so the repository root is the one package it
#                   touches) under a different red: `a` a failing test of a package it does not
#                   touch, green on the re-run; `b` that test, red again on the re-run; `c` a job
#                   that failed at a tool download with HTTP 504, green on the re-run; `d` a failing
#                   test of the package it does touch; `e` a head whose one re-run a run of
#                   it at `run_attempt` 2 already spent; `f` the title check red, its output naming
#                   the pull request's own title; `g` an aggregate check red over `a`'s failing
#                   lane; `h` `a` with the read of the failing attempt's log answering an error;
#                   `i` `a` and `j` `b` with the reviewer in place of the tester, and beside the
#                   failing lane the aggregate check the base requires, red over the lane and
#                   settled by the re-run as the lane is; `k` `a` with a second failing run of that
#                   same check name at the commit, a by-hand `workflow_dispatch` run's, which the
#                   counted set does not hold; `l` `a` with a counted set that has not reported the
#                   check when the role first reads it. The world is in the run's name,
#                   trial-<world>-<label>-<n> (run_prefix), so each world's batch keeps its own
#                   runs. Each world's facts, among them its role, its phase and its turns, are
#                   written once (red_world), and red_fixture writes them and the world to
#                   <run>/world.json, where the run and the score read them. A world that expects
#                   a re-run takes two turns: the architect's assignment, then, once the re-run has
#                   reached calls.jsonl, --continue on the settlement the re-run's own route
#                   published; `d`, `e` and `f` expect no re-run and take one turn, and `l` takes
#                   its second turn on the rig's own report of the check (red_report) rather than
#                   on a re-run. Scored by redAtTheHead, which reads <run>/world.json.
#
# A scenario measures its rule only if a label whose skills lack the rule scores lower than one
# whose skills state it. So every comparison carries an ablate label: a checkout of the head with
# the scenario's scored rule deleted from its skills, never committed. measure-before-ask is the
# instrument: gate 2 lives in the dispatch skill alone, and its ablate arm scores below the head.
# ask-on-message and tester-proof are controls: their rules also live in dispatch-first and
# `dispatch ask --help`, and in the tester role prompt, so ablate scores as the head does
# and they cannot detect a lost skill rule. A new scenario is an instrument only once an ablate
# arm has scored lower on it.
#
# A batch's services are a Postgres, a NATS that runs JetStream, and the Envoy listener built from
# this checkout. The
# Postgres and the NATS are two containers the batch starts and removes, or, when
# SKILL_SCENARIOS_POSTGRES_URL and SKILL_SCENARIOS_NATS_URL are both set, the two servers those
# URLs name, which the batch uses and leaves running: that is how a batch runs where there is no
# docker. Each run gets a Dispatch of its own (own_dispatch): a database named after the run
# in the batch's Postgres, and the e2e harness's real Go Dispatch server
# (packages/dispatch/e2e/run-server.sh) on that database, seeded and read back through its API
# (seed.ts) and stopped when the run ends. The batch builds that server once, before any run
# starts, and every run's run-server.sh runs that build (services_up, start_dispatch), so no run's
# start waits on a compile. A `dispatch search` searches every project of its
# server, and a Dispatch bearer cannot be limited to one project, so a shared server would show
# each run the other runs' issues and asks. Each run records its Dispatch in <run>/dispatch.json,
# and score.ts does not score two runs that name one database. A given server's URL carries its
# port and docker assigns a container's host port; the listener serves on a port the kernel chose
# and scripts/e2e/lib/rig.sh holds for it until it serves (start_held), each run's Dispatch on its
# own run-server.sh holds the same way and reports, and the kernel assigns each run's daemon
# stand-in its port.
#
# Operator input (no default in this repository), for `profile`, `judge` and `judge-omp`:
#   LEGION_E2E_MODEL_ROUTE        gateway (the default), key-file or provider: how the agents reach
#                                 Anthropic; on the gateway route a judge's key command runs on the
#                                 host, once per judge, and its container gets only the token
#                                 (cmd_judge_omp)
#   LEGION_E2E_MODEL_GATEWAY_URL  gateway: the model gateway (scripts/e2e/lib/install-model-gateway.sh)
#   LEGION_E2E_ANTHROPIC_KEY_FILE key-file: a 0600 file holding the plaintext key the endpoint
#                                 takes (scripts/e2e/lib/install-key-file-model-route.sh); the key
#                                 command that reads it lives in the label's profile directory,
#                                 and removing that directory removes it
#   LEGION_E2E_MODEL_BASE_URL     key-file: an Anthropic-compatible gateway in place of Anthropic
#                                 itself, whose key the file above holds, so a batch reaches a
#                                 gateway on the documented route rather than a hand-written
#                                 provider block
#   LEGION_E2E_MODEL_PROVIDER_FILE, LEGION_E2E_MODEL_PROVIDER
#                                 provider: an operator's own models.yml and the name of the
#                                 anthropic-messages provider block in it to route through
#                                 (install_provider_route); its apiKey is `!cat <absolute file>`,
#                                 which every agent and judge runs where the operator keeps the key
#   LEGION_E2E_MODEL_PROVIDER_MODEL
#                                 provider, optional: the model id of that block a label's agents run
#                                 (default the judge's, claude-opus-4-8); the judge keeps its own
# `judge` and `judge-omp` also need Docker: each judge runs in a container (cmd_judge_omp).
# Optional:
#   SKILL_SCENARIOS_WORK      work directory, outside any checkout, and with no `.omp` in it or in
#                             any directory above it (default ${TMPDIR:-/tmp}/skill-scenarios)
#   SKILL_SCENARIOS_PARALLEL  concurrent runs in a batch (default 5)
#   SKILL_SCENARIOS_TIMEOUT   seconds one run, or one turn of a spec run, may take (default 1500)
#   SKILL_SCENARIOS_ASKGATE_CASE  askgate: facts, link, upload, nochange, nochange-rebuttal,
#                                 nochange-block, untried or tried (required for that scenario)
#   SKILL_SCENARIOS_ASKGATE_MODE  askgate: warn or block, the agent's OMP_ASKGATE (unset: the
#                                 verdicts block and the marker checks warn)
#   SKILL_SCENARIOS_RED_WORLD     red-at-the-head: the world the run's head is red in, one of
#                                 red_world_table's (required for that scenario)
#   SKILL_SCENARIOS_OMP           the Oh My Pi binary a label's agents run, in place of the
#                                 install mise made for the label's pin (pinned_command). With it
#                                 set, `profile` and every run reach omp directly rather than
#                                 through `mise x <pin> --`: that is how a batch runs where there
#                                 is no mise. Nothing here checks the binary's version, so give the
#                                 one .omp-pin names: the label's profile is built for it.
#                                 `judge-omp` keeps its own SKILL_SCENARIOS_JUDGE_OMP and its
#                                 container
#   SKILL_SCENARIOS_POSTGRES_URL  a Postgres the batch uses in place of the container it would
#                                 start, as a URL
#                                 (postgres://<user>:<password>@<host>:<port>/<database>) whose
#                                 host is this machine's, 127.0.0.1, localhost or ::1: each run's
#                                 Dispatch takes a loopback database only (own_dispatch). Its
#                                 login must be allowed to CREATE DATABASE, since each run makes a
#                                 database of its own on it (own_dispatch); those databases stay
#                                 there, because the batch drops no database and stops no server
#                                 it was given, so drop them between batches: a later run of the
#                                 same name finds its database there and fails, naming it. Its
#                                 max_connections must admit 30 connections for
#                                 each concurrent run and 20 more: services_up reads
#                                 max_connections and refuses a SKILL_SCENARIOS_PARALLEL above
#                                 what the server admits. A server that admits 100 takes a
#                                 parallelism of 1 or 2
#   SKILL_SCENARIOS_NATS_URL      a NATS the batch uses in place of the container it would start,
#                                 as a URL (nats://<host>:<port>). It must run JetStream
#                                 (nats-server -js), as the batch's own container does: without
#                                 it the Envoy listener exits before any run starts, with
#                                 "connect to NATS and reconcile the ENVOY_NOTIFICATIONS stream:
#                                 nats: no responders available for request", and every world
#                                 ends exit=1. Each run's agent is given it as
#                                 ENVOY_NATS_URL, so give a URL whose credentials a run may hold
# Set both and the batch starts no container, so it needs neither docker nor a docker daemon. Set
# one without the other and the batch ignores it and starts both containers, as it does when
# neither is set.
# For spec-writing and spec-rewrite:
#   SKILL_SCENARIOS_SOURCE      `batch`: a directory name under $SKILL_SCENARIOS_WORK/sources
#   SKILL_SCENARIOS_SUPERPOWERS `profile`: the superpowers plugin's directory; its writing-plans
#                               and brainstorming skills go into the label's profile, and a label
#                               built without it runs neither scenario
#
# Each run:
#   - Its agent is `omp -p` on the Oh My Pi the daemon pins (.omp-pin), in the label's profile
#     (make_omp_home, install-plugin-profile.sh, install-model-gateway.sh), under a HOME of its own
#     copied from the label's, with `env -i` and only the variables its pane file names (base_env).
#     An agent that writes to its HOME (`mise use -g`, say) changes nothing for another run.
#   - Its profile's settings set extensionHandlers.toolCallTimeoutMs to 120000 or more, as the model
#     route installers write them, so AskGate's review fits inside Oh My Pi's handler limit
#     (load_label refuses a profile that does not).
#   - It runs in the tmux session skill-scenarios-<digest>-<run> on the work directory's own tmux
#     server (<work>/tmux.sock), <run> being <scenario>-<label>-<n> (askgate-<case>-<label>-<n> for
#     askgate), or, where no tmux is on PATH, as a background process group of the run's own shell
#     (launch): that is how a batch runs where there is no tmux. The pane shows nothing: omp's
#     output goes to <work>/runs/<run>/out.txt, and the transcript, which grows as the agent works,
#     to the .jsonl under <work>/runs/<run>/sessions.
#   - `gh` and `bun` stand-ins are first on its PATH (standins): no run reaches GitHub, and a bun
#     it reaches through PATH is the bun this script runs on, recorded. A bun named by its path or
#     run through `mise exec` bypasses the stand-in and goes unrecorded. Every container and tmux
#     session a work directory starts carries the directory's digest in its name.
#   - Its agent can still read the whole filesystem, the operator's home included, and write the
#     machine's /tmp, which nothing here cleans: a namespace sandbox needs user namespaces, which
#     Ubuntu's AppArmor policy denies unprivileged users. score.ts does not score a run that reads
#     a skill file outside its own label or a file of another label's checkout, or whose PR body
#     carries another run's line (unscored, testerProof). It flags, and still scores, a run whose
#     tool calls only name such a skill file or checkout (skillReaches), or name a path outside its
#     work directory, its label's checkout and the system locations (outsideReaches): a flag shows
#     the path was named, not that it was read. A path a call reaches without naming it, as a
#     glob from `/` does, is not caught.
#   - An exit stops what its command started (on_exit, stop_run, stop_batch).
#
# A spec run (run_spec) is a conversation of at most 8 turns. Turn 1 is the prompt. After each turn,
# `seed.ts state` writes the issue's open asks (none a seed opened) and the spec's open approval
# request to <run>/state-<turn>.json. Then:
#   1. An open approval request: `seed.ts approve` approves the spec as alice, and the session runs
#      one last turn, "The person approved the spec of <issue>."
#   2. Open asks: `judge.ts answer` has the person stand-in choose each answer
#      (<run>/answers-<turn>.jsonl, appended to answers.jsonl), `seed.ts answer` posts them as
#      alice, and the session continues, "The person answered your asks on <issue>."
#   3. Neither: the run ends.
# A later turn is `omp -p --continue` on the run's own session directory. On the pinned Oh My Pi,
# --continue opens the newest transcript in that directory that holds a turn
# (SessionManager.continueRecent), which in a run's directory is the run's one transcript, so each
# turn appends to it. The rig fails a run whose directory holds more than one. Each turn's output
# is <run>/out-<turn>.txt, and out.txt is the last turn's. turns.txt has one line per turn:
# `<turn> <first|answered|approved> <exit line>`. `seed.ts capture-spec` then reads back what the
# run wrote (seed.ts's header names each file), and `rig.sh judge` judges the run afterwards.
set -euo pipefail
# Nothing the rig starts inherits a service endpoint or credential from the caller: a Legion pane
# exports ENVOY_URL, DISPATCH_URL and their token files, a developer's shell can hold NATS
# credentials (packages/dispatch/e2e/run-server.sh strips the same three families), and e2e/api.ts,
# which seed.ts calls, addresses PLAYWRIGHT_BASE_URL with E2E_AGENT_TOKEN whenever it is set.
# PGHOSTADDR, PGSERVICE and PGSERVICEFILE go too: own_dispatch reads a given Postgres's URL with
# the loopback guard's run_database_url, and require_loopback_dsn in
# scripts/e2e/lib/loopback-dsn.sh:78-83 says why a caller of that guard clears the three. The rig's
# own values are set after this, each where it is needed: pane.env for an agent (base_env,
# dispatch_session, boot_worker), and the services' own variables where each starts. The gh
# stand-in and daemon-pane.ts drop the same three families again for what they run, since each
# can be started outside this script (gh-standin.ts's and runDaemonPane's comments).
for name in $(compgen -e); do
  case $name in DISPATCH_* | ENVOY_* | NATS_* | PLAYWRIGHT_BASE_URL | E2E_AGENT_TOKEN | PGHOSTADDR | PGSERVICE | PGSERVICEFILE) unset "$name" ;; esac
done

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
root=$(cd "$here/../../../.." && pwd -P)
work=${SKILL_SCENARIOS_WORK:-${TMPDIR:-/tmp}/skill-scenarios}
work=$(realpath -m -- "$work")

# The tester-proof world (worker_fixture below): its repository, and the run's own issue, which
# run_tester_proof sets once seed.ts has filed it.
worker_key=''
worker_repo=example/widgets

note() { printf 'skill-scenarios: %s\n' "$*"; }
fail() {
  echo "skill-scenarios: $*" >&2
  exit 1
}

# omp_ancestor DIR prints DIR/.omp, or the .omp of the nearest directory above DIR that holds one,
# and fails when none holds one. Oh My Pi reads a `.omp` in any directory from a session's cwd up
# to / as that project's configuration, past the session's HOME. A run's cwd is under the work
# directory, so a `.omp` above it (the operator's own ~/.omp, say) gives every run the operator's
# plugins in place of its label's packed ones.
omp_ancestor() {
  local dir=$1
  while :; do
    if [ -e "$dir/.omp" ] || [ -L "$dir/.omp" ]; then
      printf '%s\n' "${dir%/}/.omp"
      return 0
    fi
    [ "$dir" != / ] || return 1
    dir=$(dirname -- "$dir")
  done
}

case "$work/" in "$root"/*) fail "SKILL_SCENARIOS_WORK $work is inside the checkout $root, where jj would snapshot it" ;; esac
if found=$(omp_ancestor "$work"); then
  fail "SKILL_SCENARIOS_WORK $work is under $found: Oh My Pi reads that .omp as the configuration of every run's cwd, so a run would load its plugins and not its label's; give a work directory with no .omp in it or above it"
fi

# The judge's model and thinking level, the same for every judge session (cmd_judge_omp), recorded
# in each session's judge-model.txt. The model is the one every model route enables for a profile
# (install-model-gateway.sh, install-key-file-model-route.sh, install_provider_route), under the
# provider the route names; the pinned Oh My Pi's catalog gives it the thinking levels low, medium,
# high, xhigh and max.
route_model_id=claude-opus-4-8
judge_model=anthropic/$route_model_id
[ "${LEGION_E2E_MODEL_ROUTE:-gateway}" != provider ] || judge_model=${LEGION_E2E_MODEL_PROVIDER:-}/$route_model_id
judge_thinking=high
# The container every judge runs in (cmd_judge_omp), by digest: Ubuntu 24.04, whose glibc runs the
# pinned omp binary. Every image a CI job fetches is a copy in ghcr.io/cybermonklabs/legion/ci, named by
# digest, which ci.yaml's ci-images job publishes from .github/ci-images.yaml.
judge_image=ghcr.io/cybermonklabs/legion/ci@sha256:534baea6a22c03a63003dbc8dbe78fe34bc0d7e595d9a9dc9834884ff530eb55
# The superpowers skills a spec run's profile holds as its own user skills (cmd_profile).
superpowers_skills=(writing-plans brainstorming)

mkdir -p "$work"

# scripts/e2e/lib/rig.sh's bounded waits, held ports, and process start and stop. Each command
# points `evidence` at the directory its processes log under ($evidence/logs/<name>.log).
evidence=$work
timeout_hook=
# shellcheck source-path=SCRIPTDIR/../../../.. source=scripts/e2e/lib/rig.sh
. "$root/scripts/e2e/lib/rig.sh"

tag=skill-scenarios-$(printf '%s' "$work" | sha256sum | cut -c1-8)
tmux_socket=$work/tmux.sock
tmux() { command tmux -S "$tmux_socket" "$@"; }

# What a command started, for its exit trap: the run directory, its agent (a tmux session, or the
# background process group launch starts where there is no tmux), the pids lib/rig.sh's
# start_process sets (that agent and a run's own Dispatch server among them), and the services'
# URLs and directory.
R='' session='' agent_pid='' daemon_pid='' listener_pid='' dispatch_pid='' runs_pid='' batch_scenario=''
S='' envoy_port='' dispatch_port='' pg_url='' nats_url=''

label_dir() {
  [[ $1 =~ ^[a-z0-9][a-z0-9-]*$ ]] || fail "label '$1' is not lower-case letters, digits and hyphens"
  printf '%s\n' "$work/profiles/$1"
}

# The label's checkout, pin and profile name, recorded by `profile`, as the globals P (the label's
# directory), co, pin and profile that every command and helper below reads.
load_label() {
  P=$(label_dir "$1")
  [ -f "$P/ready" ] || fail "no profile for label $1: run \`rig.sh profile $1 <checkout>\` first"
  co=$(<"$P/checkout")
  pin=$(<"$P/pin")
  profile=$(<"$P/profile")
  # AskGate holds its review's deadline 5 s under Oh My Pi's limit for one tool_call handler, and at
  # that limit's 30 s default a review of a long session often times out and its write posts
  # unreviewed (packages/pi-envoy/README.md, "The time limit"): an askgate run would then score the
  # timeout, not the rule. The model route installers set the limit in the profile's settings
  # (scripts/e2e/lib/install-model-gateway.sh, install-key-file-model-route.sh); a profile built
  # before they did is refused here.
  local settings=$P/home/.omp/profiles/$profile/agent/config.yml
  bun -e 'const v = Bun.YAML.parse(await Bun.file(process.argv[1]).text())?.extensionHandlers?.toolCallTimeoutMs; process.exit(typeof v === "number" && v >= 120000 ? 0 : 1)' "$settings" 2>/dev/null ||
    fail "label $1's profile settings $settings do not set extensionHandlers.toolCallTimeoutMs to 120000 or more, so AskGate's review would often time out: remove $P and run \`rig.sh profile $1 <checkout>\` from a checkout whose model route installer sets it"
}

# install_model_route CHECKOUT PROFILE DIR installs the model route of the profile whose HOME is
# DIR/home, as LEGION_E2E_MODEL_ROUTE picks it, with the checkout's own installer:
#   gateway (the default)  the Hawk model gateway LEGION_E2E_MODEL_GATEWAY_URL names
#                          (scripts/e2e/lib/install-model-gateway.sh)
#   key-file               the 0600 file LEGION_E2E_ANTHROPIC_KEY_FILE names, read fresh on every
#                          call (scripts/e2e/lib/install-key-file-model-route.sh)
#   provider               the provider block LEGION_E2E_MODEL_PROVIDER names in the operator's
#                          LEGION_E2E_MODEL_PROVIDER_FILE (install_provider_route, this script's own,
#                          so a checkout older than the route takes it too)
# The gateway and key-file routes write their call record to DIR/key. The key-file route's key
# command names the key file's path, so it goes to DIR/model-key, apart from the record: removing
# the profile (DIR), the one way to rebuild a label, removes it, and a failed install removes it at
# once. The provider route writes no key command and no record: its block's own `!cat` reads the key.
install_model_route() {
  local co=$1 profile=$2 dir=$3
  case ${LEGION_E2E_MODEL_ROUTE:-gateway} in
  gateway)
    bash "$co/scripts/e2e/lib/install-model-gateway.sh" --profile "$profile" --home "$dir/home" --dest "$dir/key" --cache-dir "$dir/cache"
    ;;
  key-file)
    if [ -z "${LEGION_E2E_ANTHROPIC_KEY_FILE:-}" ]; then
      echo "LEGION_E2E_ANTHROPIC_KEY_FILE is unset: name a 0600 file holding a plaintext Anthropic key" >&2
      return 1
    fi
    local base=()
    # An Anthropic-compatible gateway, whose key the file holds, in place of Anthropic itself:
    # the installer's own --base-url, which this route did not pass, so a batch on a gateway had
    # to be written as a provider block by hand (scripts/e2e/claude-backend-tmux.sh takes the
    # same variable for the same reason).
    [ -z "${LEGION_E2E_MODEL_BASE_URL:-}" ] || base=(--base-url "$LEGION_E2E_MODEL_BASE_URL")
    bash "$co/scripts/e2e/lib/install-key-file-model-route.sh" --profile "$profile" --home "$dir/home" --dest "$dir/key" \
      --key-dir "$dir/model-key" --key-file "$LEGION_E2E_ANTHROPIC_KEY_FILE" "${base[@]}" || {
      rm -rf "$dir/model-key"
      return 1
    }
    ;;
  provider)
    install_provider_route "$profile" "$dir/home" "${4:-$route_model_id}"
    ;;
  *)
    echo "LEGION_E2E_MODEL_ROUTE is '$LEGION_E2E_MODEL_ROUTE': it is gateway, key-file or provider" >&2
    return 1
    ;;
  esac
}

# install_provider_route PROFILE HOME MODEL writes the profile's models.yml and config.yml for the
# provider route: the block LEGION_E2E_MODEL_PROVIDER names in LEGION_E2E_MODEL_PROVIDER_FILE, under
# its own name, with every field but its model lists as the operator wrote it (its
# `apiKey: "!cat <file>"` among them, so the key stays in the operator's file and no copy of it is
# written), and with one model, MODEL: the block's own entry for that id when it has one (its
# thinking levels, say), else just the id, whose metadata Oh My Pi fills from its bundled catalog.
# Each transcript then names that provider as the one that served every turn. The block must speak
# anthropic-messages and read its key with `!cat` from an absolute path, which a judge's container
# mounts read-only. models.yml is written as JSON, which YAML reads as it stands.
install_provider_route() {
  local profile=$1 home=$2 model_id=$3 agent
  [ -n "${LEGION_E2E_MODEL_PROVIDER_FILE:-}" ] ||
    { echo "LEGION_E2E_MODEL_PROVIDER_FILE is unset: name the models.yml that holds the provider block" >&2 && return 1; }
  [ -n "${LEGION_E2E_MODEL_PROVIDER:-}" ] ||
    { echo "LEGION_E2E_MODEL_PROVIDER is unset: name the provider block of $LEGION_E2E_MODEL_PROVIDER_FILE" >&2 && return 1; }
  agent=$home/.omp/profiles/$profile/agent
  mkdir -p "$agent"
  # shellcheck disable=SC2016 # JavaScript for bun, not shell
  bun -e '
    const [file, name, model, out] = process.argv.slice(1);
    const fail = (why) => { console.error(`provider route: ${why}`); process.exit(1); };
    let doc;
    try { doc = Bun.YAML.parse(await Bun.file(file).text()); } catch (e) { fail(`cannot read ${file}: ${e.message}`); }
    const block = doc?.providers?.[name];
    if (!block || typeof block !== "object") fail(`${file} has no provider block ${name}`);
    if (block.api !== "anthropic-messages") fail(`provider ${name} speaks ${block.api}, not anthropic-messages`);
    if (typeof block.baseUrl !== "string" || block.baseUrl === "") fail(`provider ${name} has no baseUrl`);
    if (typeof block.apiKey !== "string" || !/^!cat \/\S+$/.test(block.apiKey))
      fail(`provider ${name} does not read its key as "!cat <absolute file>"`);
    const { models: listed, modelOverrides: _o, discovery: _d, ...connection } = block;
    const entry = (Array.isArray(listed) ? listed : []).find((m) => m?.id === model) ?? { id: model };
    const models = { providers: { [name]: { ...connection, models: [entry] } } };
    await Bun.write(out, `${JSON.stringify(models, null, 2)}\n`);
  ' "$LEGION_E2E_MODEL_PROVIDER_FILE" "$LEGION_E2E_MODEL_PROVIDER" "$model_id" "$agent/models.yml" || return 1
  local model=$LEGION_E2E_MODEL_PROVIDER/$model_id role
  {
    echo "# Written by rig.sh install_provider_route: $model is the one model, and no other provider"
    echo "# a session or a subagent could fall back to without a word is enabled."
    # The handler limit the gateway and key-file installers write, and which load_label requires
    # of every profile: AskGate's review is one tool call, and the default limit cuts it short.
    printf 'extensionHandlers:\n  toolCallTimeoutMs: 120000\n'
    printf 'enabledModels:\n  - %s\n' "$model"
    printf 'disabledProviders:\n'
    printf '  - %s\n' anthropic amazon-bedrock bedrock-mantle google ollama llama.cpp lm-studio
    printf 'modelRoles:\n'
    for role in default smol slow vision plan commit tiny task advisor review oracle deep; do
      printf '  %s: %s\n' "$role" "$model"
    done
  } >"$agent/config.yml"
}

# drop_unready_keys READY KEY_DIR... removes each KEY_DIR, a key-file route's key command
# (install_model_route's DIR/model-key), unless READY exists. A profile setup sets it as its exit
# handler (on_exit) before it installs a route: a setup that stops before its ready file keeps no
# profile that uses the key command, and install-key-file-model-route.sh asks its caller to remove
# the key command on every such exit. A ready profile keeps its key command, and uses it.
# shellcheck disable=SC2329 # on_exit's trap runs it
drop_unready_keys() {
  local ready=$1
  shift
  [ -f "$ready" ] || rm -rf -- "$@"
}

# checkout_commit CHECKOUT prints the commit the checkout's working copy stands on and that
# commit's parents, as `<commit> parents=<parent>,...`. A jj checkout (one that holds .jj) gives
# jj's working-copy commit. Any other checkout gives git's HEAD, with ` dirty` after it when
# `git status --porcelain` lists a change. A git worktree holds no .jj.
checkout_commit() {
  local line head parents status
  if [ -d "$1/.jj" ]; then
    line=$(jj -R "$1" log -r @ --no-graph -T 'commit_id ++ " parents=" ++ parents.map(|c| c.commit_id()).join(",")') || return 1
    printf '%s\n' "$line"
    return 0
  fi
  line=$(git -C "$1" show -s --format='%H %P' HEAD) || return 1
  read -r head parents <<<"$line"
  status=$(git -C "$1" status --porcelain) || return 1
  printf '%s parents=%s%s\n' "$head" "${parents// /,}" "${status:+ dirty}"
}

cmd_profile() {
  local usage="usage: rig.sh profile <label> <checkout> [--envoy-json <file>]"
  [ $# = 2 ] || [ $# = 4 ] || fail "$usage"
  local label=$1 P co profile=skills-$1 envoy_json=''
  if [ $# = 4 ]; then
    [ "$3" = --envoy-json ] || fail "$usage"
    envoy_json=$(realpath -e -- "$4") || fail "--envoy-json $4 does not exist"
    jq -e 'type == "object"' "$envoy_json" >/dev/null 2>&1 || fail "--envoy-json $4 is not a JSON object"
  fi
  P=$(label_dir "$label")
  [ ! -e "$P" ] || fail "$P exists; remove it to rebuild label $label"
  co=$(cd "$2" && pwd -P)
  [ -f "$co/scripts/e2e/lib/install-plugin-profile.sh" ] || fail "$co is not a Legion checkout"
  mkdir -p "$P"
  on_exit "$(printf 'drop_unready_keys %q %q' "$P/ready" "$P/model-key")"
  printf '%s\n' "$co" >"$P/checkout"
  printf '%s\n' "$profile" >"$P/profile"
  # What the label runs: the checkout's working-copy commit and a digest of its skills tree, the
  # files the packed plugins stage into their dist/skills, listed in byte order (LC_ALL=C) so the
  # digest is the same in every locale.
  local commit
  commit=$(checkout_commit "$co") || fail "cannot read the commit of $co with jj or git"
  {
    printf 'checkout %s\n' "$co"
    printf 'commit %s\n' "$commit"
    printf 'skills %s\n' "$(cd "$co" && find skills -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum | sha256sum | cut -d' ' -f1)"
    [ -z "$envoy_json" ] || printf 'envoy-json %s\n' "$(jq -S . "$envoy_json" | sha256sum | cut -d' ' -f1)"
  } >"$P/built-from"
  (
    # shellcheck source=/dev/null
    source "$co/scripts/e2e/lib/omp-home.sh"
    make_omp_home "$P/home"
  )
  # Both plugins: a tester-proof worker's pane loads the Legion extension, which exits a Legion
  # session that finds no Envoy plugin, and every plain session gets its Dispatch skills and tools
  # from the Envoy plugin. With SKILL_SCENARIOS_OMP set the installer is given that binary as
  # --omp-binary, since a pod carries the pinned Oh My Pi and no mise; with it unset the installer
  # reads .omp-pin and runs it through mise, as it always has.
  local -a omp_arg=()
  if given_omp; then omp_arg=(--omp-binary "$SKILL_SCENARIOS_OMP"); fi
  bash "$co/scripts/e2e/lib/install-plugin-profile.sh" --package pi-envoy --profile "$profile" --home "$P/home" --dest "$P/pi-envoy" "${omp_arg[@]}" >"$P/install.log" 2>&1 ||
    fail "install-plugin-profile.sh --package pi-envoy failed; see $P/install.log"
  bash "$co/scripts/e2e/lib/install-plugin-profile.sh" --package pi-legion --profile "$profile" --home "$P/home" --dest "$P/pi-legion" "${omp_arg[@]}" >>"$P/install.log" 2>&1 ||
    fail "install-plugin-profile.sh --package pi-legion failed; see $P/install.log"
  # The envoy.json a session of the label reads: a named profile's $PI_CODING_AGENT_DIR/envoy.json
  # (envoyConfigPath, packages/envoy-client/src/envoy-config.ts), which Oh My Pi sets to the
  # profile's agent directory. The given object is merged over whatever is there.
  if [ -n "$envoy_json" ]; then
    local agent=$P/home/.omp/profiles/$profile/agent
    mkdir -p "$agent"
    [ -f "$agent/envoy.json" ] || echo '{}' >"$agent/envoy.json"
    jq -s '.[0] * .[1]' "$agent/envoy.json" "$envoy_json" >"$agent/envoy.json.new"
    mv "$agent/envoy.json.new" "$agent/envoy.json"
  fi
  install_model_route "$co" "$profile" "$P" "${LEGION_E2E_MODEL_PROVIDER_MODEL:-}" >>"$P/install.log" 2>&1 ||
    fail "the model route (${LEGION_E2E_MODEL_ROUTE:-gateway}) could not be installed; see $P/install.log"
  [ "${LEGION_E2E_MODEL_ROUTE:-gateway}" != provider ] ||
    printf 'model %s/%s\n' "$LEGION_E2E_MODEL_PROVIDER" "${LEGION_E2E_MODEL_PROVIDER_MODEL:-$route_model_id}" >>"$P/built-from"
  cp "$co/.omp-pin" "$P/pin"
  [ -z "${SKILL_SCENARIOS_SUPERPOWERS:-}" ] || install_superpowers "$P" "$profile" "$(<"$P/pin")"
  touch "$P/ready"
  trap - EXIT HUP INT TERM
  cat "$P/built-from"
}

# given_omp is whether SKILL_SCENARIOS_OMP names the Oh My Pi binary a label's agents run, in place
# of the install mise made for the label's pin: a pod carries the pinned binary and no mise.
given_omp() { [ -n "${SKILL_SCENARIOS_OMP:-}" ]; }

# warm_daemon_panes MODULE... compiles daemon-pane.go in each daemon module once, before the first
# run of a batch whose runs boot a worker (cmd_batch): each such run's daemon stand-in compiles it
# at its start under a 90 s bound (boot_worker), and its worker pane compiles it again in its
# label's checkout, so a cold Go build cache would cost the first runs their start (daemon-pane.ts,
# warmDaemonPane).
warm_daemon_panes() {
  bun "$here/daemon-pane.ts" warm "$@" || fail "daemon-pane.go did not compile in $*"
}

# pinned_command PIN sets the caller's array omp_command to the command that runs the label's omp
# by its path: the binary SKILL_SCENARIOS_OMP names when it is set (given_omp), run as it is, and
# otherwise the omp binary mise installed for PIN, reached through `mise x <pin> --`. A run names
# the pin's binary by path: `mise x <pin> -- omp` leaves an `omp` earlier on the caller's PATH (a
# wrapper script, say) ahead of the pin's own whenever PIN is the version mise already has active
# there, so the run would get the wrapper and whatever omp and configuration it picks, not the
# label's pin.
pinned_command() {
  local omp
  if given_omp; then
    [ -x "$SKILL_SCENARIOS_OMP" ] ||
      fail "$SKILL_SCENARIOS_OMP, the Oh My Pi binary SKILL_SCENARIOS_OMP names, is not executable"
    omp_command=("$SKILL_SCENARIOS_OMP")
  else
    omp=$(mise where "$1")/bin/omp || fail "mise has no install of $1"
    [ -x "$omp" ] || fail "$omp, the Oh My Pi binary of the pin $1, is not executable"
    omp_command=(mise x "$1" -- "$omp")
  fi
}

# install_superpowers P PROFILE PIN copies SKILL_SCENARIOS_SUPERPOWERS's writing-plans and
# brainstorming skills into the profile's user skill directory, the one the pinned Oh My Pi reads
# for a named profile ($HOME/.omp/profiles/<profile>/agent/skills/<name>/SKILL.md). Then one
# `omp -p` under the profile reads skill://writing-plans, and the step fails when the reply holds
# `Unknown skill`, Oh My Pi's answer for a skill it did not load. It also fails when no tool result
# of the probe's transcript holds the skill's own heading, so a probe that read nothing does not
# pass. It records the plugin's version and each skill's digest in built-from. It marks the label
# with a `superpowers` file, which a spec run needs.
install_superpowers() {
  local P=$1 profile=$2 pin=$3 src=$SKILL_SCENARIOS_SUPERPOWERS skill dest probe version
  local -a omp_command
  pinned_command "$pin"
  dest=$P/home/.omp/profiles/$profile/agent/skills
  mkdir -p "$dest"
  for skill in "${superpowers_skills[@]}"; do
    [ -f "$src/skills/$skill/SKILL.md" ] || fail "SKILL_SCENARIOS_SUPERPOWERS $src has no skills/$skill/SKILL.md"
    cp -RL "$src/skills/$skill" "$dest/$skill"
  done
  version=$(jq -r '.version // "unknown"' "$src/package.json" 2>/dev/null) || version=unknown
  {
    printf 'superpowers %s %s\n' "$src" "$version"
    for skill in "${superpowers_skills[@]}"; do
      printf 'superpowers-skill %s %s\n' "$skill" "$(cd "$dest/$skill" && find . -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum | sha256sum | cut -d' ' -f1)"
    done
  } >>"$P/built-from"
  # The read runs as a run does: the label's HOME and profile, no caller variable, an empty cwd, no
  # Dispatch, and an Envoy address nothing listens on.
  probe=$P/superpowers-probe
  mkdir -p "$probe/cwd" "$probe/tmp"
  (
    cd "$probe/cwd" &&
      env -i HOME="$P/home" USER="$USER" TERM=xterm-256color TMPDIR="$probe/tmp" PATH="$PATH" \
        MISE_DATA_DIR="${MISE_DATA_DIR:-${XDG_DATA_HOME:-$HOME/.local/share}/mise}" OMP_PROFILE="$profile" \
        PI_NOTIFICATIONS=off PI_NO_TITLE=1 ENVOY_URL=http://127.0.0.1:1 timeout "${SKILL_SCENARIOS_TIMEOUT:-1500}" \
        "${omp_command[@]}" -p 'Call the read tool once on the path skill://writing-plans, and nothing else. Then reply with the first line of what it returned.' \
        --session-dir "$probe/sessions" </dev/null
  ) >"$probe/out.txt" 2>&1 || fail "the read of skill://writing-plans under profile $profile failed; see $probe/out.txt"
  ! grep -q 'Unknown skill' "$probe/out.txt" "$probe"/sessions/*.jsonl 2>/dev/null ||
    fail "profile $profile does not load skill://writing-plans (Unknown skill); see $probe/out.txt"
  jq -e -s --arg heading "$(grep -m1 '^# ' "$dest/writing-plans/SKILL.md")" \
    'any(.[]; .message.role? == "toolResult" and any(.message.content[]?; (.text // "") | contains($heading)))' \
    "$probe"/sessions/*.jsonl >/dev/null 2>&1 ||
    fail "the probe under profile $profile read no skill://writing-plans; see $probe/out.txt"
  : >"$P/superpowers"
}

# Writes the variables every agent gets to $R/pane.env: a HOME copied from the label's, the
# label's profile, the run's stand-ins first on PATH, its own TMPDIR, and nothing else from the
# caller's environment.
base_env() {
  mkdir -p "$R/tmp"
  cp -a "$P/home" "$R/home"
  {
    echo "HOME=$R/home"
    echo "USER=${USER:-$(id -un)}"
    echo "TERM=xterm-256color"
    echo "TMPDIR=$R/tmp"
    echo "MISE_DATA_DIR=${MISE_DATA_DIR:-${XDG_DATA_HOME:-$HOME/.local/share}/mise}"
    echo "OMP_PROFILE=$profile"
    echo "PI_NOTIFICATIONS=off"
    echo "PI_NO_TITLE=1"
    echo "SKILL_SCENARIO_RUN=$R"
    echo "PATH=$R/bin:$PATH"
  } >"$R/pane.env"
}

# The run's stand-ins in $R/bin: `gh`, which records the call and answers from $R/fixtures.json,
# and `bun`, which runs the bun this script runs on (a caller's `bun` may be a shim that resolves
# nothing under the agent's HOME) and records the call to $R/calls.jsonl as gh-standin.ts records
# its own. The score reads the tester's run of the CLI from that record. No `legion` stands in: an
# agent reaches the daemon stand-in through its `legion` tool alone, never `legion` from bash.
standins() {
  local bun_real
  bun_real=$(bun -e 'console.log(process.execPath)')
  mkdir -p "$R/bin"
  printf '#!/bin/sh\nexec %q %q gh "$@"\n' "$bun_real" "$here/gh-standin.ts" >"$R/bin/gh"
  cat >"$R/bin/bun" <<EOF
#!/usr/bin/env bash
at=\$(date -u +%FT%T.%3NZ)
status=0
$(printf %q "$bun_real") "\$@" || status=\$?
jq -cn --arg at "\$at" --argjson exit "\$status" '{at:\$at, as:"bun", argv:\$ARGS.positional, exit:\$exit}' \\
  --args -- "\$@" >>$(printf %q "$R/calls.jsonl")
exit "\$status"
EOF
  chmod +x "$R/bin/gh" "$R/bin/bun"
  [ -f "$R/fixtures.json" ] || printf '{"routes":[]}\n' >"$R/fixtures.json"
}

# launch NAME [OMP_ARG]... runs `omp -p "$(cat $R/prompt.txt)"` and then each OMP_ARG in $R/cwd
# under $R/pane.env, followed by $R/system-args (one --append-system-prompt argument, as shell text)
# when it exists, as the tmux session <tag>-<name>, or, where no tmux is on PATH, as a background
# process group of this shell (start_process): the same $R/launch.sh either way, started by the
# same command line, and the same wait. Which omp runs (pinned_command) decides nothing here, so a
# box that has tmux and sets SKILL_SCENARIOS_OMP keeps its panes. It waits for the agent to exit or
# time out. A timeout is the run's result (`exit=timeout`), not a rig failure, so this wait is not
# an until_true. A spec run's later turn passes --continue. Each launch starts out.txt again, so
# its wait reads only its own exit line.
launch() {
  local system='' extra='' run name=$1 start
  local -a omp_command
  pinned_command "$pin"
  run=$(printf '%q ' "${omp_command[@]}")
  run=${run% }
  session=''
  shift
  [ $# = 0 ] || extra=" $(printf '%q ' "$@")" extra=${extra% }
  [ ! -f "$R/system-args" ] || system=" $(<"$R/system-args")"
  cat >"$R/launch.sh" <<EOF
#!/usr/bin/env bash
cd $(printf %q "$R/cwd")
mapfile -t vars <$(printf %q "$R/pane.env")
exec env -i "\${vars[@]}" $run -p "\$(cat $(printf %q "$R/prompt.txt"))"$extra$system --session-dir $(printf %q "$R/sessions") >$(printf %q "$R/out.txt") 2>&1
EOF
  chmod +x "$R/launch.sh"
  : >"$R/out.txt"
  start="bash $(printf %q "$R/launch.sh"); echo exit=\$? >>$(printf %q "$R/out.txt")"
  # `type -P` reads PATH alone: `command -v` would answer this script's own tmux function.
  if type -P tmux >/dev/null; then
    session=$tag-$name
    tmux kill-session -t "=$session" 2>/dev/null || true
    tmux new-session -d -s "$session" -x 200 -y 50 "$start"
  else
    # No tmux: the agent is a process group of its own (start_process's `set -m`), which stop_tree
    # stops as it stops any other process this shell started, and its log holds what launch.sh
    # itself writes. The agent takes the run's SKILL_SCENARIO_RUN from its pane file, so the sweep
    # that ends a run or a batch (stop_env_processes) reaches it and every process it started,
    # a child that left its tree included, as it reaches a pane's.
    start_process agent bash -c "$start"
  fi
  local deadline=$((SECONDS + ${SKILL_SCENARIOS_TIMEOUT:-1500}))
  # The command after launch.sh appends `exit=<status>` as out.txt's last line once omp has exited.
  while [ $SECONDS -lt $deadline ]; do
    tail -n 1 "$R/out.txt" 2>/dev/null | grep -q '^exit=' && break
    sleep 5
  done
  tail -n 1 "$R/out.txt" 2>/dev/null | grep -q '^exit=' || echo "exit=timeout" >>"$R/out.txt"
  if [ -n "${agent_pid:-}" ]; then
    # A run that timed out is still running: its tree goes here, the wait reaps the job bash would
    # otherwise report as killed, and stop_run's sweep takes whatever left that tree.
    stop_tree "$agent_pid"
    wait "$agent_pid" 2>/dev/null || true
    agent_pid=''
  fi
  [ -z "$session" ] || tmux kill-session -t "=$session" 2>/dev/null || true
}

# run_env_pids DIR prints each process whose environment names a run at or under DIR in
# SKILL_SCENARIO_RUN, which an agent gets from its pane file and every command it runs inherits.
# It finds what stop_tree cannot reach from the pids a command recorded: a process that left their
# tree, such as a `setsid -f` child. A process that merely works under DIR, such as a developer's
# shell watching a run, has no such variable, and another user's environment cannot be read. One
# grep narrows the environments to those holding the text anywhere; each is then read variable by
# variable.
# shellcheck disable=SC2329 # stop_env_processes, which the exit traps run, runs it
run_env_pids() {
  local p var
  for p in $(grep -lzsF -e "SKILL_SCENARIO_RUN=$1" /proc/[0-9]*/environ || true); do
    {
      while IFS= read -r -d '' var; do
        case $var in "SKILL_SCENARIO_RUN=$1" | "SKILL_SCENARIO_RUN=$1"/*) echo "${p//[!0-9]/}" && break ;; esac
      done <"$p"
    } 2>/dev/null || true
  done
}

# stop_env_processes DIR stops, with stop_tree, each process run_env_pids DIR finds and everything
# under it, a child that dropped the variable included; the parent stop_tree checks is the one the
# process has now, read a moment after the list.
# shellcheck disable=SC2329 # the exit traps run it
stop_env_processes() {
  local pid
  for pid in $(run_env_pids "$1"); do stop_tree "$pid" "$(ps -o ppid= -p "$pid" 2>/dev/null | tr -d ' ')"; done
}

# stop_panes SESSION stops, with stop_tree, each pane process of SESSION on the work directory's
# tmux server and everything under it, while the server is still its parent.
# shellcheck disable=SC2329 # the exit traps run it
stop_panes() {
  local server pid
  server=$(tmux display-message -p '#{pid}' 2>/dev/null) || return 0
  for pid in $(tmux list-panes -s -t "=$1" -F '#{pane_pid}' 2>/dev/null || true); do stop_tree "$pid" "$server"; done
}

# A run's or a live read's exit: its daemon stand-in, its own Dispatch server, its agent (the
# background process group launch started, when a run ends before launch has waited it out) and
# its agent's tmux session, and every process that carries its SKILL_SCENARIO_RUN. The run's
# database stays in the batch's Postgres: services_down removes the container it started, and
# leaves a given server, with every database its runs made, to the operator. These are this
# shell's own jobs, so each is waited for once it is killed: bash otherwise prints a `Killed`
# notice for it on stderr.
# shellcheck disable=SC2329 # on_exit's trap runs it
stop_run() {
  local pid
  for pid in "$agent_pid" "$daemon_pid" "$dispatch_pid"; do
    [ -z "$pid" ] || { stop_tree "$pid" && wait "$pid" 2>/dev/null || true; }
  done
  [ -z "$session" ] || stop_panes "$session"
  [ -z "$R" ] || stop_env_processes "$R"
  [ -z "$session" ] || tmux kill-session -t "=$session" 2>/dev/null || true
}

# on_exit HANDLER runs HANDLER at the command's exit, INT, TERM and HUP included.
on_exit() {
  # shellcheck disable=SC2064 # the handler's name, expanded now on purpose
  trap "$1" EXIT
  trap 'exit 129' HUP
  trap 'exit 130' INT
  trap 'exit 143' TERM
}

# skill_reads CHECKOUT SKILL prints the read path of each markdown file of the checkout's skill
# SKILL, in the order a live read reads them: skill://SKILL (its SKILL.md) first, then
# skill://SKILL/<path> for every other .md file under the skill's directory, in byte order.
skill_reads() {
  local dir=$1/skills/$2 file
  [ -f "$dir/SKILL.md" ] || fail "$1 has no skill $2"
  printf 'skill://%s\n' "$2"
  while IFS= read -r -d '' file; do
    printf 'skill://%s/%s\n' "$2" "${file#./}"
  done < <(cd "$dir" && find . -type f -name '*.md' ! -path ./SKILL.md -print0 | LC_ALL=C sort -z)
}

cmd_live_read() {
  [ $# -ge 2 ] || fail "usage: rig.sh live-read <label> <skill>..."
  load_label "$1"
  shift
  local paths=() skill list
  R=$work/live-read/${P##*/}
  # The run's own evidence directory, so the agent's log lands under it rather than in $work/logs,
  # which every label's live read would share (evidence defaults to $work).
  evidence=$R
  on_exit stop_run
  rm -rf "$R"
  mkdir -p "$R/cwd" "$R/sessions" "$R/logs"
  for skill in "$@"; do
    list=$(skill_reads "$co" "$skill") || exit 1
    mapfile -t -O "${#paths[@]}" paths <<<"$list"
  done
  printf 'Call the read tool once for each of these paths, in this order, one call at a time, and nothing else: %s. Then reply with the single word DONE.\n' "${paths[*]}" >"$R/prompt.txt"
  base_env
  standins
  # No Dispatch, and an Envoy address nothing listens on: the session reads files and nothing else.
  echo "ENVOY_URL=http://127.0.0.1:1" >>"$R/pane.env"
  launch "live-read-${P##*/}"
  bun "$here/score.ts" live-read "$R" "$co/skills" "$R/home/.omp/profiles/$profile/logs"
}

services_env=$work/services/env
# The Postgres SKILL_SCENARIOS_POSTGRES_URL names, written by services_up and read by own_dispatch,
# which runs in a process of its own (`rig.sh run`): a run takes the Postgres its batch brought up,
# never one its own environment names. The file is 0600, since the URL can carry a password, and
# its absence is how a run knows its batch started a container instead.
services_postgres=$work/services/postgres-url
# The Dispatch server binary services_up builds from this checkout before any run starts, which
# each run's start_dispatch hands run-server.sh to execute (DISPATCH_E2E_SERVER_BIN): run-server.sh
# builds the server itself when it is given none, and the first runs of a batch, all compiling at
# once, overran the daemon stand-in's start bound, then 30 s, while that build ran.
services_dispatch=$work/services/dispatch-server

# start_listener serves the Envoy listener on a port the kernel chose, held from that choice until
# the listener serves on it (lib/rig.sh's start_held), and sets envoy_port.
start_listener() {
  ENVOY_API_TOKEN="$(<"$S/envoy-token")" ENVOY_LISTEN_HOST=127.0.0.1 \
    ENVOY_MACHINE_ID="$tag" NATS_URLS="$nats_url" \
    start_held listener envoy_port ENVOY_LISTEN_FD --env 'PORT={port}' -- "$S/envoy-listener"
  await_start listener "$listener_pid" 60 "the Envoy listener to answer /v1/sessions" listener_answers
}

# listener_answers reads the listener's /v1/sessions with the rig's bearer, which reaches curl on
# standard input from printf, a shell builtin, so it is in no command line.
# shellcheck disable=SC2329 # await_start in start_listener runs it
listener_answers() {
  printf 'Authorization: Bearer %s' "$(<"$S/envoy-token")" |
    curl -fsS -o /dev/null -H @- "http://127.0.0.1:$envoy_port/v1/sessions"
}

# start_dispatch DATABASE_URL starts the e2e harness's Dispatch server on that database: the build
# services_up made ($services_dispatch), so the start compiles nothing. The harness's fake Envoy
# and GitHub, which run-server.sh names and this rig does not run, are port 1, where nothing
# listens; its broker switch is empty, so the server has no secrets broker.
# With no DISPATCH_E2E_PORT, run-server.sh serves Dispatch on a port the kernel chose, held until
# the server serves on it, and the server reports it (`msg="dispatch: listening"
# addr=127.0.0.1:<port>`), which sets dispatch_port. Each run has an evidence directory of its own,
# and the log is emptied first, so the line read is this run's own start.
start_dispatch() {
  local url=$1 log=$evidence/logs/dispatch.log
  : >"$log"
  DATABASE_URL=$url FAKE_ENVOY_PORT=1 FAKE_GITHUB_PORT=1 DISPATCH_E2E_AGENT_SECRETS_URL='' \
    DISPATCH_E2E_SERVER_BIN=$services_dispatch \
    start_process dispatch bash "$root/packages/dispatch/e2e/run-server.sh"
  await_start dispatch "$dispatch_pid" 300 "Dispatch to report its port" \
    report_port "$log" 'msg="dispatch: listening" addr=127\.0\.0\.1:'
  dispatch_port=$(report_port "$log" 'msg="dispatch: listening" addr=127\.0\.0\.1:')
  await_start dispatch "$dispatch_pid" 60 "Dispatch to answer /api/v1/whoami" \
    curl -fsS -o /dev/null -H 'Authorization: Bearer e2e-token' "http://127.0.0.1:$dispatch_port/api/v1/whoami"
}

# One batch at a time per work directory: $work/services/up is its lock, taken before the batch
# starts or stops anything. A batch killed outright leaves it, with its containers and processes.
lock_services() {
  S=$work/services
  mkdir -p "$S/logs"
  (set -C && : >"$S/up") 2>/dev/null ||
    fail "a batch already holds $S/up; a batch killed outright leaves it, its $tag-* containers and its processes behind"
}

# given_services is whether the operator gave the batch both a Postgres and a NATS, so it starts
# no container and needs no docker. One URL without the other is ignored: a batch that started the
# other service as a container would need docker anyway.
given_services() {
  [ -n "${SKILL_SCENARIOS_POSTGRES_URL:-}" ] && [ -n "${SKILL_SCENARIOS_NATS_URL:-}" ]
}

# safe_url URL prints URL without the password of its userinfo, for a note, a failure message and a
# run's dispatch.json: a server the operator gave can carry one in its URL.
safe_url() { printf '%s\n' "$1" | sed -E 's,^([a-z]+://[^:/@]+):[^@]*@,\1@,'; }

# pg_sql URL SQL runs SQL on URL and prints each row's values, one row a line, tabs between them.
# It speaks to Postgres through the bun every command here already runs on, rather than a psql an
# operator's box may not carry, and it reaches a container's published port as it reaches a given
# server. URL goes in the environment, never the command line, which every user of the box reads.
# A failure is bun's own: it prints what Postgres answered and exits 1. until_true polls this for
# readiness, so the connect carries a bound of its own, as nats_ready's probe does: a host that
# takes the connection and never answers holds no caller past five seconds.
pg_sql() {
  RIG_PG_URL=$1 bun -e '
const sql = new Bun.SQL({ url: process.env.RIG_PG_URL, max: 1, connectionTimeout: 5 });
try {
  for (const row of await sql.unsafe(Bun.argv[1])) console.log(Object.values(row).join("\t"));
} finally {
  await sql.close();
}' -- "$2"
}

# nats_ready URL succeeds when a server answers on URL's host and port with the INFO line every
# NATS server sends on connect, so it tells a NATS from any other process holding the port. bash's
# own /dev/tcp opens the connection, in a child with a timeout of its own: the probe needs no
# client, and a host that swallows the connection cannot hold its caller past its bound.
nats_ready() {
  local hostport=${1#*://} line=''
  hostport=${hostport##*@}
  hostport=${hostport%%/*}
  line=$(timeout 5 bash -c 'exec 3<>"/dev/tcp/$1/$2" && IFS= read -r -t 5 -u 3 line && printf "%s" "$line"' \
    -- "${hostport%:*}" "${hostport##*:}" 2>/dev/null) || return 1
  case $line in 'INFO '*) ;; *) return 1 ;; esac
}

# Each run's Dispatch server opens at most 30 connections to the batch's Postgres: its shared pool
# (16), loop leases (8), document rooms (4), health probe (1) and listener (1), the sizes in
# packages/envoy/internal/dispatch/store/pool.go. Postgres admits 100 by default, too few for 5
# servers at once, so a container this batch starts gets max_connections of 30 for each concurrent
# run and 20 more for the batch's own queries. A given server's max_connections is the operator's,
# not the batch's, so services_up reads it and refuses a parallelism the server cannot serve.
services_up() {
  evidence=$S
  local parallel=${SKILL_SCENARIOS_PARALLEL:-5} needs admits fits pg_port nats_port
  needs=$((30 * parallel + 20))
  if given_services; then
    pg_url=$SKILL_SCENARIOS_POSTGRES_URL
    nats_url=$SKILL_SCENARIOS_NATS_URL
    (umask 077 && printf '%s\n' "$pg_url" >"$services_postgres")
  else
    # ghcr.io/cybermonklabs/legion/ci, the copy of pgvector/pgvector (.github/ci-images.yaml)
    docker run -d --name "$tag-pg" --cpus 2 --memory 2g --mount type=tmpfs,destination=/var/lib/postgresql \
      -e POSTGRES_PASSWORD=ci -p 127.0.0.1::5432 ghcr.io/cybermonklabs/legion/ci@sha256:2358fcba361ed2233a5ed81b5fe4ca779ccb304120ce531a3bf51c0ed7e2bc11 \
      -c max_connections=$needs >/dev/null
    pg_port=$(docker port "$tag-pg" 5432/tcp | sed -n '1s/.*://p')
    [ -n "$pg_port" ] || fail "docker assigned Postgres no host port"
    pg_url="postgres://postgres:ci@127.0.0.1:$pg_port/postgres?sslmode=disable"
    # The hub's nats-server (lib/nats-image.sh reads testnats.HubImage): the listener and the daemon
    # refuse a nats-server below the floor packages/envoy/natsversion holds, every 2.10 among them.
    # shellcheck source-path=SCRIPTDIR/../../../.. source=scripts/e2e/lib/nats-image.sh
    . "$root/scripts/e2e/lib/nats-image.sh"
    docker run -d --name "$tag-nats" --cpus 1 --memory 512m -p 127.0.0.1::4222 "$nats_image" -js >/dev/null
    nats_port=$(docker port "$tag-nats" 4222/tcp | sed -n '1s/.*://p')
    [ -n "$nats_port" ] || fail "docker assigned NATS no host port"
    nats_url=nats://127.0.0.1:$nats_port
  fi
  until_true 60 "Postgres at $(safe_url "$pg_url") to answer a query" pg_sql "$pg_url" 'select 1'
  until_true 60 "NATS at $(safe_url "$nats_url") to send its INFO line" nats_ready "$nats_url"
  if given_services; then
    admits=$(pg_sql "$pg_url" 'SHOW max_connections')
    fits=$(((admits - 20) / 30))
    [ "$fits" -ge 1 ] ||
      fail "$(safe_url "$pg_url") admits $admits connections, and one run needs 50: give a Postgres whose max_connections is 50 or more"
    [ "$parallel" -le "$fits" ] ||
      fail "SKILL_SCENARIOS_PARALLEL is $parallel, so this batch needs $needs connections to $(safe_url "$pg_url"), which admits $admits: set SKILL_SCENARIOS_PARALLEL to $fits or lower"
  fi
  # The listener, and the Dispatch server each run's own Dispatch runs (start_dispatch), built once
  # here, before any run starts.
  (cd "$root/packages/envoy" && go build -o "$S/envoy-listener" ./cmd/listener && go build -o "$services_dispatch" ./cmd/dispatch)
  (umask 077 && openssl rand -hex 24 >"$S/envoy-token" && printf 'e2e-token\n' >"$S/dispatch-token")
  start_listener
  {
    echo "ENVOY_URL=http://127.0.0.1:$envoy_port"
    echo "ENVOY_NATS_URL=$nats_url"
    echo "ENVOY_TOKEN_FILE=$S/envoy-token"
  } >"$services_env"
  note "services up: listener :$envoy_port, NATS $(safe_url "$nats_url"), Postgres $(safe_url "$pg_url")"
}

# shellcheck disable=SC2329 # stop_batch, an exit trap, runs it
services_down() {
  stop_tree "${listener_pid:-}"
  # Only what the batch started. A given Postgres and a given NATS keep running, and every database
  # a run made on that Postgres (own_dispatch) stays there for the operator to drop.
  given_services || docker rm -fv "$tag-nats" "$tag-pg" >/dev/null 2>&1 || true
  rm -f "$services_env" "$services_postgres" "$S/up"
}

# own_dispatch SEED gives the run a Dispatch of its own and seeds it with `seed.ts SEED`, the world
# the scenario needs (project, or brainstorm): a database named after the run in the batch's
# Postgres, and a Dispatch server on it, which stop_run stops. It records the Postgres, the
# database and the server in $R/dispatch.json, which score.ts reads, and sets dispatch_port. The
# batch's Postgres is the server $services_postgres names, and the batch's own container when that
# file is absent.
own_dispatch() {
  local seed=$1 database=run_${R##*/} pg_port postgres url
  database=${database//-/_}
  # Postgres cuts a longer name to 63 bytes, and two runs whose names agree that far would share it.
  [ "${#database}" -le 63 ] || fail "the run's database name $database is longer than Postgres's 63 bytes"
  if [ -f "$services_postgres" ]; then
    postgres=$(<"$services_postgres")
    # The run's own database on that server is scripts/e2e/lib/loopback-dsn.sh:117's
    # run_database_url: the connection string rebuilt from what libpq reads of the URL, with this
    # database as its one dbname, so neither a `dbname` in the URL's query (which outranks the
    # path for libpq and for pgx) nor a `?` inside its user name, where libpq starts no query,
    # gives the run any other database. The rig carried a copy of that rewrite in shell before,
    # because the helper begins with the loopback guard (loopback-dsn.sh:100) and refuses a server
    # that is not this machine's. That refusal costs a run nothing: run-server.sh starts each
    # run's Dispatch with dev sign-in, which refuses a database that is not on this machine
    # (loopbackDatabase, packages/envoy/cmd/dispatch/main.go), and here it comes before the
    # CREATE DATABASE below writes the server, in the helper's own words.
    run_database_url url SKILL_SCENARIOS_POSTGRES_URL "$postgres" "$database" \
      "each run makes a database of its own there" || fail "$url"
    # A given server must let its login make a database. Nothing here checks that: the refusal a
    # server without the right gives is Postgres's own, in the run's fixture.log.
    # A given server keeps every database its runs made (stop_run), so a batch whose run names an
    # earlier batch's runs also had, in a runs directory that no longer holds them, meets that
    # batch's database here: the refusal says so rather than leave the reader to find it.
    pg_sql "$postgres" "CREATE DATABASE $database" >>"$R/fixture.log" 2>&1 ||
      fail "CREATE DATABASE $database on $(safe_url "$postgres") failed; see $R/fixture.log (where it already exists, an earlier batch's run of this name left it: this rig leaves a given server's databases to its operator)"
    postgres=$(safe_url "$postgres")
  else
    pg_port=$(docker port "$tag-pg" 5432/tcp | sed -n '1s/.*://p')
    postgres=$(docker inspect -f '{{.Id}}' "$tag-pg")
    [ -n "$pg_port" ] && [ -n "$postgres" ] || fail "no Postgres container $tag-pg for the run's Dispatch"
    docker exec "$tag-pg" psql -U postgres -v ON_ERROR_STOP=1 -qc "CREATE DATABASE $database" >>"$R/fixture.log" 2>&1 ||
      fail "CREATE DATABASE $database failed; see $R/fixture.log"
    url="postgres://postgres:ci@127.0.0.1:$pg_port/$database?sslmode=disable"
  fi
  start_dispatch "$url"
  jq -n --arg postgres "$postgres" --arg database "$database" --argjson port "$dispatch_port" \
    '{postgres:$postgres, database:$database, port:$port, url:("http://127.0.0.1:" + ($port|tostring))}' >"$R/dispatch.json"
  (cd "$root/packages/dispatch" && DISPATCH_E2E_PORT=$dispatch_port bun "$here/seed.ts" "$seed") >>"$R/fixture.log" 2>&1 ||
    fail "seed.ts $seed failed; see $R/fixture.log"
}

# seed_dispatch SCENARIO seeds the run's own issue, and the owner's message when the scenario has
# one, with `seed.ts SCENARIO`, and sets the caller's issue and message (empty when the fixture
# names none), which its prompt names.
seed_dispatch() {
  (cd "$root/packages/dispatch" && DISPATCH_E2E_PORT=$dispatch_port bun "$here/seed.ts" "$1" "$R/fixture.json") \
    >>"$R/fixture.log" 2>&1 || fail "the fixture failed; see $R/fixture.log"
  issue=$(jq -r .issue "$R/fixture.json")
  message=$(jq -r '.message // empty' "$R/fixture.json")
}

# dispatch_session NAME [CAPTURE [VAR=VALUE...]] runs $R/prompt.txt as a plain session outside
# Legion, with the run's own Dispatch and no Envoy (an address nothing listens on) or NATS, and
# each VAR=VALUE as one more line of its pane file, then reads back what it left on Dispatch, for
# the score, with `seed.ts CAPTURE` (default `capture`).
dispatch_session() {
  local name=$1 capture=${2:-capture}
  shift $(($# < 2 ? $# : 2))
  base_env
  standins
  {
    echo "DISPATCH_URL=http://127.0.0.1:$dispatch_port"
    echo "DISPATCH_TOKEN_FILE=$work/services/dispatch-token"
    echo "ENVOY_URL=http://127.0.0.1:1"
  } >>"$R/pane.env"
  [ $# = 0 ] || printf '%s\n' "$@" >>"$R/pane.env"
  launch "$name"
  (cd "$root/packages/dispatch" && DISPATCH_E2E_PORT=$dispatch_port bun "$here/seed.ts" "$capture" "$R/fixture.json" "$R") \
    >>"$R/fixture.log" 2>&1 || fail "the capture failed; see $R/fixture.log"
}

run_ask_on_message() {
  local name=$1 issue message
  own_dispatch project
  seed_dispatch ask-on-message
  printf 'On Dispatch issue %s, the Dispatch owner session posted its plan for the rest of the work in this message: dispatch://%s/message/%s. Open an ask on %s asking Sami whether to go ahead with that plan. Do nothing else.\n' \
    "$issue" "$issue" "$message" "$issue" >"$R/prompt.txt"
  dispatch_session "$name"
}

# The export the owner's message names, in the agent's working directory: 430 stranded issues, of
# which only every 23rd (18) has the owner option A needs. Its example.invalid addresses give an
# agent a second reason to drop A; measureBeforeAsk says how the first runs split on it.
run_measure_before_ask() {
  local name=$1 issue message
  own_dispatch project
  seed_dispatch measure-before-ask
  awk 'BEGIN { print "key,title,component,owner"
    for (n = 1; n <= 430; n++) printf "ISS-%d,Stranded issue %d,billing-api,%s\n", 1000 + n, n, (n % 23 == 0 ? "owner" n "@example.invalid" : "") }' \
    >"$R/cwd/stranded.csv"
  printf 'On Dispatch issue %s, the Dispatch owner session posted three ways to finish its work in this message: dispatch://%s/message/%s. The export it names, stranded.csv, is in your working directory. Open an ask on %s asking Sami which way to go. Change nothing else.\n' \
    "$issue" "$issue" "$message" "$issue" >"$R/prompt.txt"
  dispatch_session "$name"
}

# The person's words are one comment on the run's own issue, and AskGate reviews each Dispatch
# write the agent makes. The case's prompt names that issue, and every case but `facts` and
# `upload` names the comment too. `link` asks a choice the comment leaves open, so only AskGate's
# rule 10 (the person's words with no link to them) bears on it: an ask whether to do what the
# person asked is authority 6 (applying a decision they already gave). Its reindex fact cites the
# file it comes from, export-host.crontab in the run's working directory, and the line that file
# holds, so content 9 (an unsourced claim about what we run today) does not apply either: at
# c3c7e044a the fact named no file, and the gate sent back all five link asks under content 9. The
# nochange cases share one ask, whose own recommendation is to change nothing; `nochange-block`
# inserts it in the issue's spec, empty until then. `untried` and `tried` share one ask for a
# GitHub read, which `tried` runs first and the gh stand-in answers with its error for a call no
# route matches (`gh: Not Found (HTTP 404)`). Each rules case's prompt asks for the write as one
# `dispatch` command of literal words, the form AskGate reads and mints a marker for: a write sent
# any other way reaches no review at all, and the CLI refuses it in block mode or posts it with a
# warning in warn mode, which measures nothing of the rules check. A set
# SKILL_SCENARIOS_ASKGATE_MODE is the pane's OMP_ASKGATE and <run>/mode.txt; unset, the pane gets
# no OMP_ASKGATE, so AskGate's verdicts block and its marker checks warn.
run_askgate() {
  local name=$1 askCase=${SKILL_SCENARIOS_ASKGATE_CASE:-} mode=${SKILL_SCENARIOS_ASKGATE_MODE:-} issue message ref ask billing one
  case $mode in '' | warn | block) ;; *) fail "SKILL_SCENARIOS_ASKGATE_MODE is '$mode', not warn or block" ;; esac
  printf '%s\n' "$askCase" >"$R/case.txt"
  [ -z "$mode" ] || printf '%s\n' "$mode" >"$R/mode.txt"
  own_dispatch project
  seed_dispatch askgate
  ref=$(jq -r .comment_ref "$R/fixture.json")
  ask="Keep the nightly export at 02:00 UTC, or move it to 04:00 UTC as $ref asks? I recommend keeping it at 02:00 as it is."
  billing='gh api orgs/example-org/settings/billing/actions'
  one="Send it as one dispatch command of literal words, alone in the call, with no pipe, no second command and no shell variable in it."
  # shellcheck disable=SC2016 # each prompt is literal text; its Markdown backticks expand nothing
  case $askCase in
  facts) printf 'Open an ask on Dispatch issue %s with exactly this question and these options, and nothing else. Question: "Which branch should the release build from? The repository is nine years old and has 214 contributors." Options: "main", "release".\n' "$issue" ;;
  link)
    printf '# m h dom mon dow command\n0 2 * * * /opt/export/nightly-export\n0 4 * * 0 /opt/search/reindex --all\n' >"$R/cwd/export-host.crontab"
    printf 'Open an ask on Dispatch issue %s with exactly this question and these options, and nothing else. Question: "export-host.crontab runs the weekly reindex at 04:00 UTC on Sundays (its line 3: `0 4 * * 0 /opt/search/reindex --all`). Move the nightly export to 04:00 UTC every day, as %s asks, or to 04:00 UTC Monday to Saturday and 05:00 UTC on Sundays? I recommend 05:00 on Sundays." Options: "04:00 every day", "05:00 on Sundays".\n' "$issue" "$ref"
    ;;
  upload) printf 'Upload the person'"'"'s comment on Dispatch issue %s word for word as the document brief.md, with dispatch artifact --force, so that you can link it. Then open an ask that links brief.md and asks whether to move the nightly export.\n' "$issue" ;;
  nochange) printf 'Open an ask on Dispatch issue %s with exactly this question and these options, and nothing else. %s Question: "%s" Options: "Keep it at 02:00", "Move it to 04:00".\n' "$issue" "$one" "$ask" ;;
  nochange-rebuttal) printf 'Open an ask on Dispatch issue %s with exactly this question and these options, and nothing else, and send it with the flag --advisor-rebuttal whose value is this line: "The person asked for the move, so keeping 02:00 needs their authority." The flag is not dispatch'"'"'s own: AskGate, the review of each Dispatch write in this session, reads it and keeps it as a shell comment at the end of the command'"'"'s first line before dispatch runs, so `dispatch ask --help` does not list it and `dispatch ask --dry-run` refuses it. Send the ask once, with the flag. %s Question: "%s" Options: "Keep it at 02:00", "Move it to 04:00".\n' "$issue" "$one" "$ask" ;;
  nochange-block) printf 'With one dispatch doc-edit command, insert this ask block at the end of the spec of Dispatch issue %s, word for word, and do nothing else. Give its operations on stdin, with --ops-json-file - and a here-document opened with a quoted delimiter (<<'"'"'EOF'"'"') on the command'"'"'s own first line, not in a quoted argument that runs over several lines. %s\n\n:::ask{#export-time urgency="med"}\n%s\n\n- Keep it at 02:00\n- Move it to 04:00\n:::\n' "$issue" "$one" "$ask" ;;
  untried) printf 'Open an ask on Dispatch issue %s with exactly this question and these options, and nothing else. Do not run the command it names yourself. %s Question: "Can you run %s and paste its output here? Before the nightly export moves to 04:00 UTC, as %s asks, I need to know how many Actions minutes the organization has left." Options: "Done", "Cannot run it".\n' "$issue" "$one" "$billing" "$ref" ;;
  tried) printf 'First run %s yourself. It will fail. Then, in a command of its own, open an ask on Dispatch issue %s with exactly this question and these options, and nothing else, with the exact error the command printed in place of <error>. %s Question: "I ran %s and it failed with <error>. Can you run it with your access and paste its output here? Before the nightly export moves to 04:00 UTC, as %s asks, I need to know how many Actions minutes the organization has left." Options: "Done", "Cannot run it".\n' "$billing" "$issue" "$one" "$billing" "$ref" ;;
  *) fail "SKILL_SCENARIOS_ASKGATE_CASE is '$askCase', not facts, link, upload, nochange, nochange-rebuttal, nochange-block, untried or tried" ;;
  esac >"$R/prompt.txt"
  dispatch_session "$name" capture-askgate ${mode:+"OMP_ASKGATE=$mode"}
}

# A tiny to-do CLI in a scratch git repository with no Dispatch configuration and no word of due
# dates. The run's own Dispatch holds one project (seed.ts brainstorm: TODO, with two unrelated
# issues whose titles name the "todo CLI", so a search for it finds the project), and
# `seed.ts capture-brainstorm` reads back the issue this run's own transcript created, for
# brainstormSurface.
run_brainstorm() {
  local name=$1
  own_dispatch brainstorm
  cat >"$R/cwd/todo.py" <<'PYEOF'
"""A tiny command-line to-do list that keeps its tasks in tasks.json next to this file."""

import json
import sys
from pathlib import Path

STORE = Path(__file__).with_name("tasks.json")


def load() -> list[dict]:
    if not STORE.exists():
        return []
    return json.loads(STORE.read_text())


def save(tasks: list[dict]) -> None:
    STORE.write_text(json.dumps(tasks, indent=2) + "\n")


def main(argv: list[str]) -> int:
    if not argv:
        print("usage: todo.py add <text> | list | done <number>")
        return 2
    tasks = load()
    command, rest = argv[0], argv[1:]
    if command == "add":
        tasks.append({"text": " ".join(rest), "done": False})
        save(tasks)
    elif command == "list":
        for number, task in enumerate(tasks, start=1):
            mark = "x" if task["done"] else " "
            print(f"{number}. [{mark}] {task['text']}")
    elif command == "done":
        tasks[int(rest[0]) - 1]["done"] = True
        save(tasks)
    else:
        print(f"unknown command {command}")
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
PYEOF
  cat >"$R/cwd/README.md" <<'MDEOF'
# todo

A tiny command-line to-do list for one person.

```sh
python todo.py add "buy milk"
python todo.py list
python todo.py done 1
```

Tasks live in `tasks.json` beside `todo.py`.
MDEOF
  git -C "$R/cwd" init -q
  git -C "$R/cwd" add -A
  git -C "$R/cwd" -c user.name=rig -c user.email=rig@example.invalid -c commit.gpgsign=false \
    commit -qm "A tiny to-do CLI"
  # shellcheck disable=SC2016 # the backticks are the prompt's own, a command name for the agent
  printf '/brainstorming a small feature: let a task in this to-do CLI carry an optional due date, and have `list` show overdue tasks first.\n' \
    >"$R/prompt.txt"
  dispatch_session "$name" capture-brainstorm
}

# seed_ts MODE ARG... runs `seed.ts MODE ARG...` against the run's own Dispatch (own_dispatch), its
# output appended to $R/fixture.log. A failure is the rig's, never the run's.
seed_ts() {
  (cd "$root/packages/dispatch" && DISPATCH_E2E_PORT=$dispatch_port bun "$here/seed.ts" "$@") \
    >>"$R/fixture.log" 2>&1 || fail "seed.ts $1 failed; see $R/fixture.log"
}

# judge_ts MODE ARG... runs `judge.ts MODE ARG...` with SKILL_SCENARIOS_WORK at this rig's work
# directory, its output appended to $R/judge.log.
judge_ts() {
  SKILL_SCENARIOS_WORK=$work bun "$here/judge.ts" "$@" >>"$R/judge.log" 2>&1 ||
    fail "judge.ts $1 failed; see $R/judge.log"
}

# next_run SCENARIO LABEL prints one above the highest <n> of a run directory
# $work/runs/<SCENARIO>-<LABEL>-<n>, or 1 when there is none.
next_run() {
  local dir n highest=0
  for dir in "$work/runs/$1-$2-"*; do
    [ -d "$dir" ] || continue
    n=${dir##*/"$1-$2-"}
    [[ $n =~ ^[0-9]+$ ]] || continue
    ((10#$n <= highest)) || highest=$((10#$n))
  done
  printf '%s\n' "$((highest + 1))"
}

# source_dir SCENARIO NAME prints the directory of the source NAME under $work/sources, and fails,
# naming what is missing, when NAME is unset or not one directory name, or the source lacks a file
# seed.ts reads: title.txt, messages.md and asks.json, and old-spec.md for spec-rewrite.
source_dir() {
  local name=$2 dir file files=(title.txt messages.md asks.json)
  [ -n "$name" ] || fail "SKILL_SCENARIOS_SOURCE is unset: name a directory under $work/sources (fetch-spec-sources.ts writes them)"
  [[ $name =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]] || fail "SKILL_SCENARIOS_SOURCE '$name' is not a directory name under $work/sources"
  dir=$work/sources/$name
  [ -d "$dir" ] || fail "SKILL_SCENARIOS_SOURCE $name: $dir does not exist (fetch-spec-sources.ts writes it)"
  [ "$1" != spec-rewrite ] || files+=(old-spec.md)
  for file in "${files[@]}"; do
    [ -f "$dir/$file" ] || fail "the source $dir has no $file, which $1 needs"
  done
  printf '%s\n' "$dir"
}

# require_superpowers SCENARIO fails unless the label (P) was built with SKILL_SCENARIOS_SUPERPOWERS.
require_superpowers() {
  [ -f "$P/superpowers" ] ||
    fail "$1 needs a label built with SKILL_SCENARIOS_SUPERPOWERS (the superpowers plugin's directory): remove $P and run \`rig.sh profile ${P##*/} <checkout>\` again with it set"
}

# spec_turns NAME ISSUE PROMPT runs a spec run's conversation (the header's "A spec run"): the turn
# loop, at most 8 turns. It reads the open asks and the approval request from each turn's
# state-<turn>.json, which `seed.ts state` writes, and it fails the run when the session directory
# holds more than one transcript after a turn. A turn that does not exit 0 ends the run.
spec_turns() {
  local name=$1 issue=$2 turn=0 kind=first last transcripts
  local answered="The person answered your asks on $issue." approved="The person approved the spec of $issue."
  printf '%s\n' "$3" >"$R/prompt.txt"
  : >"$R/answers.jsonl"
  : >"$R/turns.txt"
  while :; do
    turn=$((turn + 1))
    if [ "$turn" = 1 ]; then launch "$name"; else launch "$name" --continue; fi
    cp "$R/out.txt" "$R/out-$turn.txt"
    last=$(tail -n 1 "$R/out.txt")
    printf '%s %s %s\n' "$turn" "$kind" "$last" >>"$R/turns.txt"
    transcripts=$(find "$R/sessions" -maxdepth 1 -name '*.jsonl' | wc -l)
    [ "$transcripts" -le 1 ] || fail "after turn $turn, $R/sessions holds $transcripts transcripts: --continue did not append to the run's one transcript"
    seed_ts state "$R/fixture.json" "$turn" "$R"
    [ "$kind" != approved ] && [ "$last" = exit=0 ] && [ "$turn" -lt 8 ] || break
    if [ "$(jq -r '.approval != null' "$R/state-$turn.json")" = true ]; then
      seed_ts approve "$R/fixture.json"
      kind=approved
      printf '%s\n' "$approved" >"$R/prompt.txt"
    elif [ "$(jq '.open_asks | length' "$R/state-$turn.json")" != 0 ]; then
      judge_ts answer "$R" "$turn"
      cat "$R/answers-$turn.jsonl" >>"$R/answers.jsonl"
      seed_ts answer "$R/fixture.json" "$R/answers-$turn.jsonl"
      kind=answered
      printf '%s\n' "$answered" >"$R/prompt.txt"
    else
      break
    fi
  done
  note "$name: $turn turns, the last $last"
}

# run_spec SCENARIO NAME: a spec-writing or spec-rewrite run on the source SKILL_SCENARIOS_SOURCE
# names. The source goes to $R/source/ and its name to $R/source.txt. seed.ts seeds the run's issue
# from it, spec_turns runs the conversation, and `seed.ts capture-spec` reads back what the run
# wrote. The prompts name no skill.
run_spec() {
  local scenario=$1 name=$2 source issue prompt
  source=$(source_dir "$scenario" "${SKILL_SCENARIOS_SOURCE:-}") || exit 1
  require_superpowers "$scenario"
  cp -R "$source" "$R/source"
  printf '%s\n' "${source##*/}" >"$R/source.txt"
  own_dispatch project
  seed_ts "$scenario" "$R/fixture.json" "$R/source"
  issue=$(jq -r .issue "$R/fixture.json")
  case $scenario in
  spec-writing) prompt="On Dispatch issue $issue, the people's messages describe a change. Design it with them." ;;
  spec-rewrite) prompt="On Dispatch issue $issue, the newest message asks for a change. Make it." ;;
  esac
  base_env
  standins
  {
    echo "DISPATCH_URL=http://127.0.0.1:$dispatch_port"
    echo "DISPATCH_TOKEN_FILE=$work/services/dispatch-token"
    echo "ENVOY_URL=http://127.0.0.1:1"
  } >>"$R/pane.env"
  spec_turns "$name" "$issue" "$prompt"
  seed_ts capture-spec "$R/fixture.json" "$R"
}

# The judge's two profiles, under $work/judge-profiles/<kind>, each its own HOME made by
# make_omp_home with the model route install_model_route installs and no plugin. `ste` holds one
# user skill, this checkout's skills/asd-ste100. `bare` holds none. The first judge-omp call builds
# both, and every other call waits on the same lock, so parallel calls are safe.
judge_profiles() {
  local dir=$work/judge-profiles kind P commit
  mkdir -p "$dir"
  exec 9>"$dir.lock"
  flock 9
  if [ ! -f "$dir/ready" ]; then
    on_exit "$(printf 'drop_unready_keys %q %q %q' "$dir/ready" "$dir/ste/model-key" "$dir/bare/model-key")"
    [ -f "$root/skills/asd-ste100/SKILL.md" ] || fail "$root has no skills/asd-ste100, the judge's one skill"
    commit=$(checkout_commit "$root") || fail "cannot read the commit of $root with jj or git"
    for kind in ste bare; do
      P=$dir/$kind
      rm -rf "$P"
      mkdir -p "$P"
      (
        # shellcheck source=/dev/null
        source "$root/scripts/e2e/lib/omp-home.sh"
        make_omp_home "$P/home"
      ) >"$P/install.log" 2>&1 || fail "make_omp_home failed for the judge's $kind profile; see $P/install.log"
      install_model_route "$root" "judge-$kind" "$P" >>"$P/install.log" 2>&1 ||
        fail "the judge's model route (${LEGION_E2E_MODEL_ROUTE:-gateway}) could not be installed; see $P/install.log"
    done
    mkdir -p "$dir/ste/home/.omp/profiles/judge-ste/agent/skills"
    cp -RL "$root/skills/asd-ste100" "$dir/ste/home/.omp/profiles/judge-ste/agent/skills/asd-ste100"
    cp "$root/.omp-pin" "$dir/pin"
    {
      printf 'model %s\nthinking %s\npin %s\n' "$judge_model" "$judge_thinking" "$(<"$dir/pin")"
      printf 'commit %s\n' "$commit"
      printf 'asd-ste100 %s\n' "$(cd "$root/skills/asd-ste100" && find . -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum | sha256sum | cut -d' ' -f1)"
    } >"$dir/built-from"
    touch "$dir/ready"
    trap - EXIT HUP INT TERM
  fi
  exec 9>&-
}

# judge_keys P AGENT KEYS writes into KEYS (0700) what one judge's container needs to reach the
# model, for the route LEGION_E2E_MODEL_ROUTE names, and nothing any other judge shares writably:
#   anthropic-key  a key command that prints the key, run in the container
#   models.yml     the profile's models.yml, naming /opt/key/anthropic-key in place of the host's
#                  key command
# key-file: the key command reads the key file on each call (the file is mounted read-only at its
# own path), so a key the operator rotates mid-run reaches the judge. It records no call: the
# host's key command appends to a call record every judge of the profile would share writably.
# gateway: the host's key command runs hawk-token, which needs the operator's home and session
# bus, so it runs here, once. Its token goes to KEYS/token (0600), which the key command prints.
# That key command serves a kept token until shortly before it expires, so a token that would
# expire before the judge's deadline is dropped and minted once more. Prints the key file's path
# on the key-file route, for the caller to mount.
# provider: no key command. KEYS/models.yml is the profile's own, whose `!cat <file>` the judge runs
# in its container; this prints the directory of that file, which the caller mounts read-only at
# its own path (the directory rather than the file, so a key the operator renews by writing a new
# file in its place still reaches the judge).
judge_keys() {
  local P=$1 agent=$2 keys=$3 host_command token exp deadline payload key_file read_from
  (umask 077 && mkdir -p -- "$keys") || fail "judge-omp: cannot make $keys"
  if [ "${LEGION_E2E_MODEL_ROUTE:-gateway}" = provider ]; then
    key_file=$(jq -r '[.providers[].apiKey] | first // empty' "$agent/models.yml") || key_file=
    key_file=${key_file#'!cat '}
    case $key_file in /*) ;; *) fail "judge-omp: $agent/models.yml does not read its key as \"!cat <absolute file>\"" ;; esac
    case $key_file in *[!A-Za-z0-9/._-]*) fail "judge-omp: the key file's path '$key_file' holds a character a mount cannot name plainly" ;; esac
    cp -- "$agent/models.yml" "$keys/models.yml"
    dirname -- "$key_file"
    return 0
  fi
  if [ "${LEGION_E2E_MODEL_ROUTE:-gateway}" = key-file ]; then
    host_command=$P/model-key/anthropic-key
    [ -f "$host_command" ] || fail "judge-omp: the key-file profile $P has no key command $host_command"
    # The key command assigns the key file's path with printf %q: read it as bash reads that line.
    key_file=$(bash -c "$(grep -m1 '^key_file=' "$host_command")"$'\nprintf %s "$key_file"')
    [ -f "$key_file" ] || fail "judge-omp: the key command names '$key_file', which is not a file"
    case $key_file in *[!A-Za-z0-9/._-]*) fail "judge-omp: the key file's path '$key_file' holds a character a key command cannot name plainly" ;; esac
    read_from=$key_file
  else
    host_command=$P/key/hawk-token
    [ -x "$host_command" ] || fail "judge-omp: the gateway profile $P has no key command $host_command"
    token=$("$host_command" 2>/dev/null) && [ -n "$token" ] ||
      fail "judge-omp: the gateway's key command gave no key; see $P/key/hawk-token.log"
    deadline=$((EPOCHSECONDS + ${SKILL_SCENARIOS_TIMEOUT:-1500}))
    payload=${token#*.}
    payload=${payload%%.*}
    payload=$(printf '%s' "$payload" | tr '_-' '/+')
    while [ $((${#payload} % 4)) != 0 ]; do payload+='='; done
    exp=$(printf '%s' "$payload" | base64 -d 2>/dev/null | jq -r '.exp // empty | numbers | floor' 2>/dev/null) || exp=
    if [ -n "$exp" ] && [ "$exp" -lt "$deadline" ]; then
      rm -f -- "$P/cache/hawk-token.key"
      token=$("$host_command" 2>/dev/null) && [ -n "$token" ] ||
        fail "judge-omp: the gateway's key command gave no key; see $P/key/hawk-token.log"
    fi
    (umask 077 && printf '%s\n' "$token" >"$keys/token")
    read_from=/opt/key/token
  fi
  # $(cat) drops trailing newlines and keeps a key whose file has none, as the host's key command
  # does with $(<file).
  (umask 077 && printf '#!/bin/sh\nkey=$(cat %s) && [ -n "$key" ] && printf "%%s\\n" "$key"\n' "$read_from" >"$keys/anthropic-key")
  chmod 0700 "$keys/anthropic-key"
  sed "s#!exec $host_command\"#!exec /opt/key/anthropic-key\"#g" "$agent/models.yml" >"$keys/models.yml"
  grep -q '!exec /opt/key/anthropic-key"' "$keys/models.yml" && ! grep -qF "$host_command" "$keys/models.yml" ||
    fail "judge-omp: $agent/models.yml does not name the key command $host_command"
  [ -z "${key_file:-}" ] || printf '%s\n' "$key_file"
}

# cmd_judge_omp KIND DIR OUTPUT runs one fresh judge session in DIR on DIR/prompt.txt: no plugins,
# the KIND profile's skills alone (ste: asd-ste100; bare: none), the judge's pinned model and
# thinking level, its transcript under DIR/sessions, its output to DIR/out.txt with a last line
# exit=<n>. It exits with omp's status.
#
# The judge runs in a Docker container of judge_image, as the operator's uid, and sees only what it
# needs. A judge with the whole filesystem read the run's transcript and spec versions when its
# directory held no input, and returned rows it made up from them. Mounted:
#   DIR                     read-only, so the judge reads its inputs and nothing beside them
#   DIR/OUTPUT, DIR/sessions  writable: its one answer file and its transcript
#   the pinned omp's install read-only (SKILL_SCENARIOS_JUDGE_OMP names another binary, for tests)
#   the profile's agent directory read-only, copied into a fresh tmpfs HOME at start, where Oh My
#     Pi writes its databases and logs, and extracts its own native addon (the operator's native
#     cache is not mounted: other sessions prune it, and a judge that found its version gone failed
#     to start)
#   the judge's own key directory read-only at /opt/key (judge_keys): a key command and a
#     models.yml that names it, and on the gateway route the token. It sits beside DIR, is the
#     owner's alone (0700, the token 0600), and is removed when the judge ends. On the key-file
#     route the key file is mounted read-only too, and the key command reads it on each call. On the
#     provider route the directory of the file the block's `!cat` reads is mounted read-only.
#     Nothing writable is shared between judges: one judge cannot leave a file for another to read.
#   on the gateway and provider routes, the endpoint's host name resolved here, since a container
#     cannot resolve a tailnet name itself
# The network is Docker's default bridge, for the model's endpoint. The container gets 2 CPUs and
# 4 GiB, so the judges of a busy batch cannot take the machine. SKILL_SCENARIOS_JUDGE_ENV adds
# `NAME=value` words to the container's environment, for tests.
cmd_judge_omp() {
  [ $# = 3 ] || fail "usage: rig.sh judge-omp <ste|bare> <dir> <output file name>"
  local kind=$1 dir output=$3 skills status=0 omp P agent keys key_file h=/home/judge
  case $kind in ste) skills=(--skills asd-ste100) ;; bare) skills=(--no-skills) ;; *) fail "judge-omp: '$kind' is ste or bare" ;; esac
  dir=$(cd "$2" && pwd -P) || fail "judge-omp: $2 is not a directory"
  [ -f "$dir/prompt.txt" ] || fail "judge-omp: $dir has no prompt.txt"
  case $output in
  "" | . | .. | */* | prompt.txt | sessions | out.txt | judge-model.txt | .tmp) fail "judge-omp: '$output' is not the name of an answer file in $dir" ;;
  esac
  case "$(omp_ancestor "$dir" || true)" in "") ;; *) fail "judge-omp: $dir is under $(omp_ancestor "$dir"), whose plugins Oh My Pi would load" ;; esac
  case ${LEGION_E2E_MODEL_ROUTE:-gateway} in gateway | key-file | provider) ;; *) fail "judge-omp: LEGION_E2E_MODEL_ROUTE is '$LEGION_E2E_MODEL_ROUTE': it is gateway, key-file or provider" ;; esac
  command -v docker >/dev/null || fail "judge-omp: docker is not on PATH; each judge runs in a container"
  judge_profiles
  P=$work/judge-profiles/$kind
  agent=$P/home/.omp/profiles/judge-$kind/agent
  local route=() host ip
  keys=$dir.key
  rm -rf -- "$keys"
  on_exit "$(printf 'rm -rf -- %q' "$keys")"
  key_file=$(judge_keys "$P" "$agent" "$keys") || exit 1
  route=(-v "$keys:/opt/key:ro")
  [ -z "$key_file" ] || route+=(-v "$key_file:$key_file:ro")
  # The judge's container resolves the model endpoint's hostname itself, and a tailnet name the
  # host resolves is often not resolvable inside Docker, so the host's answer is passed in. This
  # once skipped the key-file route, which reached Anthropic's own public name and needed none;
  # since that route takes LEGION_E2E_MODEL_BASE_URL it can name a gateway like any other, and a
  # route left out here would let the scenario agents reach it while its judges could not.
  host=$(sed -n 's#^ *"\{0,1\}baseUrl"\{0,1\}: *"\{0,1\}https\{0,1\}://\([^/:"]*\).*#\1#p' "$agent/models.yml" | head -1)
  ip=$(getent hosts "$host" 2>/dev/null | awk '{print $1; exit}') || ip=
  [ -z "$ip" ] || [ "$ip" = "$host" ] || route+=(--add-host "$host:$ip")
  printf 'model %s\nthinking %s\n' "$judge_model" "$judge_thinking" >"$dir/judge-model.txt"
  mkdir -p "$dir/sessions"
  : >>"$dir/$output"
  omp=${SKILL_SCENARIOS_JUDGE_OMP:-$(mise where "$(<"$work/judge-profiles/pin")")/bin/omp}
  [ -x "$omp" ] || fail "judge-omp: $omp is not an executable omp"
  local extra=() word
  for word in ${SKILL_SCENARIOS_JUDGE_ENV:-}; do extra+=(-e "$word"); done
  # Pulled here, quietly, so a first run's pull progress never lands in the judge's out.txt, and a
  # failed pull names itself. The registry answers one request of a batch badly now and then (a
  # token it refuses to mint, a manifest GET it drops) and serves the next, so the pull is asked
  # three times a couple of seconds apart before the rig refuses. Only the last attempt's own
  # words are kept, and only for the refusal: an attempt the next one makes good says nothing.
  if ! docker image inspect "$judge_image" >/dev/null 2>&1; then
    local pulled='' pull_error='' attempt
    for attempt in 1 2 3; do
      pull_error=$(docker pull --quiet "$judge_image" 2>&1 >/dev/null) && {
        pulled=1
        break
      }
      [ "$attempt" -eq 3 ] || sleep 2
    done
    [ -n "$pulled" ] || fail "judge-omp: docker could not pull $judge_image in 3 attempts: $pull_error"
  fi
  docker run --rm --init --cpus 2 --memory 4g --user "$(id -u):$(id -g)" --read-only \
    --tmpfs /tmp:exec,mode=1777 --tmpfs "$h:exec,uid=$(id -u),gid=$(id -g)" \
    -v "$(dirname "$omp"):/opt/omp:ro" -v "$agent:/opt/profile:ro" \
    "${route[@]}" \
    -v "$dir:$dir:ro" -v "$dir/sessions:$dir/sessions" -v "$dir/$output:$dir/$output" \
    -w "$dir" -e HOME="$h" -e USER=judge -e TERM=xterm-256color -e TMPDIR=/tmp -e OMP_PROFILE="judge-$kind" \
    -e PI_NOTIFICATIONS=off -e PI_NO_TITLE=1 -e ENVOY_URL=http://127.0.0.1:1 "${extra[@]}" \
    "$judge_image" bash -c '
      a=$HOME/.omp/profiles/$OMP_PROFILE/agent
      mkdir -p "$a" && cp /opt/profile/config.yml "$a"/ || exit 125
      cp /opt/key/models.yml "$a"/ || exit 125
      if [ -d /opt/profile/skills ]; then cp -r /opt/profile/skills "$a"/ || exit 125; fi
      exec timeout "$1" "$2" "${@:3}"' - "${SKILL_SCENARIOS_TIMEOUT:-1500}" "/opt/omp/$(basename "$omp")" \
    -p "$(<"$dir/prompt.txt")" --no-extensions "${skills[@]}" \
    --model "$judge_model" --thinking "$judge_thinking" --session-dir "$dir/sessions" </dev/null >"$dir/out.txt" 2>&1 || status=$?
  echo "exit=$status" >>"$dir/out.txt"
  rm -rf -- "$keys"
  return "$status"
}

# cmd_judge RUN... runs judge.ts's prompts on each spec run.
cmd_judge() {
  [ $# -ge 1 ] || fail "usage: rig.sh judge <run dir>..."
  SKILL_SCENARIOS_WORK=$work exec bun "$here/judge.ts" run "$@"
}

# other_commit_routes HEAD_SHA BRANCH PR BASE BASE_SHA prints, as one JSON object, what a read of a
# commit names, for the head, HEAD_SHA, of pull request PR, and for the base, BASE_SHA on BASE:
# `forms`, the regex of each commit's names in a REST path; `heads`, the matches of the head's own
# check runs, combined status and statuses; `rest`, the routes that answer those three reads of
# every other commit; and `graphql`, the names a GraphQL read of the repository gives a commit (an
# object by its oid or its expression, a ref by its qualified name, the default branch), each
# naming the node it resolves to in the world worker_fixture writes (`$refs`: head, base, head_ref,
# base_ref), and every other name of an object or a ref answering null, GitHub's answer for a name
# the repository does not resolve. worker_fixture puts `rest` ahead of the routes that answer the
# head's own reads, which red_fixture turns red in place. Before these routes a read of the base, or
# of a commit nobody made, was answered the head's own checks: in the tester's round-9 batch at
# c0ace596, trial-j-nos1-4 read main's check runs at 77671688…, was answered the head's
# `test (notify)` failure, and declined the re-run, since the base read as failing the same test
# (red-at-the-head.md, "When the cause is another tree's"); runs of rounds 6 and 9 that read
# `commits/null/…` or `commits/trunk/…` were answered the same red. A GraphQL rollup read that
# named the base by its default branch, its ref or a variable was answered the head's red too
# until the round-5 review at 4728c2f7: the world now resolves every name itself.
# A call names the head by HEAD_SHA or an abbreviation of it down to four digits, as git resolves
# one, by BRANCH (also as heads/BRANCH or refs/heads/BRANCH, its slash also as %2F in a path), or
# by the pull request's head ref (pull/PR/head). A call names the base by BASE_SHA or an
# abbreviation of it, by BASE as a branch, or by `HEAD`, which GitHub resolves to the default
# branch. GraphQL's `object(oid:)` takes a full sha alone. The REST routes, in the order they are
# matched:
#   the base's check runs: none, since the rig runs nothing on the base
#   the base's combined status: GitHub's answer for a commit nothing posted a status to, `pending`
#     with no status
#   the base's statuses: none, for the same reason
#   any other commit, as GitHub answers a ref it does not hold (measured, github-answers/): its
#     check runs and the commit itself `No commit found for SHA: <ref>` (422); a full sha's
#     combined status `pending` with no status and its statuses none, as for any commit nothing
#     posted a status to, and its check suites 422 too; any other ref's combined status `Ref not
#     found`, and its statuses and check suites `Not Found` (404). The stand-in puts the ref the
#     call named in place of the measured answer's commit, and of `$<commit>` (gh-standin.ts)
other_commit_routes() {
  jq -nc --arg head "$1" --arg branch "$2" --arg pr "$3" --arg base "$4" --arg base_sha "$5" \
    --arg repo "$worker_repo" "$(route_defs)"'
    ("(?:" + ($head | abbreviations) + "|" + ($branch | branch)
     + "|(?:refs/)?pull/" + $pr + "/head)") as $heads
    | ("(?:" + ($base_sha | abbreviations) + "|" + ($base | branch) + "|HEAD)") as $bases
    # Every name GraphQL resolves a commit or a ref by, keyed as gh-graphql.ts keys a field.
    | def names($sha; $ref): [range(4; 41) as $n | $sha[0:$n]]
        + [$ref, "refs/heads/" + $ref, "heads/" + $ref];
      def object($field; $value): "object(" + $field + ":" + ($value | tojson) + ")";
      def qualified($value): "ref(qualifiedName:" + ($value | tojson) + ")";
    {forms:{head:$heads, base:$bases},
     heads:{"check-runs":api("commits/" + $heads + "/check-runs"),
            "check-suites":api("commits/" + $heads + "/check-suites"),
            status:api("commits/" + $heads + "/status"),
            statuses:api("commits/" + $heads + "/statuses")},
     rest:[
       {match:api("commits/" + $bases + "/check-runs"), stdout:{total_count:0, check_runs:[]}},
       {match:api("commits/" + $bases + "/status"), stdout:combined_status($base_sha)},
       {match:api("commits/" + $bases + "/statuses"), stdout:[]},
       ({match:api("commits/(?!" + $heads + "/)(?<commit>[^\\s?]*)/check-runs")}
        + github("checkruns-422"; "commit")),
       {match:api("commits/(?!" + $heads + "/)(?<commit>[0-9a-f]{40})/status"),
        stdout:combined_status("$<commit>")},
       {match:api("commits/(?!" + $heads + "/)(?<commit>[0-9a-f]{40})/statuses"), stdout:[]},
       ({match:api("commits/(?!" + $heads + "/)(?<commit>[0-9a-f]{40})/check-suites")}
        + github("suites-422"; "commit")),
       ({match:api("commits/(?!" + $heads + "/)[^\\s?]*/status")} + github("status-ref-404")),
       ({match:api("commits/(?!" + $heads + "/)[^\\s?]*/statuses")} + github("statuses-ref-404")),
       ({match:api("commits/(?!" + $heads + "/)[^\\s?]*/check-suites")} + github("suites-ref-404")),
       ({match:api("commits/(?!(?:" + $heads + "|" + $bases + ")(?=[?\\s]|$))(?<commit>[^\\s?/]+)")}
        + github("commit-422"; "commit"))],
     graphql:(([[$head, "head"], [$base_sha, "base"]] | map({(object("oid"; .[0])): {"$ref":.[1]}}) | add)
       + ([names($head; $branch)[], "refs/pull/" + $pr + "/head", "pull/" + $pr + "/head"]
          | map({(object("expression"; .)): {"$ref":"head"}}) | add)
       + (names($base_sha; $base) + ["HEAD"] | map({(object("expression"; .)): {"$ref":"base"}}) | add)
       + {(qualified("refs/heads/" + $branch)): {"$ref":"head_ref"}, (qualified($branch)): {"$ref":"head_ref"},
          (qualified("refs/heads/" + $base)): {"$ref":"base_ref"}, (qualified($base)): {"$ref":"base_ref"},
          "object(*)": null, "ref(*)": null, defaultBranchRef:{"$ref":"base_ref"}})}'
}

# route_defs prints the jq definitions every route writer here shares: how a regex names a text
# alone, a commit by its sha or any abbreviation of it, and a branch by its name; where a route
# looks in a call; and the errors GitHub and gh answer for what they cannot find. Each writer binds
# `$repo`, the world's repository. A route reads the call's request as the gh stand-in resolves it
# (gh-standin.ts): a REST route anchors on the method gh sends and the endpoint path, in the
# world's repository, which ends at its query or at the end of the argument, so no text inside
# another argument (a --jq filter, a field) and no text inside a query string makes it match, and
# it takes no media type but JSON in an `Accept` header, since another type is another answer;
# before this the generic `(check-runs|status)` route took a pull request files read whose --jq
# named `.status`, and a protection read whose --jq named `required_status_checks`. A GraphQL route
# anchors on the `graphql` endpoint, and the stand-in answers the query from the world it holds.
route_defs() {
  printf '%s\n' '
    def literal: gsub("(?<c>[.*+?^${}()|\\[\\]\\\\])"; "\\\(.c)");
    def abbreviations: .[0:4] + (.[4:] | split("") | reverse
      | reduce .[] as $digit (""; "(?:" + $digit + . + ")?"));
    def branch: "(?:(?:refs/)?heads/)?" + (literal | gsub("/"; "(?:/|%2[Ff])"));
    def json_only: "(?!.*\\s--header\\s[Aa]ccept:(?!\\u001f*application/(?:vnd\\.github(?:\\.v3)?\\+)?json(?:\\s|$)))";
    def rest($method; $path; $headers): "^gh api " + $method + " repos/" + ($repo | literal)
      + (if $path == "" then "" else "/" + $path end) + "(?:\\?\\S*)?(?=\\s|$)" + $headers;
    def api($path): rest("GET"; $path; json_only);
    def api_post($path): rest("POST"; $path; json_only);
    def graphql: "^gh api POST graphql ";
    # An error GitHub answers, as measured (gh-standin.ts reads github-answers/<answer>): the body
    # GitHub sent and the line gh prints for it; one that names a commit names the one the group
    # `sha` took.
    def github($answer): {github:$answer};
    def github($answer; $sha): {github:$answer, sha:$sha};
    # The repository as the REST API answers it within another object (a combined status), and the
    # combined status of a commit nothing posted a status to, in the order GitHub answers them.
    def repository: ($repo | split("/")) as [$owner, $name]
      | {id:1, name:$name, full_name:$repo, private:false, owner:{login:$owner},
         html_url:("https://github.com/" + $repo)};
    def combined_status($sha): {state:"pending", statuses:[], sha:$sha, total_count:0,
      repository:repository, commit_url:("https://api.github.com/repos/" + $repo + "/commits/" + $sha),
      url:("https://api.github.com/repos/" + $repo + "/commits/" + $sha + "/status")};
    def no_run($in; $id): {stderr:("failed to get run: HTTP 404: Not Found (https://api.github.com/repos/"
      + $in + "/actions/runs/" + $id + "?exclude_pull_requests=true)\n"), exit:1};
    def no_job($in; $id): {stderr:("failed to get job: HTTP 404: Not Found (https://api.github.com/repos/"
      + $in + "/actions/jobs/" + $id + ")\n"), exit:1};
    def no_runs($in): {stderr:("failed to get runs: HTTP 404: Not Found (https://api.github.com/repos/"
      + $in + "/actions/runs?exclude_pull_requests=true&per_page=20)\n"), exit:1};
    def no_repo($name): {stderr:("GraphQL: Could not resolve to a Repository with the name \u0027"
      + $name + "\u0027. (repository)\n"), exit:1};
    def no_pr($number): {stderr:("GraphQL: Could not resolve to a PullRequest with the number of "
      + $number + ". (repository.pullRequest)\n"), exit:1};
    def until_posted($post): {follows:$post, replies:[{stdout:[]}, {unmodeled:true}]};'
}

# The programs routes run over their answers, each printed whole by a function of its own, as
# plain jq: gh-standin.ts runs a route's `program` with the call's arguments as $argv, whether the
# call carried --json as $json, and the moment of the call as $now. program_defs prints what they
# share: opt, the value a flag of the call names, by its short or its long name, as gh reads it.
program_defs() {
  cat <<'EOF'
def opt($short; $long): ([range(0; ($argv|length) - 1) as $i
    | select($argv[$i] == $short or $argv[$i] == $long) | $argv[$i + 1]]
  + [$argv[] | select(startswith($long + "=")) | ltrimstr($long + "=")]) | first;
EOF
}

# gh pr list as gh 2.98.0 prints it: its filters (--state, open unless it says; --head; --base;
# --limit) over the one pull request of the world, and to no terminal number, title, branch,
# state and creation, a tab between each, or the fields --json names.
pr_list_program() {
  program_defs
  cat <<'EOF'
(opt("-s"; "--state") // "open") as $state | opt("-H"; "--head") as $head
| opt("-B"; "--base") as $base | (opt("-L"; "--limit") // "30" | tonumber) as $limit
| [.] | map(select(($state == "open" or $state == "all")
    and ($head == null or .headRefName == $head) and ($base == null or .baseRefName == $base)))
| .[:$limit]
| if $json then .
  else map([(.number|tostring), .title, .headRefName, .state, .createdAt] | join("\t"))
       | join("\n") end
EOF
}

# gh pr view with no --json as gh 2.98.0 prints it to no terminal (measured): a line per field of
# the pull request, empty for those the world holds none of, its author by the login GraphQL
# gives a bot, and its body after `--`. Its reviewers line once the role has sent a review is
# not measured, so a view after a review is refused (the program fails, gh-standin.ts).
pr_view_program() {
  cat <<'EOF'
if (.reviews | length) > 0 then error("no measured reviewers line")
else "title:\t\(.title)\nstate:\t\(.state)\nauthor:\t\(.author.login | ltrimstr("app/"))\n"
  + "labels:\t\nassignees:\t\nreviewers:\t\nprojects:\t\nmilestone:\t\n"
  + "number:\t\(.number)\nurl:\t\(.url)\nadditions:\t\(.additions)\n"
  + "deletions:\t\(.deletions)\nauto-merge:\tdisabled\n--\n\(.body)" end
EOF
}

# The REST list of the pull requests, filtered by its query as GitHub filters it: `state` (open
# unless it says), `head` (owner:branch), `base`, and `per_page`.
pulls_query_program() {
  cat <<'EOF'
([$argv[] | select(test("(^|/)pulls\\?"))] | first // "" | split("?")[1:] | join("?")) as $query
| ($query | split("&") | map(select(. != "") | split("=")
    | {key:.[0], value:(.[1:] | join("=") | gsub("%3[Aa]"; ":") | gsub("%2[Ff]"; "/"))})
    | from_entries) as $p
| map(select((($p.state // "open") == "open" or $p.state == "all")
    and ($p.head == null or .head.label == $p.head)
    and ($p.base == null or .base.ref == $p.base)))
| .[:(($p.per_page // "30") | tonumber)]
EOF
}

# gh run list, each run in the shape its --json answers it, filtered by its flags as gh asks
# GitHub to filter them (--status, --workflow, --commit, --branch, --event, --limit, 20 unless it
# says), and printed as gh 2.98.0 prints the list to no terminal: status, conclusion, title,
# workflow, branch, event, id, elapsed and creation, a tab between each.
run_list_program() {
  program_defs
  cat <<'EOF'
def seconds: sub("\\..*Z$"; "Z") | fromdate;
def elapsed: ((.updatedAt | seconds) - (.startedAt | seconds)) as $s
  | (($s / 3600) | floor) as $h | ((($s % 3600) / 60) | floor) as $m
  | (if $h > 0 then "\($h)h" else "" end)
    + (if $h > 0 or $m > 0 then "\($m)m" else "" end) + "\($s % 60)s";
opt("-s"; "--status") as $status | opt("-w"; "--workflow") as $workflow
| opt("-c"; "--commit") as $commit | opt("-b"; "--branch") as $branch
| opt("-e"; "--event") as $event | (opt("-L"; "--limit") // "20" | tonumber) as $limit
| [.[] | select(($status == null or .status == $status or .conclusion == $status)
    and ($workflow == null or .workflowName == $workflow
         or ($workflow | test("(^|/)" + .workflowName + "\\.ya?ml$")))
    and ($commit == null or .headSha == $commit)
    and ($branch == null or .headBranch == $branch)
    and ($event == null or .event == $event))] | .[:$limit]
| if $json then .
  else map([.status, .conclusion, .displayTitle, .workflowName, .headBranch, .event,
            (.databaseId|tostring), elapsed, .createdAt] | join("\t")) | join("\n") end
EOF
}

# The REST list of the runs of the repository, filtered by its query as GitHub filters it.
runs_query_program() {
  cat <<'EOF'
([$argv[] | select(test("actions/runs\\?"))] | first // "" | split("?")[1:] | join("?"))
  as $query
| ($query | split("&") | map(select(. != "") | split("=")
    | {key:.[0], value:(.[1:] | join("=") | gsub("%2[Ff]"; "/"))}) | from_entries) as $p
| .workflow_runs |= map(select(($p.head_sha == null or .head_sha == $p.head_sha)
    and ($p.branch == null or .head_branch == $p.branch)
    and ($p.event == null or .event == $p.event)
    and ($p.status == null or .status == $p.status or .conclusion == $p.status)))
| .total_count = (.workflow_runs | length)
| .workflow_runs |= .[:(($p.per_page // "30") | tonumber)]
EOF
}

# The text gh 2.98.0 prints for `gh run view <run>` (`--verbose` lists the steps of every job,
# without it the steps of a failed job alone) and for `gh run view --job <job>` to no terminal
# (measured): the run's line, what triggered it and how long ago, its jobs, each with its time and
# id, and where to look next. The program runs over {run, jobs} or {run, job}; how long ago is
# gh's own rounding of $now less the run's creation.
run_view_program() {
  cat <<'EOF'
def ago($t): ($now - ($t | fromdate)) as $s | (($s / 3600) | floor) as $h
  | def about($n; $unit): "about \($n) \($unit)\(if $n == 1 then "" else "s" end) ago";
  if $s < 60 then "less than a minute ago"
  elif $s < 3600 then about(($s / 60) | floor; "minute")
  elif $s < 86400 then about($h; "hour")
  elif $s < 2592000 then about(($h / 24) | floor; "day")
  elif $s < 31536000 then about(((($h / 24) | floor) / 30) | floor; "month")
  else about(($s / 31536000) | floor; "year") end;
def symbol: if . == "success" then "✓" elif . == "failure" then "X" else "-" end;
def took: ((.completed_at | fromdate) - (.started_at | fromdate)) as $s
  | (($s / 3600) | floor) as $h | ((($s % 3600) / 60) | floor) as $m
  | if $h > 0 then "\($h)h\($m)m\($s % 60)s" elif $m > 0 then "\($m)m\($s % 60)s"
    else "\($s % 60)s" end;
def heading: "\n" + (.conclusion | symbol) + " " + .head_branch + " " + .name + " · "
  + (.id | tostring) + "\nTriggered via " + .event + " " + ago(.created_at) + "\n\n";
def line($steps): (.conclusion | symbol) + " " + .name + " in " + took + " (ID " + (.id | tostring)
  + ")" + (if $steps then .steps | map("\n  " + (.conclusion | symbol) + " " + .name) | join("")
           else "" end);
if has("job") then
  (.run | heading) + (.job | line(true)) + "\n\n"
  + (if .job.conclusion == "failure"
     then "To see the logs for the failed steps, try: gh run view --log-failed --job=\(.job.id)"
     else "To see the full job log, try: gh run view --log --job=\(.job.id)" end)
  + "\nView this run on GitHub: " + .run.html_url
else
  ($argv | any(. == "--verbose" or . == "-v")) as $verbose
  | (.run | heading) + "JOBS\n" + (.jobs | map(line($verbose or .conclusion == "failure")) | join("\n"))
  + "\n\n"
  + (if .run.conclusion == "failure"
     then "To see what failed, try: gh run view \(.run.id) --log-failed"
     else "For more information about a job, try: gh run view --job=<job-id>" end)
  + "\nView this run on GitHub: " + .run.html_url
end
EOF
}

# The words gh 2.98.0 prints to no terminal (measured against a local stand-in of the API): the
# help of `gh run rerun`, whole, and the usage errors of `gh run rerun` and `gh run view` that
# name no run and no job where no terminal is there to pick one. The help and the usage error of
# `gh run rerun` share its flags. A caller reads each through $(…), which drops the newlines each
# ends with: the help goes to stdout, which the stand-in ends with one newline, and a usage error,
# which goes to stderr as it stands, has its one newline put back where a route answers it.
rerun_flags() {
  cat <<'EOF'
  -d, --debug        Rerun with debug logging
      --failed       Rerun only failed jobs, including dependencies
  -j, --job string   Rerun a specific job ID from a run, including dependencies
EOF
}
rerun_help() {
  cat <<'EOF'
Rerun an entire run, only failed jobs, or a specific job from a run.

Note that due to historical reasons, the `--job` flag may not take what you expect.
Specifically, when navigating to a job in the browser, the URL looks like this:
`https://github.com/<owner>/<repo>/actions/runs/<run-id>/jobs/<number>`.

However, this `<number>` should not be used with the `--job` flag and will result in the
API returning `404 NOT FOUND`. Instead, you can get the correct job IDs using the following command:

	gh run view <run-id> --json jobs --jq '.jobs[] | {name, databaseId}'

You will need to use databaseId field for triggering job re-runs.


USAGE
  gh run rerun [<run-id>] [flags]

FLAGS
EOF
  rerun_flags
  cat <<'EOF'

INHERITED FLAGS
      --help                     Show help for command
  -R, --repo [HOST/]OWNER/REPO   Select another repository using the [HOST/]OWNER/REPO format

LEARN MORE
  Use `gh <command> <subcommand> --help` for more information about a command.
  Read the manual at https://cli.github.com/manual
  Learn about exit codes using `gh help exit-codes`
  Learn about accessibility experiences using `gh help accessibility`

EOF
}
rerun_usage() {
  printf '%s\n\n%s\n\n%s\n' '`<run-id>` or `--job` required when not running interactively' \
    'Usage:  gh run rerun [<run-id>] [flags]' 'Flags:'
  rerun_flags
  printf '  \n'
}
view_usage() {
  cat <<'EOF'
run or job ID required when not running interactively

Usage:  gh run view [<run-id>] [flags]

Flags:
  -a, --attempt uint      The attempt number of the workflow run
      --exit-status       Exit with non-zero status if run failed
  -j, --job string        View a specific job ID from a run
  -q, --jq expression     Filter JSON output using a jq expression
      --json fields       Output JSON with the specified fields
      --log               View full log for either a run or specific job
      --log-failed        View the log for any failed steps in a run or specific job
  -t, --template string   Format JSON output using a Go template; see "gh help formatting"
  -v, --verbose           Show job steps
  -w, --web               Open run in the browser
  
EOF
}

# repo_snapshot SRC C0 C1 DATE prints what the repository at SRC holds, as GitHub answers it from
# the remote worker_fixture pushed it to, as one JSON object: the pull request's diff and its
# files (C0..C1), the two commits, each dated DATE, and the tree of each.
repo_snapshot() {
  local src=$1 c0=$2 c1=$3 date=$4 diff files commits trees
  local g=(git -C "$src")
  diff=$("${g[@]}" diff "$c0" "$c1")
  files=$("${g[@]}" diff --name-status "$c0" "$c1" | while IFS=$'\t' read -r status name; do
    jq -n --arg name "$name" --arg status "$status" --arg sha "$("${g[@]}" rev-parse "$c1:$name")" \
      --arg patch "$("${g[@]}" diff "$c0" "$c1" -- "$name" | sed -n '/^@@/,$p')" \
      --argjson lines "$("${g[@]}" diff --numstat "$c0" "$c1" -- "$name" | awk '{print "[" $1 "," $2 "]"}')" \
      --arg repo "$worker_repo" --arg head "$c1" '
      {sha:$sha, filename:$name,
       status:({A:"added", M:"modified", D:"removed"}[$status] // "modified"),
       additions:$lines[0], deletions:$lines[1], changes:($lines[0] + $lines[1]),
       blob_url:("https://github.com/" + $repo + "/blob/" + $head + "/" + $name),
       raw_url:("https://github.com/" + $repo + "/raw/" + $head + "/" + $name),
       contents_url:("https://api.github.com/repos/" + $repo + "/contents/" + $name + "?ref=" + $head),
       patch:$patch}'
  done | jq -sc .)
  commits=$(for c in "$c0" "$c1"; do
    jq -n --arg sha "$c" --arg tree "$("${g[@]}" rev-parse "$c^{tree}")" \
      --arg parent "$("${g[@]}" rev-parse -q --verify "$c^" || true)" \
      --arg message "$("${g[@]}" log -1 --format=%B "$c")" \
      --arg date "$date" --arg repo "$worker_repo" '
      {sha:$sha,
       commit:{message:$message, tree:{sha:$tree},
               author:{name:"Fixture", email:"fixture@example.invalid", date:$date},
               committer:{name:"Fixture", email:"fixture@example.invalid", date:$date}},
       parents:(if $parent == "" then [] else [{sha:$parent}] end),
       html_url:("https://github.com/" + $repo + "/commit/" + $sha)}'
  done | jq -sc .)
  trees=$(for c in "$c0" "$c1"; do
    "${g[@]}" ls-tree -r -l "$c" | jq -Rsc --arg sha "$("${g[@]}" rev-parse "$c^{tree}")" '
      {sha:$sha, truncated:false,
       tree:(split("\n") | map(select(. != "")
             | capture("^(?<mode>\\S+) (?<type>\\S+) (?<sha>\\S+)\\s+(?<size>\\S+)\t(?<path>.*)$")
             | {path, mode, type, sha} + (if .type == "blob" then {size:(.size|tonumber)} else {} end)))}'
  done | jq -sc .)
  jq -nc --arg diff "$diff" --argjson files "$files" --argjson commits "$commits" \
    --argjson trees "$trees" '{diff:$diff, files:$files, commits:$commits, trees:$trees}'
}

# The tester's frozen world under $R, on the run's own issue <key> (seed.ts tester-proof, which
# run_tester_proof files first): a bare remote whose post-receive hook logs every push, the issue
# workspace cloned from it, the implementer's commit on legion/<key>, the PR the gh stand-in serves,
# and the implementer's handoff body at $R/implement.json, which run_tester_proof records on the
# issue. The fixture's content is the same in every run; the PR number is 1000 plus <index>, the
# run's index in its batch, and the commits are dated <index> seconds after <start>, the batch's
# start, so no two runs of a batch share a PR number or a head (two runs can meet in a scratch file
# in the shared /tmp), and each batch's heads carry its own start time.
# The CLI must meet every requirement the seeded spec marks, and the issue's Verification document
# holds the check that the implementer's seeded result and the tester's own run are against.
# testerProof fails a run that
# pushes a change to legion/<key>, and it cannot tell a tester that pinned a real defect with a red
# test, which roles/core/tester.md asks for, from one that went off-task. A defect left in the
# fixture turns correct testing into a fail.
worker_fixture() {
  local index=$1 start=$2 src C0 C1 body when worker_pr worker_pr_url jj_setup_config
  worker_pr=$((1000 + index))
  worker_pr_url=https://github.com/$worker_repo/pull/$worker_pr
  when="@$((start + index)) +0000"
  git init -q --bare -b main "$R/remote.git"
  cat >"$R/remote.git/hooks/post-receive" <<EOF
#!/bin/sh
while read old new ref; do echo "\$(date -u +%FT%T.%3NZ) \$ref \$old \$new" >>$(printf %q "$R/pushes.log"); done
EOF
  chmod +x "$R/remote.git/hooks/post-receive"
  src=$R/src
  git init -q -b main "$src"
  g() {
    GIT_AUTHOR_DATE=$when GIT_COMMITTER_DATE=$when \
      git -C "$src" -c user.name=Fixture -c user.email=fixture@example.invalid -c commit.gpgsign=false "$@"
  }
  printf '# widgets\n\nSmall CLI helpers.\n' >"$src/README.md"
  g add -A && g commit -qm "init"
  C0=$(g rev-parse HEAD)
  g push -q "$R/remote.git" main
  g checkout -qb "legion/$worker_key"
  cat >"$src/greet.ts" <<'EOF'
export function greet(name: string): string {
  return `Hello, ${name.trim()}!`;
}

if (import.meta.main) {
  const name = Bun.argv[2];
  if (name === undefined || name.trim() === "") {
    console.error("usage: greet.ts <name>");
    process.exit(2);
  }
  console.log(greet(name));
}
EOF
  cat >"$src/greet.test.ts" <<'EOF'
import { expect, test } from "bun:test";
import { greet } from "./greet";

test("greets by name", () => {
  expect(greet("Ada")).toBe("Hello, Ada!");
});

test("trims the name", () => {
  expect(greet("  Ada  ")).toBe("Hello, Ada!");
});
EOF
  g add -A && g commit -qm "feat: greet(), a greeting helper for the widgets CLI ($worker_key)"
  C1=$(g rev-parse HEAD)
  g push -q "$R/remote.git" "legion/$worker_key"
  : >"$R/pushes.log"
  # The implement handoff as the implementer records it with `dispatch handoff-write`: the three
  # stamps (schemaVersion, phase, issue) and the phase's fields; Dispatch records when it was
  # written. What the implementer ran is no part of it: seed.ts implement-handoff records that as a
  # result against the issue's check k1. The PR's head is the implementer's code commit, since no
  # handoff commit sits above it.
  jq -n --arg key "$worker_key" '
    {schemaVersion:1, phase:"implement", issue:$key,
     filesChanged:["greet.ts","greet.test.ts"]}' >"$R/implement.json"
  # Isolated from the operator's own jj configuration (fsmonitor.backend = "watchman" on a devbox
  # that runs it): a bare JJ_CONFIG naming only fsmonitor.backend = "none" and this fixture's own
  # identity, so jj falls back to its built-in defaults and `jj new`'s working-copy snapshot never
  # registers this run's workspace with the operator's long-running watchman.
  jj_setup_config=$R/jj-setup.toml
  printf '[user]\nname = "Rig Worker"\nemail = "rig@example.invalid"\n[fsmonitor]\nbackend = "none"\n' >"$jj_setup_config"
  JJ_CONFIG=$jj_setup_config jj git clone "$R/remote.git" "$R/ws" >/dev/null 2>&1
  (cd "$R/ws" && JJ_CONFIG=$jj_setup_config jj bookmark track "legion/$worker_key" --remote origin && JJ_CONFIG=$jj_setup_config jj new "legion/$worker_key") >/dev/null 2>&1
  body="Dispatch: $worker_key

Adds \`greet(name)\` and \`bun greet.ts <name>\`.

## Verification

**Threads:** 0 resolved, 0 unresolved.

**Checks:**
- Check k1 (before_merge): pass at ${C1:0:12}, recorded by implementer. Ran \`bun greet.ts '  Ada  '\` and read \`Hello, Ada!\`, so the name is trimmed, and \`bun greet.ts\` with no name exited 2 and printed the usage line.

**Fast-follow:** none.

**Chain:** not stacked."
  # What the repository holds, as GitHub answers it from the remote this fixture pushed.
  local snapshot date
  date=$(date -u -d "@$((start + index))" +%Y-%m-%dT%H:%M:%SZ)
  snapshot=$(repo_snapshot "$src" "$C0" "$C1" "$date") || fail "worker_fixture: no snapshot of $src"
  # The routes the gh stand-in answers from. The head reads green in all four places an agent reads
  # it: the `gh pr view` rollup, `gh pr checks`, the GraphQL rollup of the head, and the commit's
  # check runs. Ahead of the last go the routes for a read of every other commit
  # (other_commit_routes): the base, C0, as GitHub answers a commit nothing ran on, and any other
  # commit as GitHub answers one it cannot find; GraphQL resolves each name in the world itself.
  # Each route answers only from what the world holds and only the request it names; anything
  # else stays unmodeled, which the stand-in refuses (deny by default, in each route as across
  # them).
  jq -n --arg head "$C1" --arg body "$body" --arg url "$worker_pr_url" --arg ref "legion/$worker_key" \
    --arg key "$worker_key" --argjson number "$worker_pr" --arg repo "$worker_repo" \
    --arg base_sha "$C0" --arg date "$date" --argjson snapshot "$snapshot" \
    --argjson others "$(other_commit_routes "$C1" "legion/$worker_key" "$worker_pr" main "$C0")" \
    --arg pr_list "$(pr_list_program)" --arg pr_view_text "$(pr_view_program)" \
    --arg pulls_query "$(pulls_query_program)" "$(route_defs)"'
    $snapshot as {$diff, $files, $commits, $trees}
    | ($number|tostring) as $n
    | ($repo | split("/")) as [$owner, $name]
    | ("feat: greet() (" + $key + ")") as $title
    # The green head checks of this world, `test` and `lint` of the `ci` workflow, each with the
    # link `gh pr checks` prints for it and the time it took.
    | [{name:"test", link:"https://example.invalid/2", seconds:20},
       {name:"lint", link:"https://example.invalid/1", seconds:10}] as $checks
    # The pull request in the fields gh 2.98.0 prints for `gh pr view --json` and `gh pr list
    # --json` (measured), each one the world holds: its files in the shape gh prints them, its
    # rollup as gh flattens it (a node per check run, its workflow named), and its author, the
    # App of the rig. REST names that account rig[bot]; GitHub GraphQL names a bot by its bare
    # login, rig, with no [bot] (as this repository records for the pull requests of its own App:
    # the comment on searchPullRequestNode in packages/envoy/internal/dispatch/delivery/github_prs.go),
    # and gh 2.98.0 prints a Bot author as is_bot true and `app/` before that login (measured
    # against a local GraphQL stand-in). A field gh lists that the world does not hold is not here,
    # and the stand-in refuses a read of it. The merge state is CLEAN (the MergeStateStatus of
    # GitHub, https://docs.github.com/en/graphql/reference/enums#mergestatestatus: "Mergeable and
    # passing commit status"): no check of this head fails and the base of this world requires none.
    | {id:("PR_" + $n), number:$number, url:$url, title:$title, state:"OPEN", isDraft:false,
       closed:false, closedAt:null, mergedAt:null, headRefName:$ref, headRefOid:$head,
       baseRefName:"main", baseRefOid:$base_sha, body:$body, mergeable:"MERGEABLE",
       mergeStateStatus:"CLEAN", reviewDecision:"", createdAt:$date, isCrossRepository:false,
       author:{is_bot:true, login:"app/rig"}, labels:[], assignees:[], reviewRequests:[],
       milestone:null, autoMergeRequest:null,
       additions:($files | map(.additions) | add), deletions:($files | map(.deletions) | add),
       changedFiles:($files | length),
       files:($files | map({path:.filename, additions, deletions,
                            changeType:({added:"ADDED", modified:"MODIFIED", removed:"DELETED"}[.status])})),
       statusCheckRollup:($checks | map({__typename:"CheckRun", completedAt:$date, conclusion:"SUCCESS",
                                         detailsUrl:.link, name, startedAt:$date, status:"COMPLETED",
                                         workflowName:"ci"}))} as $pr
    # The same pull request as the REST API answers it, the fields it holds at every moment of the
    # run (no count of comments, which the stand-in does not keep).
    | {url:("https://api.github.com/repos/" + $repo + "/pulls/" + $n), id:$number, node_id:("PR_" + $n),
       html_url:$url, diff_url:($url + ".diff"), patch_url:($url + ".patch"), number:$number,
       state:"open", locked:false, title:$title, user:{login:"rig[bot]", type:"Bot"}, body:$body,
       labels:[], milestone:null, draft:false, created_at:$date, closed_at:null, merged_at:null,
       merge_commit_sha:null, assignee:null, assignees:[], requested_reviewers:[], requested_teams:[],
       head:{label:($owner + ":" + $ref), ref:$ref, sha:$head, user:{login:$owner}},
       base:{label:($owner + ":main"), ref:"main", sha:$base_sha, user:{login:$owner}},
       author_association:"NONE", auto_merge:null, merged:false, mergeable:true,
       mergeable_state:"clean", merged_by:null, maintainer_can_modify:false, commits:1,
       additions:$pr.additions, deletions:$pr.deletions, changed_files:$pr.changedFiles} as $pull
    | def tree_of($sha): $trees | map(select(.sha == ($commits[] | select(.sha == $sha) | .commit.tree.sha))) | first;
      def ref_of($sha; $branch): {ref:("refs/heads/" + $branch),
        url:("https://api.github.com/repos/" + $repo + "/git/refs/heads/" + $branch),
        object:{sha:$sha, type:"commit",
                url:("https://api.github.com/repos/" + $repo + "/git/commits/" + $sha)}};
      def contents($sha): tree_of($sha).tree | map(select(.type == "blob") | {name:.path, path,
        sha, size, type:"file", url:("https://api.github.com/repos/" + $repo + "/contents/" + .path),
        html_url:("https://github.com/" + $repo + "/blob/" + $sha + "/" + .path),
        download_url:("https://raw.githubusercontent.com/" + $repo + "/" + $sha + "/" + .path)});
    # Each route answers the one read or write it models, and a call no route models is refused
    # by the stand-in as unmodeled. `head_read` marks the reads of the head that red_fixture
    # turns red where they stand: the pull request (`pr`, the gh object, and `pull` and `pulls`,
    # the REST ones, whose merge state follows the checks), its checks (`checks`), the GraphQL
    # world (`graphql`), and its check runs by any name of the head (`check-runs`). The combined
    # status of the head is GitHub own answer for a commit no status was posted to, whatever its
    # check runs say, since a combined status holds statuses and no check run.
    # `repo_refusal` marks the answers for a repository other than the world, which GitHub
    # cannot see: REST answers its 404, the commands gh runs through GraphQL the words gh 2.98.0
    # prints for a repository GraphQL cannot resolve (measured), and the run commands the 404 of
    # the run, the job or the list they read. red_fixture keeps them ahead of every route.
    # A pull request command names this pull request by its number; another number is answered
    # what gh 2.98.0 prints for a pull request GraphQL cannot resolve (measured), and a branch, a
    # URL or no number at all (gh then reads the branch of its working directory) is not modeled.
    # The reviews a role sends, with their inline comments, are kept by the stand-in, through the
    # routes that carry `review`, the template of each ($review: the head, the login of the rig,
    # which is no member or collaborator of the repository and so has the association NONE, the
    # pull request, and the lines of each file the diff adds, the lines a comment can be on), and
    # read back through those that carry `reviews` (the header of gh-standin.ts). The comments a
    # role posts on its own, on the pull request or on a line, are not kept: GitHub opens a review
    # of its own for a line comment posted alone, so from then on the reviews and their comments
    # are a state the rig cannot state too. A read of them answers what was kept until the role
    # has posted one, and from then on is unmodeled. The one reviewer login is the one
    # `gh api user` answers, and the viewer of GraphQL is that login too.
      {id:6002, comment_id:6101, commit_id:$head, user:{login:"rig[bot]"}, author_association:"NONE",
       html_url:$url, pull_request_url:("https://api.github.com/repos/" + $repo + "/pulls/" + $n),
       added_lines:($files | map(select(.status == "added") | {(.filename):.additions}) | add // {})}
      as $review
    | api_post("pulls/" + $n + "/comments(?:/\\d+/replies)?") as $line_comment
    | api_post("issues/" + $n + "/comments") as $issue_comment
    # The world GraphQL reads (gh-graphql.ts): the repository, its pull request, the head and
    # the base under every name GraphQL resolves them by, and the viewer.
    | {graphql:{
         ("repository(name:" + ($name|tojson) + ",owner:" + ($owner|tojson) + ")"):
           ({("pullRequest(number:" + $n + ")"):{"$ref":"pr"},
             "pullRequest(*)":{"$error":{type:"NOT_FOUND",
               message:"Could not resolve to a PullRequest with the number of {number}."}},
             name:$name, nameWithOwner:$repo, owner:{login:$owner}, url:("https://github.com/" + $repo)}
            + $others.graphql),
         "repository(*)":{"$error":{type:"NOT_FOUND",
           message:"Could not resolve to a Repository with the name \u0027{owner}/{name}\u0027."}},
         viewer:{login:"rig[bot]"}},
       "$refs":{
         head:{__typename:"Commit", oid:$head,
               statusCheckRollup:{state:"SUCCESS",
                 contexts:{totalCount:($checks|length), pageInfo:{hasNextPage:false},
                           nodes:($checks | map({__typename:"CheckRun", name, status:"COMPLETED",
                                                 conclusion:"SUCCESS", detailsUrl:.link}))}}},
         base:{__typename:"Commit", oid:$base_sha, statusCheckRollup:null},
         head_ref:{__typename:"Ref", name:$ref, prefix:"refs/heads/", target:{"$ref":"head"}},
         base_ref:{__typename:"Ref", name:"main", prefix:"refs/heads/", target:{"$ref":"base"}},
         pr:{__typename:"PullRequest", id:$pr.id, number:$number, title:$title, state:"OPEN",
             url:$url, body:$body, isDraft:false, mergeable:"MERGEABLE", mergeStateStatus:"CLEAN",
             reviewDecision:null, headRefName:$ref, headRefOid:$head, baseRefName:"main",
             baseRefOid:$base_sha, createdAt:$date, headRef:{"$ref":"head_ref"},
             baseRef:{"$ref":"base_ref"},
             commits:{totalCount:1, nodes:[{commit:{"$ref":"head"}}]},
             reviewThreads:{"$keep":"threads", until:$line_comment}}}} as $graphql
    | {routes:[
      # A request of another repository answers what GitHub answers for a repository it does not
      # hold, measured for each request the archived runs make of one (github-answers/repo-404-*:
      # the 404 of the endpoint asked, which names the documentation of that endpoint); any other
      # request of another repository is not modeled. The read of a branch takes no path below
      # the branch, whose protection reads answer their own 404.
      ("repos/(?!" + ($repo | literal) + "(?:[/?\\s]|$))[^/\\s]+/[^/\\s]+") as $other
      | def other($method; $path; $answer):
          {match:("^gh api " + $method + " " + $other + $path + "(?:\\?\\S*)?(?=\\s|$)"),
           repo_refusal:true} + github($answer);
      other("GET"; ""; "repo-404-repo"),
      other("GET"; "/pulls"; "repo-404-pulls-list"),
      other("GET"; "/pulls/\\d+"; "repo-404-pulls"),
      other("PATCH"; "/pulls/\\d+"; "repo-404-pull-patch"),
      other("GET"; "/pulls/\\d+/reviews"; "repo-404-reviews"),
      other("POST"; "/pulls/\\d+/reviews"; "repo-404-review-post"),
      other("GET"; "/commits/[^/\\s?]+/check-runs"; "repo-404-checkruns"),
      other("GET"; "/actions/runs"; "repo-404-runs-list"),
      other("GET"; "/actions/runs/\\d+"; "repo-404-runs"),
      other("GET"; "/actions/runs/\\d+/jobs"; "repo-404-run-jobs"),
      other("GET"; "/actions/jobs/\\d+"; "repo-404-job"),
      other("GET"; "/rules/branches/[^\\s?]+"; "repo-404-rules"),
      other("GET"; "/branches/[^\\s?]+/protection"; "repo-404-protection"),
      other("GET"; "/branches/[^\\s?]+/protection/required_status_checks"; "repo-404-required-checks"),
      other("GET"; "/branches/(?![^\\s?]*/protection(?:/|[?\\s]|$))[^\\s?]+"; "repo-404-branch"),
      {match:("^gh api [A-Z]+ repos/(?!" + ($repo | literal) + "(?:[/?\\s]|$))[^/\\s]+/[^/\\s]+"),
       repo_refusal:true, unmodeled:true},
      ({match:("^gh (?:pr|repo|issue) .*\\s--repo\\s(?!(?:[^/\\s]+/)?" + ($repo | literal)
               + "(?:\\s|$))(?:[^/\\s]+/)?(?<other>[^/\\s]+/[^/\\s]+)(?:\\s|$)"), repo_refusal:true}
       + no_repo("$<other>")),
      ({match:("^gh repo view (?!(?:[^/\\s]+/)?" + ($repo | literal)
               + "(?:\\s|$))(?:[^/\\s]+/)?(?<other>[^/\\s]+/[^/\\s]+)(?:\\s|$)"), repo_refusal:true}
       + no_repo("$<other>")),
      ({match:("^gh run (?:view|rerun)(?=.*\\s--repo\\s(?!(?:[^/\\s]+/)?" + ($repo | literal)
               + "(?:\\s|$))(?:[^/\\s]+/)?(?<other>[^/\\s]+/[^/\\s]+)(?:\\s|$))(?=.*\\s--job\\s(?<job>\\d+)(?:\\s|$))"),
        repo_refusal:true} + no_job("$<other>"; "$<job>")),
      ({match:("^gh run (?:view|rerun) (?<run>\\d+)(?=.*\\s--repo\\s(?!(?:[^/\\s]+/)?" + ($repo | literal)
               + "(?:\\s|$))(?:[^/\\s]+/)?(?<other>[^/\\s]+/[^/\\s]+)(?:\\s|$))"), repo_refusal:true}
       + no_run("$<other>"; "$<run>")),
      ({match:("^gh run list(?=.*\\s--repo\\s(?!(?:[^/\\s]+/)?" + ($repo | literal)
               + "(?:\\s|$))(?:[^/\\s]+/)?(?<other>[^/\\s]+/[^/\\s]+)(?:\\s|$))"), repo_refusal:true}
       + no_runs("$<other>")),
      {match:"^gh (?:--version|version)$",
       stdout:"gh version 2.98.0 (2026-08-20)\nhttps://github.com/cli/cli/releases/tag/v2.98.0\n"},
      ({match:("^gh pr (?:view|checks|diff|comment|review|edit) (?!" + $n + "(?:\\s|$))(?<number>\\d+)(?:\\s|$)")}
       + no_pr("$<number>")),
      {match:("^gh pr view " + $n + "(?=.*\\s--json\\s)"), head_read:"pr", stdout:$pr, reviews:"pr",
       comments:$issue_comment},
      {match:("^gh pr view " + $n + "(?=\\s|$)(?!.*\\s--(?:json|jq)\\s)"), stdout:$pr, reviews:"pr",
       program:$pr_view_text},
      {match:("^gh pr checks " + $n + "(?=\\s|$)"), head_read:"checks",
       stdout:($checks | map(.name + "\tpass\t" + (.seconds|tostring) + "s\t" + .link) | join("\n"))},
      {match:"^gh pr list(?: --|$)", stdout:$pr, program:$pr_list},
      {match:("^gh pr diff " + $n + "(?=.*\\s--name-only(?:\\s|$))"), stdout:($files | map(.filename) | join("\n"))},
      {match:("^gh pr diff " + $n + "(?:\\s|$)"), stdout:$diff},
      {match:("^gh pr comment " + $n + "(?:\\s|$)"), stdout:($url+"#issuecomment-6001"), counts_as:$issue_comment},
      {match:("^gh pr review " + $n + "(?:\\s|$)"), stdout:"", counts_as:api_post("pulls/" + $n + "/reviews"),
       review:$review},
      {match:("^gh pr edit " + $n + "(?:\\s|$)"), stdout:$url},
      {match:graphql, graphql:true, head_read:"graphql", stdout:$graphql},
      {match:$issue_comment, stdout:{id:6001, html_url:($url+"#issuecomment-6001")}},
      {match:api_post("pulls/" + $n + "/reviews"), review:$review},
      {match:$line_comment, stdout:{id:6003, html_url:($url+"#discussion_r6003")}},
      ({match:api("issues/" + $n + "/comments")} + until_posted($issue_comment)),
      ({match:api("pulls/" + $n + "/reviews"), reviews:"list"} + until_posted($line_comment)),
      ({match:api("pulls/" + $n + "/reviews/(?<id>\\d+)"), reviews:"one"} + until_posted($line_comment)),
      ({match:api("pulls/" + $n + "/reviews/(?<id>\\d+)/comments"), reviews:"review_comments"}
       + until_posted($line_comment)),
      ({match:api("pulls/" + $n + "/comments"), reviews:"comments"} + until_posted($line_comment)),
      $others.rest[],
      {match:$others.heads["check-runs"], head_read:"check-runs",
       stdout:{total_count:2, check_runs:[{name:"test", status:"completed", conclusion:"success"}, {name:"lint", status:"completed", conclusion:"success"}]}},
      # The check suites of the head, one per workflow run, which red_fixture writes from the runs
      # of its world; this world holds no run.
      {match:$others.heads["check-suites"], head_read:"check-suites", unmodeled:true},
      {match:$others.heads.status, stdout:combined_status($head)},
      {match:$others.heads.statuses, stdout:[]},
      # The repository, its commits, trees, refs and root contents, and its one pull request.
      {match:api(""), stdout:(repository + {default_branch:"main"})},
      {match:("^gh repo view(?: (?:[^/\\s]+/)?" + ($repo | literal) + ")?(?=.*\\s--json\\s)"),
       stdout:{name:$name, nameWithOwner:$repo, owner:{login:$owner},
               url:("https://github.com/" + $repo), defaultBranchRef:{name:"main"}, isPrivate:false,
               visibility:"PUBLIC"}},
      {match:api("commits/" + $others.forms.base), stdout:($commits[0])},
      {match:api("commits/" + $others.forms.head), stdout:($commits[1])},
      {match:api("git/trees/" + $others.forms.base), stdout:tree_of($base_sha)},
      {match:api("git/trees/" + $others.forms.head), stdout:tree_of($head)},
      {match:api("git/refs?/heads/main"), stdout:ref_of($base_sha; "main")},
      {match:api("git/refs?/heads/" + ($ref | literal)), stdout:ref_of($head; $ref)},
      ({match:api("git/ref/heads/\\S+")} + github("gitref-404")),
      ({match:api("git/refs/heads/\\S+")} + github("gitrefs-404")),
      {match:rest("GET"; "contents/?(?:\\?(?!\\S*\\bref=)\\S*)?"; json_only), stdout:contents($base_sha)},
      {match:api("contents/?\\?(?:\\S*&)?ref=" + $others.forms.base + "(?:&\\S*)?"), stdout:contents($base_sha)},
      {match:api("contents/?\\?(?:\\S*&)?ref=" + $others.forms.head + "(?:&\\S*)?"), stdout:contents($head)},
      {match:api("branches/" + ($ref | literal | gsub("/"; "(?:/|%2[Ff])"))),
       stdout:{name:$ref, commit:{sha:$head, url:("https://api.github.com/repos/" + $repo + "/commits/" + $head)},
               protected:false}},
      ({match:api("branches/(?!main(?:[/?\\s]|$))[^/?\\s]+")} + github("branch-404")),
      {match:api("rules/branches/(?!main(?:[?\\s]|$))[^?\\s]+"), stdout:[]},
      # GitHub serves no list of check runs and no `runs` under a repository (workflow runs are
      # under actions/runs, check runs under check-runs and a commit), so a read of either answers
      # its 404 for a route it does not have (round 6 trial-c-ablate-3 read check run 9001008 so).
      ({match:api("check-runs")} + github("checkruns-list-404")),
      ({match:api("runs(?:/\\S*)?")} + github("noroute-404")),
      # The diff as GitHub serves it, which ends with the newline git ends a diff with ($diff is
      # read through $(…), which drops it); gh api prints a body as it stands.
      {match:rest("GET"; "pulls/" + $n;
                  "(?=.*\\s--header\\s[Aa]ccept:\\u001f*application/vnd\\.github(?:\\.v3)?\\.diff(?:\\s|$))"),
       stdout:($diff + "\n")},
      {match:api("pulls/" + $n + "/files"), stdout:$files},
      {match:api("pulls"), head_read:"pulls", stdout:[$pull], program:$pulls_query},
      {match:api("pulls/" + $n), head_read:"pull", stdout:$pull},
      {match:rest("PATCH"; "pulls/" + $n; json_only), head_read:"pull", stdout:$pull},
      # Another pull request number, its reviews or its comments: the 404 of GitHub (measured).
      ({match:api("pulls/(?!" + $n + "(?:[/?\\s]|$))\\d+")} + github("pull-404")),
      ({match:api("pulls/(?!" + $n + "/)\\d+/reviews")} + github("reviews-404")),
      ({match:api("issues/(?!" + $n + "/)\\d+/comments")} + github("comments-404")),
      {match:"^gh api GET user(?:\\s|$)", stdout:{login:"rig[bot]"}}
    ]}' >"$R/fixtures.json"
  # The world the scorer reads: head is the PR's head, the implementer's code commit. It names
  # the base the pull request merges into, its commit, and the pull request's title, which
  # red_fixture reads here.
  jq -n --arg key "$worker_key" --arg repo "$worker_repo" --argjson pr "$worker_pr" --arg head "$C1" \
    --arg base_sha "$C0" \
    '{key:$key, repo:$repo, pr:$pr, branch:("legion/" + $key), head:$head, base:"main",
      base_sha:$base_sha, title:("feat: greet() (" + $key + ")")}' >"$R/world.json"
}

# worker_world INDEX START [WORLD] seeds one worker run: its own Dispatch and issue, the
# worker_fixture world, that world's red where WORLD is given (red_fixture), and the implement
# record the role's `Handoff:` line names. It leaves `worker_key`, `version`, `pr`, `head` and
# `check` set for the caller, read from world.json in one jq, `check` being `null` where the
# world has none.
worker_world() {
  local index=$1 start=$2 world=${3:-} gate=() facts
  # A world whose role must reach an approval, the reviewer whose round goes on (red_world), takes
  # its check as `after_merge`, since this rig's Dispatch reaches no GitHub and so can place no
  # commit, which leaves every `before_merge` check without a standing pass and makes a reviewer
  # refuse every approval it is asked for (seed.ts testerProofIssue says what was measured). A
  # reviewer whose round ends in a changes request is not held up by the gate. The issue is seeded
  # before red_fixture writes the world's facts to world.json, so they are read from red_world.
  if [ -n "$world" ]; then
    facts=$(red_world "$world") || fail "red_world: '$world' is no world of red-at-the-head"
    if jq -e '.role == "reviewer" and .round == "goes-on"' <<<"$facts" >/dev/null; then gate=(after-merge-check); fi
  fi
  own_dispatch project
  seed_ts tester-proof "$R/fixture.json" "${gate[@]}"
  worker_key=$(jq -r .issue "$R/fixture.json")
  worker_fixture "$index" "$start" >>"$R/fixture.log" 2>&1 || fail "the fixture failed; see $R/fixture.log"
  [ -z "$world" ] ||
    red_fixture "$world" >>"$R/fixture.log" 2>&1 || fail "the world $world fixture failed; see $R/fixture.log"
  # The routes the gh stand-in answers from, parsed once as it parses them, before any agent
  # boots: a file it refuses, a `follows` that names no route's `match` among them, would answer
  # every `gh` call of the run with that refusal, so it fails the run here instead, a rig error.
  SKILL_SCENARIO_RUN=$R bun "$here/gh-standin.ts" --check >>"$R/fixture.log" 2>&1 ||
    fail "the gh stand-in refuses $R/fixtures.json; see $R/fixture.log"
  # `check` is last, so `read` keeps a check name with spaces in it whole.
  read -r pr head check < <(jq -r '"\(.pr) \(.head) \(.check)"' "$R/world.json")
  seed_ts implement-handoff "$R/fixture.json" "$R/implement.json" "$head"
  version=$(jq -r .implement.version "$R/fixture.json")
}

# boot_worker ROLE writes the pane the label's checkout's daemon would give ROLE: the run's own
# Legion project and boot secret, the daemon stand-in on a port the kernel assigns, the base
# environment and the stand-ins, and the pane file the launch reads. The caller launches.
boot_worker() {
  local role=$1
  state=$R/state
  # Each run its own Legion project, so concurrent runs claim distinct role tokens on one listener.
  # A project token is lower-case letters and digits only (isLegionProjectToken), so the label's
  # hyphens go and `r` separates it from the run number.
  project=skills${P##*/}r$n
  project=${project//-/}
  mkdir -p "$state/gh" "$state/secrets"
  chmod 0700 "$state/secrets"
  (umask 077 && openssl rand -hex 16 >"$state/secrets/boot")
  printf '[user]\nname = "Rig Worker"\nemail = "rig@example.invalid"\n[ui]\npaginate = "never"\n' >"$R/jj.toml"
  # Port 0: the kernel assigns the stand-in's port, which it prints, so concurrent runs never race.
  start_process daemon bun "$here/daemon-standin.ts" 0 "$R/daemon.log" "$state/secrets/boot" "$project" "$worker_key" "$role"
  # The bound on the daemon stand-in's start. Measured on 2026-11-17 in an issue pod with two
  # CPUs: with the pod quiet, five starts of `bun daemon-standin.ts` at this head listened 8.1 to
  # 9.3 s after they started, with a warm Go build cache. In the tester's round-5 batch (170 runs,
  # two at a time, 2026-11-17), the 30 s bound stopped 10 runs. 161 starts listened, at a median
  # of 15.6 s and a 95th percentile of 28.4 s; the slowest of them listened at 38.5 s, after the
  # bound had already stopped that run (trial-j-ablate-1), and the other 9 had not listened when
  # the bound stopped them. Ninety seconds is about ten times the slower quiet start and more than
  # twice the slowest loaded one, so a loaded pod still starts its runs, and a stand-in that hangs
  # still fails its run in under two minutes.
  await_start daemon "$daemon_pid" 90 "the daemon stand-in to listen" grep -q '^listening on ' "$R/logs/daemon.log"
  port=$(sed -n 's|^listening on http://127\.0\.0\.1:||p' "$R/logs/daemon.log")
  base_env
  standins
  # The pane the label's checkout's daemon would give the role, over the base every agent gets:
  # worker.env and system-args. standins runs first, since worker-pane.ts names its $R/bin/gh, the
  # recording stand-in, as the pane's real gh.
  bun "$here/worker-pane.ts" "$R" "$R/pane.env" "$port" "$profile" "$project" "$worker_key" "$role" "$co"
  {
    cat "$R/worker.env" "$services_env"
    echo "DISPATCH_URL=http://127.0.0.1:$dispatch_port"
    echo "DISPATCH_TOKEN_FILE=$work/services/dispatch-token"
    echo "GIT_CONFIG_COUNT=0"
    echo "JJ_USER=Rig Worker"
    echo "JJ_EMAIL=rig@example.invalid"
    echo "JJ_CONFIG=$R/jj.toml"
    echo "GH_REPO=$worker_repo"
  } >"$R/pane.env"
}

# record_turn N WAKE copies the turn's output to out-<N>.txt and appends its line to turns.txt.
record_turn() {
  printf '%s %s %s %s\n' "$1" "$2" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$(tail -n 1 "$R/out.txt")" >>"$R/turns.txt"
  cp "$R/out.txt" "$R/out-$1.txt"
}

# architect_assignment ROLE [TOLD] writes to $R/prompt.txt the architect's assignment of ROLE's
# phase (tester or reviewer) on the run's pull request, with the Handoff line the daemon's task
# names the predecessor's record in (packages/daemon/internal/workflow/effects.go), and TOLD, a
# sentence of what the daemon reports, after the head. The issue holds an implement record and no
# test record, since no tester ran here, so the reviewer's `Handoff:` line names that implement
# record and its version too; the daemon's own reviewer task would name the test record the tester
# wrote. It reads the caller's `worker_key`, `pr`, `head` and `version` (worker_world).
architect_assignment() {
  local from ask
  case $1 in
  tester) from=implementer ask='Test it and finish your phase.' ;;
  reviewer) from=tester ask="Tester's verdict: pass. Review it and finish your phase." ;;
  *) fail "architect_assignment: '$1' is not tester or reviewer" ;;
  esac
  printf 'Architect → %s, %s. The %s has finished: PR #%s (%s, repository %s) is at %s.%s Handoff: implement version %s (dispatch handoff-read --issue %s --phase implement --version %s). %s\n' \
    "$1" "$worker_key" "$from" "$pr" "https://github.com/$worker_repo/pull/$pr" "$worker_repo" "$head" "${2:+ $2}" \
    "$version" "$worker_key" "$version" "$ask" >"$R/prompt.txt"
}

run_tester_proof() {
  local name=$1 n=$2 index=$3 start=$4 state project port pr head check version
  # The run's own Dispatch, and its own issue on it, whose newest implement record the tester
  # reads and whose newest test record it writes.
  worker_world "$index" "$start"
  architect_assignment tester
  boot_worker tester
  launch "$name"
  # What the run left on its issue: every handoff record, each with its body, for testerProof.
  seed_ts capture-handoffs "$R/fixture.json" "$R"
}

# red_world WORLD prints the facts of WORLD as one JSON object, the one place each world's facts are
# written: red_fixture adds them to <run>/world.json, where run_red_at_the_head and score.ts's
# redAtTheHead read them, and worker_world reads the two it seeds the run's issue by. WORLD outside
# the twelve prints nothing and fails, which is how cmd_run, cmd_batch and red_fixture refuse it:
# the table below is the shell's one list of the worlds. The facts:
#   after         what the head settles at once the re-run is pressed, green or red
#   role          the role that holds the phase: the reviewer in `i` and `j`, the tester in the rest
#   phase         that role's phase, as the daemon's task names it (testing or reviewing)
#   handoff       the phase of the handoff record that role writes, test or review (phase.Handoff,
#                 packages/daemon/internal/phase/phase.go)
#   rerun         whether the role writes the red down and re-runs it once. `d`, `e` and `f` re-run
#                 nothing: in `d` the failing test is of the package the change touches, in `e` a
#                 run of that head whose `run_attempt` is 2 has already spent the head's one
#                 re-run, and in `f` the title check judges the pull request rather than its code.
#                 `k` and `l` re-run as `a` does: the failure of `k` that would spend the head's one
#                 re-run is at the commit and in no counted set, and the counted set of `l` reports
#                 its red once the rig does
#   turns         2 in a world that re-runs, the second turn on the wake the re-run's settlement (in
#                 `l`, the rig's report of the check) brings; 1 in a world that re-runs nothing
#   round         how the role must end its round: goes-on where the re-run clears the red, and
#                 fails-naming-check where it comes back red; fails in `d` and `f`, and fails-spent
#                 in `e`, whose words must say the head's one re-run is spent
#   disposal      whether the role owes the three steps of the page's "What becomes of the record":
#                 its red is a test of a package the change does not touch, the re-run cleared it,
#                 and the log read gave the test's full name. The page's own words are "each run
#                 that cleared a red searches for the test's issue", so `b` and `j` clear nothing,
#                 `c`'s red is a tool download and no test, `d`'s test is the change's own, `e`
#                 re-ran nothing, `f` is the title check, and in `h` the log read answered an
#                 error, so the role never read the name the search is on
#   report        whether the counted set has not reported the red when the role first reads it,
#                 and reports it only when the rig does, between the two turns (red_report): `l`
#   decline_fails whether a round the role ended before its re-run fails the world: `k`, whose
#                 commit endpoint answers a by-hand failure that reads as the re-run already spent
#   aggregate     whether the head carries, beside the failing lane and in the same run, the
#                 aggregate check `ci` the base requires, red over the lane and settled with it:
#                 `i` and `j`
#   spent         whether the head's one re-run is already spent: the head's runs carry a second
#                 attempt of the e2e run somebody already pressed, and the commit endpoint carries
#                 the superseded e2e failure and its passing re-run: `e`
#   by_hand       whether the commit endpoint carries, beside the counted failure, a by-hand
#                 `workflow_dispatch` run's failure of the same check name, which no counted set
#                 and none of the head's `pull_request` runs hold: `k`
#   over_lane     whether the world's red check is itself the aggregate over a failing lane, the
#                 job `job` names, whose own check run is red beside it: `g`. With `aggregate`
#                 (`i` and `j`, the red lane under the aggregate) it marks the worlds whose red
#                 is one aggregate over one lane, which the scorer's check fact reads
red_world_table() {
  printf '%s\n' '
    {
      a: {after:"green", role:"tester",   rerun:true,  round:"goes-on",            disposal:true,  report:false, decline_fails:false, aggregate:false, spent:false, by_hand:false, over_lane:false},
      b: {after:"red",   role:"tester",   rerun:true,  round:"fails-naming-check", disposal:false, report:false, decline_fails:false, aggregate:false, spent:false, by_hand:false, over_lane:false},
      c: {after:"green", role:"tester",   rerun:true,  round:"goes-on",            disposal:false, report:false, decline_fails:false, aggregate:false, spent:false, by_hand:false, over_lane:false},
      d: {after:"red",   role:"tester",   rerun:false, round:"fails",              disposal:false, report:false, decline_fails:false, aggregate:false, spent:false, by_hand:false, over_lane:false},
      e: {after:"red",   role:"tester",   rerun:false, round:"fails-spent",        disposal:false, report:false, decline_fails:false, aggregate:false, spent:true,  by_hand:false, over_lane:false},
      f: {after:"red",   role:"tester",   rerun:false, round:"fails",              disposal:false, report:false, decline_fails:false, aggregate:false, spent:false, by_hand:false, over_lane:false},
      g: {after:"green", role:"tester",   rerun:true,  round:"goes-on",            disposal:true,  report:false, decline_fails:false, aggregate:false, spent:false, by_hand:false, over_lane:true},
      h: {after:"green", role:"tester",   rerun:true,  round:"goes-on",            disposal:false, report:false, decline_fails:false, aggregate:false, spent:false, by_hand:false, over_lane:false},
      i: {after:"green", role:"reviewer", rerun:true,  round:"goes-on",            disposal:true,  report:false, decline_fails:false, aggregate:true,  spent:false, by_hand:false, over_lane:false},
      j: {after:"red",   role:"reviewer", rerun:true,  round:"fails-naming-check", disposal:false, report:false, decline_fails:false, aggregate:true,  spent:false, by_hand:false, over_lane:false},
      k: {after:"green", role:"tester",   rerun:true,  round:"goes-on",            disposal:true,  report:false, decline_fails:true,  aggregate:false, spent:false, by_hand:true,  over_lane:false},
      l: {after:"green", role:"tester",   rerun:true,  round:"goes-on",            disposal:true,  report:true,  decline_fails:false, aggregate:false, spent:false, by_hand:false, over_lane:false}
    }'
}
red_world() {
  jq -nce --arg world "$1" "$(red_world_table)"'[$world] // empty
    | . + {phase:{tester:"testing", reviewer:"reviewing"}[.role],
           handoff:{tester:"test", reviewer:"review"}[.role],
           turns:(if .rerun then 2 else 1 end)}'
}

# red_worlds prints red_world's worlds as a refusal names them, `a, b, … k or l`, from its table,
# so a world added there is named in every refusal with nothing else to edit.
red_worlds() {
  jq -nr "$(red_world_table)"' | keys_unsorted | (.[:-1] | join(", ")) + " or " + .[-1]'
}

# red_fixture WORLD turns worker_fixture's green head red, over the world it has already written:
# the change is still greet.ts and greet.test.ts, so the repository root is the one package the
# change touches and every other directory is one it does not. The head reads red in all four
# places an agent reads it — the `gh pr view` rollup, `gh pr checks`, the GraphQL
# `statusCheckRollup` of the head, and the commit's check runs — and every call
# skills/legion-worker/references/red-at-the-head.md tells a role to make is answered: the head's
# own check runs, the re-run, the read of the failing attempt's log, the head's runs and the
# failing run's jobs. The GraphQL world is worker_fixture's own (gh-graphql.ts), whose head the jq
# below turns red where it stands, so a rollup read of the head by any name GraphQL resolves it by
# answers the counted set, and a read of any other commit answers that commit's own.
# The first three of those reads are the counted set, what GitHub counts for the pull request;
# the commit's check-runs endpoint answers every run at that commit instead, which in world `k`
# is one run more and in world `l` one run the counted set has not reported. The head's runs read
# is neither: it answers the head's own `pull_request` workflow runs with their `run_attempt`,
# which is where the page reads whether the head's one re-run is already spent — attempt 1 in
# every world but `e`, whose head carries a second attempt somebody already pressed.
# The head has one state per act, never one per read: every read of it answers a sequence, red and
# then what the world settles at, and each of them follows the re-run's route rather than its own
# calls (gh-standin.ts, `follows`), so the head reads red however many times a role reads it
# before the re-run, and the world's own answer from the moment the re-run is pressed, and
# `gh pr checks` exits 1 while a check it reports is red and 0 when none is, as the real command
# does. The merge state of the pull request follows the checks, as GitHub's MergeStateStatus
# states it (https://docs.github.com/en/graphql/reference/enums#mergestatestatus: BLOCKED "The
# merge is blocked.", UNSTABLE "Mergeable with non-passing commit status.", CLEAN "Mergeable and
# passing commit status."), with a required status check that has not passed blocking the merge
# (https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-protected-branches/about-protected-branches#require-status-checks-before-merging):
# BLOCKED while a check the base requires is missing or red, UNSTABLE while it is green and
# another check is red, CLEAN when none is red; the world's protection requires no review, so
# checks alone decide it (merge_state below). Per world, from its facts: the heads of `a` to `f`,
# `h`, `k` and `l` report no `ci`, the one check their base requires, so each reads BLOCKED before
# and after the re-run; `g` and `i` read BLOCKED while `ci` is red and CLEAN once the re-run
# settles it and every lane green; `j` stays BLOCKED, its `ci` red again on the re-run; and no
# world reads UNSTABLE, since none has `ci` green beside a red lane. The base
# requires the one aggregate check `ci`, and every read of its required set says so:
# `gh pr checks --required` answers the checks the base requires alone, as GitHub does, and the
# rules of the base's rulesets and its branch protection, the two reads the reviewer's prompt
# gives for that set, each name `ci` in the shape GitHub answers them in, as do the protection
# endpoint of the branch, its required status checks and their contexts, and the repository's
# rulesets and its one ruleset (required_set_fixture, which red_fixture runs last, so those seven
# reads go ahead of every other route). The heads of worlds `g`, `i` and `j` report `ci`, and at
# every other head the `--required` read answers gh's `no required checks reported on the
# '<branch>' branch` and exits 1. In a world that stays red both answers are red, and nothing of it
# moves; in a world that presses no re-run (`d`, `e`, `f`) the head reads red for the whole run.
# World `l`'s counted reads answer three states, the first of them reporting no red at all, and
# each route that answers them carries the mark `unreported`: red_report drops that first state of
# each marked route between the two turns, which is the rig reporting the check. A read of
# another commit's check runs or status takes the routes other_commit_routes wrote ahead of the
# head's, and the head's combined status answers GitHub's `pending` with no status, since no
# status was posted to it. The re-run's route carries the settlement the head's checks would raise
# later, as the envelope the gh stand-in publishes the moment the role presses the re-run. Only a
# re-run of the failed jobs of the run the red belongs to (`--failed`), or of the red's own job
# (`--job`), presses it, as GitHub re-runs the jobs that depend on that one; gh's help and its
# errors for a call naming no run, or a run or a job GitHub has no run or job for, are answered
# as gh answers them, and move neither the head nor the count the head's reads follow; a re-run
# of all the jobs of a run, of another job of the head or of another run of the head makes a state
# this world does not hold, and is not modeled. A read of the runs of another commit or branch
# answers no run.
# The twelve worlds, each the same change under a different red:
#   a  a failing test of packages/notify, a package the change does not touch; green on the re-run
#   b  that test, red again on the re-run
#   c  a job that failed at a tool download with HTTP 504, a step that reads no file the change
#      touches; green on the re-run
#   d  a failing test of the repository root, the package the change does touch
#   e  a's red at a head whose one re-run is already spent: the head's own runs carry a second
#      attempt of the e2e workflow, the re-run somebody pressed, which is where the page reads the
#      spend. The e2e check runs stay in the commit endpoint beside it — an earlier run that
#      failed and a later one that passed — since the page lets no role judge the spend there, so
#      a world that still carries them measures a role that judged elsewhere
#   f  the title check red, its output naming the pull request's own title; it judges the pull
#      request rather than its code, and runs in a workflow of its own named `title`, as this
#      repository's pr-title.yaml runs one, beside the `ci` run of lint and typecheck
#   g  an aggregate check, ci, red over a's failing lane, whose own check run is red beside it
#   h  a, with the read of the failing attempt's log answering an error
#   i  a with a reviewer, and beside the failing lane, in the same run, the aggregate check ci red
#      over it, as on cybermonklabs/legion's own base, which requires the aggregate ci-ok over its
#      test lanes: the red the reviewer meets is one the base requires. The re-run settles the
#      aggregate as it settles the lane, green
#   j  b with a reviewer and i's aggregate: the re-run leaves the lane and the aggregate red again
#   k  a, with a second failing run of that same check name at the commit, a by-hand
#      `workflow_dispatch` run's, which neither the counted set nor the head's `pull_request`
#      runs hold: a role that judges the head's one re-run as spent from the commit endpoint
#      presses nothing, and one that judges it on the head's own runs re-runs
#   l  a, with a counted set that does not report the red when the role first reads it, although
#      the commit endpoint already holds its failing run: the role waits for the pull request's
#      checks event, and the rig reports the check between the two turns (red_report)
# red_fixture runs the parts below in order: red_model prints the model of the world's red,
# run_routes the routes of the head's runs, jobs and check runs from it, the jq here rewrites the
# reads of the head worker_fixture marked, and required_set_fixture puts the reads of the base's
# required set ahead of every route. The model and the routes go through files of the run, which
# red_fixture removes once it has written the world: a world's routes are more than one argument
# can carry (fixtures.json is 195 to 233 KB; Linux takes 128 KB in one argument).
red_fixture() {
  local world=$1 model=$R/red-model.json routes=$R/red-routes.json required base base_sha
  red_model "$world" >"$model" || fail "red_fixture: no model of world '$world'"
  run_routes "$model" >"$routes" || fail "red_fixture: no routes of world '$world'"
  jq --slurpfile model "$model" --slurpfile routes "$routes" "$(red_model_defs)$(route_defs)"'
    $routes[0] as $new
    # The reads of the head worker_fixture marks `head_read` answer the red where they stand, and
    # what the head settles at once the re-run is pressed: the pull request (its rollup and its
    # merge state, through gh and through REST), its checks, the GraphQL world (the rollup of the
    # head and the merge state of the pull request), and its check runs. The commit endpoint answers
    # every run at the head, the by-hand one among them, and the attempt each re-run made once it
    # is pressed. The check suites of the head are one of GitHub Actions per run of the head, each
    # with what the run holds: its suite id and node id, branch, commit, status and conclusion,
    # pull requests and times. A field of a suite a run does not hold (before, after, its count of
    # check runs, whether it can be re-requested) is in no answer.
    | def check_suite: {id:.check_suite_id, node_id:.check_suite_node_id, head_branch,
        head_sha, status, conclusion, url:($api + "/check-suites/" + (.check_suite_id|tostring)),
        pull_requests, app:{id:15368, slug:"github-actions"}, created_at, updated_at,
        check_runs_url:($api + "/check-suites/" + (.check_suite_id|tostring) + "/check-runs")};
      .routes |= map(
        if .head_read == "pr"
        then (.stdout as $view
              | . + counted({stdout:($view + {statusCheckRollup:rollup(.), mergeStateStatus:merge_state(.)})})
              | .follows = $rerun | del(.stdout))
        elif .head_read == "pull"
        then (.stdout as $pull
              | . + counted({stdout:($pull + {mergeable_state:(merge_state(.) | ascii_downcase)})})
              | .follows = $rerun | del(.stdout))
        elif .head_read == "pulls"
        then (.stdout as $pulls
              | . + counted(merge_state(.) as $state
                            | {stdout:($pulls | map(. + {mergeable_state:($state | ascii_downcase)}))})
              | .follows = $rerun | del(.stdout))
        # `gh pr checks --required` answers the checks the base requires and no other, as GitHub
        # does, from a route of its own ahead of the one that answers every check. The base
        # requires the one aggregate check `ci` ($required), which the heads of worlds `g`, `i`
        # and `j` report: every other head reports no check the base requires, which gh answers on
        # stderr, exiting 1 (\u0027 is the quote gh puts around the branch: this program is in
        # single quotes).
        elif .head_read == "checks"
        then (def answer($checks):
                {stdout:rows($checks), exit:(if (failing($checks)|length) > 0 then 1 else 0 end)};
              ({match:(.match + "(?=.*\\s--required(?:\\s|$))"), follows:$rerun}
               + counted(map(select(.name == $required))
                 | if . == []
                   then {stderr:("no required checks reported on the \u0027" + $branch
                                 + "\u0027 branch\n"), exit:1}
                   else answer(.) end)),
              (. + counted(answer(.)) | .follows = $rerun | del(.stdout)))
        elif .head_read == "graphql"
        then (.stdout as $world
              | . + counted(. as $checks
                            | {stdout:($world
                                       | .["$refs"].head.statusCheckRollup = graphql_rollup($checks)
                                       | .["$refs"].pr.mergeStateStatus = merge_state($checks))})
              | .follows = $rerun | del(.stdout))
        elif .head_read == "check-runs"
        then (. + {follows:$rerun,
                   replies:[{stdout:{total_count:(($red_checks + $uncounted)|length),
                                     check_runs:($red_checks + $uncounted)}},
                            {stdout:{total_count:(($settled_checks + $uncounted)|length),
                                     check_runs:($settled_checks + $uncounted)}}]}
              | del(.stdout))
        elif .head_read == "check-suites"
        then (moving(.match; [0, 1] | map(. as $state
                | [$runs[].states[$state].run | check_suite] as $suites
                | {stdout:{total_count:($suites|length), check_suites:$suites}}))
              + {head_read:"check-suites"})
        else . end)
    # The answers for a repository other than the world (`repo_refusal`, worker_fixture) stand
    # ahead of every route, as GitHub refuses such a read whatever it asks.
    | .routes = ([.routes[] | select(.repo_refusal)] + $new + [.routes[] | select(.repo_refusal | not)])
    # The envelope red_report publishes in a world whose counted set the rig reports (world `l`),
    # the settlement the head would raise when the counted set reports the red.
    | . + (if $facts.report then {reported:published($shown)} else {} end)' \
    "$R/fixtures.json" >"$R/fixtures.red.json"
  if jq -e '.facts.report' "$model" >/dev/null; then
    jq '.reported' "$R/fixtures.red.json" >"$R/reported.json"
    jq 'del(.reported)' "$R/fixtures.red.json" >"$R/fixtures.json"
    rm -f "$R/fixtures.red.json"
  else
    mv "$R/fixtures.red.json" "$R/fixtures.json"
  fi
  # The reads of the base's required set, which read nothing of the world but the repository and
  # the base, go ahead of every route the jq above wrote, on the check the base requires.
  IFS=$'\t' read -r required base base_sha < <(jq -r '[.required, .base, .base_sha] | @tsv' "$model")
  required_set_fixture "$required" "$base" "$base_sha"
  # The world the scorer reads, beside worker_fixture's: the red check, the run and job that carry
  # it, the failing line the world's log read answers (`log`, empty where that read answers an
  # error), where the red is a test, that test's full name (`test`, empty where it is none), the
  # check paired with the red where the head carries an aggregate over a lane (`partner`: the
  # aggregate beside the red lane in `i` and `j`, the lane under the red aggregate in `g`; no
  # partner in any other world), and the world's facts (red_world). score.ts's redAtTheHead judges
  # the role's record against `log` and its `dispatch search` against `test`, so that neither rests
  # on how the role printed its own log read, and a record's check against `check` and `partner`.
  jq --slurpfile model "$model" '$model[0] as $m
    | . + {world:$m.world, check:$m.check, job:$m.job, run_id:$m.run_id, job_id:$m.job_id,
           log:$m.world_log, test:$m.test_name}
    + (if $m.partner == "" then {} else {partner:$m.partner} end) + $m.facts' \
    "$R/world.json" >"$R/world.red.json"
  mv "$R/world.red.json" "$R/world.json"
  rm -f "$model" "$routes"
}

# red_defs prints what red_model, run_routes and red_fixture's rewrite share, as a jq prefix over
# the facts of the red, which the program that takes it has bound ($repo, $pr, $head, $partner,
# $run_id, $required): the repository's API, whether the red has a partner, the latest attempt of
# each job of a run, a check suite's id, the counted set's verdicts and merge state, the
# settlement the re-run publishes, and a run and a job in the shape gh answers them. It ends in
# definitions, so the program that takes it goes on with its own.
red_defs() {
  printf '%s\n' '
    ("https://api.github.com/repos/" + $repo) as $api
    | ($partner != "") as $paired
    # The jobs of a state of a run as its jobs read answers them: the latest attempt of each job.
    | def latest: .attempts as $attempts
        | reduce $attempts[1:][] as $later ($attempts[0];
            map(. as $j | ([$later[] | select(.name == $j.name)] | first) // $j));
      # The check suite of a run: a rig id made as the run ids are, the run id less a million.
      def check_suite_id($run): $run - 1000000;
    # One state of the counted set as its reads answer it: the `gh pr view` rollup, the rows of
    # `gh pr checks`, and the GraphQL rollup of the head (below), all over the latest counted run
    # of each name. The link each carries is the details_url of the check run, the page of the job
    # that ran it, which is the link GitHub gives a check run of GitHub Actions and the one
    # `gh pr checks` prints. The merge state of the pull request follows from them and from the
    # check the base requires.
    def failing($checks): $checks | map(select(.conclusion == "failure") | .name) | sort;
    def passing($checks): $checks | map(select(.conclusion == "success") | .name) | sort;
    def verdict: if .conclusion == "failure" then "FAILURE" else "SUCCESS" end;
    def rows($checks): $checks
      | map(.name + "\t" + (if .conclusion == "failure" then "fail" else "pass" end)
            + "\t20s\t" + .details_url)
      | join("\n");
    def merge_state($checks): ($checks | map(select(.name == $required))) as $requires
      | if ($requires | length) == 0 or any($requires[]; .conclusion != "success") then "BLOCKED"
        elif any($checks[]; .conclusion == "failure") then "UNSTABLE" else "CLEAN" end;
    def run_url: "https://github.com/" + $repo + "/actions/runs/" + ($run_id|tostring);
    # The settlement as the Envoy listener renders one (packages/envoy/internal/cistore/render.go),
    # and the envelope that carries it to the listener of the run.
    def settlement($checks):
      {kind:"checks", repo:$repo, number:($pr|tostring), sha:$head,
       check_runs:($checks | map({name:.name, id:.id}) | sort_by(.name)),
       generation:2, snapshot:$head[0:8],
       failed:{count:(failing($checks)|length), checks:failing($checks)},
       running:{count:0, checks:[]},
       passed:{count:(passing($checks)|length), checks:passing($checks)},
       queued:{count:0, checks:[]},
       cancelled:{count:0, checks:[]},
       skipped:{count:0, checks:[]},
       failing_checks:(failing($checks) | map({name:., url:run_url}))};
    def published($checks):
      settlement($checks) as $settled
      | ($repo | split("/")) as [$owner, $repo_name]
      | {topic:("notifications.github." + $owner + "." + $repo_name + ".pr."
                + ($pr|tostring) + ".checks"),
         source:"github",
         message:("checks settled on " + $repo + "#" + ($pr|tostring) + " @ " + $head[0:7]
                  + ": " + ($settled.passed.count|tostring) + " passed, "
                  + ($settled.failed.count|tostring) + " failed, 0 cancelled, 0 skipped"),
         payload:($settled|tojson)};
    # A run and a job in the shape `gh run view --json` and `gh run list --json` answer them, with
    # the field names gh 2.98.0 lists for those flags.
    def gh_job: {completedAt:.completed_at, conclusion, databaseId:.id, name,
      startedAt:.started_at, status, steps:(.steps | map({conclusion, name, number, status})),
      url:.html_url};
    def gh_run($jobs): {attempt:.run_attempt, conclusion, createdAt:.created_at, databaseId:.id,
      displayTitle:.display_title, event, headBranch:.head_branch, headSha:.head_sha,
      jobs:($jobs | map(gh_job)), name, number:.run_number, startedAt:.run_started_at, status,
      updatedAt:.updated_at, url:.html_url, workflowDatabaseId:1, workflowName:.name};
'
}

# red_model WORLD prints the model of the red of WORLD as one JSON object, from worker_fixture's
# world ($R/world.json) and the world's facts (red_world): the facts of the red (its check, job,
# failing line, test and partner, its run and job ids, the check the base requires), the runs of
# the head, each in its two states, before the re-run and after it, with their jobs, and the check
# runs of the head in each state, the counted set among them as its reads answer it.
red_model() {
  local world=$1 facts pr head branch check job step summary log log_gone test_name world_log
  local run_id job_id red_id lanes required base base_sha title run_name partner partner_summary
  facts=$(red_world "$world") || fail "red_fixture: '$world' is no world of red-at-the-head"
  # The pull request, its head and branch, the base it merges into and the base commit, and its
  # title, which every run of the head carries as its display title: worker_fixture's world.
  IFS=$'\t' read -r pr head branch base base_sha title < <(jq -r \
    '[.pr, .head, .branch, .base, .base_sha, .title] | @tsv' "$R/world.json")
  [ -n "$base_sha" ] || fail "red_fixture: $R/world.json names no base commit"
  run_id=$((7000000 + pr))
  job_id=$((8000000 + pr))
  red_id=$((9000000 + pr))
  log_gone=false
  # The workflow the run of the red belongs to: `ci`, which holds every job of the head, in every
  # world but `f`, whose title check runs in a workflow of its own (below).
  run_name=ci
  # The words of an aggregate check with one failed lane under it: world `g`'s red check, and the
  # aggregate beside the red lane of `i` and `j`.
  lanes='1 of 6 lanes failed: test (notify).'
  # The worlds whose red is not a's. Every other world red_world knows carries a's red, the
  # failing test of packages/notify, and red_world has already refused a world it does not know,
  # so the catalogue of worlds is red_world's alone.
  case $world in
  c)
    check='build (linux-x64)'
    job='build (linux-x64)'
    test_name=''
    step='Set up Bun'
    summary='The job failed while it installed its tools.'
    log='Error: Failed to download https://bun.sh/download/bun-v1.4.4/bun-linux-x64.zip: HTTP 504 Gateway Timeout'
    ;;
  d)
    check='test (widgets)'
    job='test (widgets)'
    test_name='greet.test.ts > trims the name'
    step='Run bun test .'
    summary='1 of 2 tests in the repository root failed.'
    log='(fail) greet.test.ts > trims the name [1.00ms]'
    ;;
  f)
    check='title'
    job='title'
    test_name=''
    step='Check the pull request title'
    summary="The title \"feat: greet() ($worker_key)\" has no scope: the pattern is <type>(<scope>): <subject>."
    log=$summary
    # The title check runs in a workflow of its own, named for it, as this repository's
    # pr-title.yaml runs one: the run and the job are the check's own, and the `ci` run that holds
    # lint and typecheck is another run of the head.
    run_name=title
    ;;
  g)
    check='ci'
    job='test (notify)'
    test_name='notify > retries a refused send'
    step='Run bun test packages/notify'
    summary=$lanes
    log='(fail) notify > retries a refused send [3.00ms]'
    ;;
  *)
    check='test (notify)'
    job='test (notify)'
    test_name='notify > retries a refused send'
    step='Run bun test packages/notify'
    summary='1 of 412 tests in packages/notify failed.'
    log='(fail) notify > retries a refused send [3.00ms]'
    # The log of attempt 1 of the run of the red is past GitHub's retention in world `h`, so each
    # read of it answers the 410 GitHub answers for such a log (log_gone below).
    [ "$world" != h ] || log_gone=true
    ;;
  esac
  # The check the base requires: the one aggregate check `ci`, which every read of the required set
  # of the base answers, `gh pr checks --required` (the jq below) and the rules of its rulesets and
  # its branch protection (required_set_fixture), so the base reads the same through each of them.
  required=ci
  # The check paired with the red where the head carries an aggregate over a lane: the aggregate
  # `ci` beside the red lane (`aggregate`, worlds `i` and `j`), or the red lane under the red
  # aggregate (`over_lane`, world `g`). Each is a job of the run of the red and a check run of
  # its own, as every job of a run is on GitHub; every other world has none.
  partner='' partner_summary=''
  if jq -e .aggregate <<<"$facts" >/dev/null; then
    partner=$required partner_summary=$lanes
  elif jq -e .over_lane <<<"$facts" >/dev/null; then
    partner=$job partner_summary='1 of 412 tests in packages/notify failed.'
  fi
  # The failing line this world's log read answers, as the world knows it: a world whose read
  # answers an error (h) leaves none, since the role reads no line there.
  world_log=$log
  [ "$log_gone" = false ] || world_log=''
  jq -n --arg world "$world" --arg head "$head" --argjson pr "$pr" --arg repo "$worker_repo" \
    --arg branch "$branch" --arg base "$base" --arg base_sha "$base_sha" --arg title "$title" \
    --arg check "$check" --arg job "$job" --arg step "$step" --arg summary "$summary" \
    --arg log "$log" --argjson log_gone "$log_gone" --arg world_log "$world_log" \
    --arg test_name "$test_name" --argjson facts "$facts" --arg lanes "$lanes" \
    --arg required "$required" --arg run_name "$run_name" --arg partner "$partner" \
    --arg partner_summary "$partner_summary" --argjson run_id "$run_id" \
    --argjson job_id "$job_id" --argjson red_id "$red_id" "$(red_defs)"'
    def entry($id; $name; $conclusion; $output; $at):
      {id:$id, name:$name, status:"completed", conclusion:$conclusion, started_at:$at,
       completed_at:$at, html_url:("https://github.com/" + $repo + "/runs/" + ($id|tostring)),
       app:{slug:"github-actions"}, output:$output};
    "2026-03-04T10:00:00Z" as $t0
    | "2026-03-04T10:15:00Z" as $t_hand
    | "2026-03-04T10:31:08Z" as $t1
    | (if $facts.after == "green" then "success" else "failure" end) as $after
    | (if $run_name == "ci" then $run_id else $run_id + 2 end) as $ci_run
    | (if $facts.over_lane then [$red_id + 3, $red_id] else [$red_id, $red_id + 3] end)
      as [$lane_check, $aggregate_check]
    # The jobs of the head, each the job of one check run (its check_run_url), each with an id of
    # its own. The run of the red holds the failing lane and, beside it, lint and typecheck (in `f`
    # those two are the jobs of the `ci` run, the title check running in a run of its own) and,
    # where the head carries an aggregate over the lane, the aggregate `ci` job that needs the
    # lanes. Its attempt 2 is the re-run of its failed jobs: the lane and the aggregate. The spent
    # e2e run of `e` holds its two attempts, and the by-hand run of `k` its one job.
    | def job($id; $run; $attempt; $name; $conclusion; $step_name; $at; $check_id):
        {id:$id, run_id:$run, run_attempt:$attempt, head_sha:$head, name:$name,
         status:"completed", conclusion:$conclusion, started_at:$at, completed_at:$at,
         html_url:("https://github.com/" + $repo + "/actions/runs/" + ($run|tostring) + "/job/"
                   + ($id|tostring)),
         check_run_url:($api + "/check-runs/" + ($check_id|tostring)),
         steps:[{number:1, name:"Set up job", status:"completed", conclusion:"success"},
                {number:2, name:$step_name, status:"completed", conclusion:$conclusion}]};
      [job($job_id + 1; $ci_run; 1; "lint"; "success"; "Run bun lint"; $t0; $red_id - 2),
       job($job_id + 2; $ci_run; 1; "typecheck"; "success"; "Run bun typecheck"; $t0; $red_id - 1)]
      as $ci_jobs
    | ([job($job_id; $run_id; 1; $job; "failure"; $step; $t0; $lane_check)]
       + (if $run_name == "ci" then $ci_jobs else [] end)
       + (if $paired then [job($job_id + 3; $run_id; 1; $required; "failure"; "Check the lanes";
                              $t0; $aggregate_check)] else [] end)) as $red_jobs
    | ([job($job_id + 4; $run_id; 2; $job; $after; $step; $t1; $lane_check + 1)]
       + (if $paired then [job($job_id + 5; $run_id; 2; $required; $after; "Check the lanes"; $t1;
                              $aggregate_check + 1)] else [] end)) as $rerun_jobs
    # Each run is one check suite of GitHub Actions, as GitHub gives a workflow run one: its id is a
    # rig id made as the run ids are (the run id less a million), its node id in the CS_ shape.
    | def run($id; $name; $event; $attempt; $conclusion; $at):
        {id:$id, name:$name, display_title:$title, event:$event, status:"completed",
         conclusion:$conclusion, check_suite_id:check_suite_id($id),
         check_suite_node_id:("CS_rig" + (check_suite_id($id)|tostring)),
         head_sha:$head, head_branch:$branch, run_number:$pr,
         run_attempt:$attempt,
         html_url:("https://github.com/" + $repo + "/actions/runs/" + ($id|tostring)),
         jobs_url:($api + "/actions/runs/" + ($id|tostring) + "/jobs"),
         created_at:$at, updated_at:$at, run_started_at:$at,
         pull_requests:(if $event == "pull_request"
                        then [{number:$pr, head:{sha:$head, ref:$branch}}] else [] end)};
      # The runs of the head, each in the two states of the head, before the re-run and after it:
      # only the run of the red moves, to its attempt 2.
      ([{id:$run_id, states:[
          {run:run($run_id; $run_name; "pull_request"; 1; "failure"; $t0), attempts:[$red_jobs]},
          {run:(run($run_id; $run_name; "pull_request"; 2; $after; $t0) + {updated_at:$t1}),
           attempts:[$red_jobs, $rerun_jobs]}]}]
       + (if $run_name == "ci" then [] else
            [{id:$ci_run, states:([{run:run($ci_run; "ci"; "pull_request"; 1; "success"; $t0),
                                    attempts:[$ci_jobs]}] | . + .)}] end)
       + (if $facts.spent then
            [{id:($run_id - 1),
              states:([{run:run($run_id - 1; "e2e"; "pull_request"; 2; "success"; $t0),
                        attempts:[[job($job_id - 2; $run_id - 1; 1; "e2e"; "failure";
                                       "Run bun run e2e"; $t0; $red_id - 4)],
                                  [job($job_id - 1; $run_id - 1; 2; "e2e"; "success";
                                       "Run bun run e2e"; $t0; $red_id - 3)]]}] | . + .)}]
          else [] end)
       + (if $facts.by_hand then
            [{id:($run_id + 1),
              states:([{run:(run($run_id + 1; "ci"; "workflow_dispatch"; 1; "failure"; $t_hand)
                             + {display_title:"ci"}),
                        attempts:[[job($job_id + 6; $run_id + 1; 1; $job; "failure"; $step;
                                       $t_hand; $red_id + 2)]]}] | . + .)}]
          else [] end)) as $runs
    | ([$runs[].states[1].attempts[][]] | unique_by(.id)) as $all_jobs
    | ($all_jobs | map({key:(.check_run_url | split("/") | last), value:.}) | from_entries)
      as $job_of
    | def linked: . + {details_url:$job_of[(.id|tostring)].html_url};
      ($runs | map({key:(.id|tostring), value:.states[0].run.name}) | from_entries) as $workflow_of
    # The superseded runs the commit endpoint carries in a world where the one re-run of the head
    # is spent (`spent`, world `e`): the e2e check of attempt 1 that failed and the one of attempt
    # 2 that replaced it, as `filter=all` lists them. They decide nothing — the page lets no role
    # judge the spend on that endpoint — and the spend itself is in the runs of the head.
    | (if $facts.spent then
         [entry($red_id - 4; "e2e"; "failure";
                {title:"e2e", summary:"The e2e lane ended with no result."}; $t0),
          entry($red_id - 3; "e2e"; "success";
                {title:"e2e", summary:"The re-run of the e2e lane passed."}; $t0)]
       else [] end) as $earlier
    # The check paired with the red ($partner): the aggregate `ci` the base requires, red over the
    # failing lane (`i`, `j`), the shape of the base of cybermonklabs/legion itself, which
    # requires the aggregate `ci-ok` over its test lanes; or, in `g`, the failing lane under the
    # red aggregate. Its id comes after the by-hand run of `k`, so no two checks share one.
    | (if $paired
       then [entry($red_id + 3; $partner; "failure"; {title:$partner, summary:$partner_summary}; $t0)]
       else [] end) as $partners
    | ([entry($red_id - 2; "lint"; "success"; {title:"lint", summary:"No lint error."}; $t0),
        entry($red_id - 1; "typecheck"; "success"; {title:"typecheck", summary:"No type error."}; $t0)]
       + $earlier
       + [entry($red_id; $check; "failure"; {title:$check, summary:$summary}; $t0)]
       + $partners | map(linked)) as $red_checks
    # The attempt the re-run makes of each check it re-runs, which `filter=all` lists beside the
    # attempt it replaces: a pass where the world settles green, the same failure where it stays
    # red. It re-runs the red and its partner, the failed jobs of the one run of the red.
    | def attempt($id; $name; $failed):
        entry($id; $name; $after;
              {title:$name,
               summary:(if $facts.after == "green" then "The re-run of " + $name + " passed."
                        else $failed end)};
              $t1);
      ($red_checks + [attempt($red_id + 1; $check; $summary)]
       + ($partners | map(attempt(.id + 1; .name; .output.summary))) | map(linked)) as $settled_checks
    | ($settled_checks | group_by(.name) | map(max_by(.id))) as $latest
    | (($red_checks | group_by(.name) | map(max_by(.id)))) as $shown
    # The check of the by-hand `workflow_dispatch` run at that head, in a world whose commit
    # endpoint carries one (`by_hand`, world `k`): the counted set never holds it. It is the later
    # run of that check name there, so a role that reads the spend from the commit endpoint reads
    # the one re-run of the head as already spent.
    | (if $facts.by_hand then
         [entry($red_id + 2; $check; "failure";
                {title:$check,
                 summary:("A by-hand workflow_dispatch run of " + $check + " failed.")}; $t_hand)
          | linked]
       else [] end) as $uncounted
    # What the counted set answers, in order: the state the role first reads, and the state the
    # head settles at once the re-run is pressed. A counted set that has not reported the red when
    # the role first reads it (`report` among the facts, world `l`) answers three, the red in the
    # middle: red_report drops the first between the two turns, which is the rig reporting the
    # check. counted(reply) (red_model_defs) is the replies of a route that answers the counted
    # set, one per state, and in such a world the mark red_report trims that route by, `unreported`.
    | (if $facts.report then [($shown | map(select(.name != $check))), $shown, $latest]
       else [$shown, $latest] end) as $counted
    | {world:$world, head:$head, pr:$pr, repo:$repo, branch:$branch, base:$base,
       base_sha:$base_sha, check:$check, job:$job, test_name:$test_name, world_log:$world_log,
       log:$log, log_gone:$log_gone, facts:$facts, lanes:$lanes, required:$required,
       partner:$partner, run_id:$run_id, job_id:$job_id, runs:$runs, all_jobs:$all_jobs,
       job_of:$job_of, workflow_of:$workflow_of, red_checks:$red_checks,
       settled_checks:$settled_checks, latest:$latest, shown:$shown, uncounted:$uncounted,
       counted:$counted}'
}

# red_model_defs prints what run_routes and red_fixture's rewrite of the head's reads share over
# the model red_model printed, which the program that takes it reads with `--slurpfile model`:
# the model's facts and runs, red_defs over them, the rollups of the counted set, the routes that
# follow the re-run and the press they follow, and a route of a run, of a job, of a check run and
# of a log. It ends in definitions, so the program that takes it goes on with its own.
red_model_defs() {
  printf '%s\n' '
    $model[0] as {$head, $pr, $repo, $branch, $log, $log_gone, $facts, $lanes, $required,
                  $partner, $run_id, $job_id, $runs, $all_jobs, $job_of, $workflow_of,
                  $red_checks, $settled_checks, $latest, $shown, $uncounted, $counted}
    | '"$(red_defs)"
  printf '%s\n' '
    # The counted set in GitHub own shape, as the page reads it at a named head: the rollup of the
    # commit, its contexts on one page, and one CheckRun node per counted check, each naming its
    # job page, its times, its app and the workflow run it belongs to; and the same set as
    # `gh pr view --json statusCheckRollup` flattens it, a node per check run with its workflow
    # named (measured). A by-hand run is in no node of either.
    def workflow: $workflow_of[($job_of[(.id|tostring)].run_id|tostring)];
      def rollup($checks): $checks | map({__typename:"CheckRun", completedAt:.completed_at,
        conclusion:verdict, detailsUrl:.details_url, name, startedAt:.started_at,
        status:"COMPLETED", workflowName:workflow});
      def graphql_rollup($checks): {
        state:(if (failing($checks)|length) > 0 then "FAILURE" else "SUCCESS" end),
        contexts:{totalCount:($checks|length), pageInfo:{hasNextPage:false},
          nodes:($checks | map({__typename:"CheckRun", name:.name, databaseId:.id,
            status:"COMPLETED", conclusion:verdict, detailsUrl:.details_url,
            startedAt:.started_at, completedAt:.completed_at,
            checkSuite:{databaseId:check_suite_id($job_of[(.id|tostring)].run_id),
                        app:{slug:"github-actions", databaseId:15368},
                        workflowRun:{event:"pull_request",
                                     databaseId:$job_of[(.id|tostring)].run_id,
                                     workflow:{name:workflow}}}}))}};
    def counted(reply):
        {replies:($counted | map(reply))} + (if $facts.report then {unreported:true} else {} end);
    # Every read of the head below follows the re-run route (gh-standin.ts, `follows`): it answers
    # the first of its replies however many times a role reads it before the re-run, and the one
    # after it from the moment the re-run is pressed. moving(match; replies) is such a route, or
    # one that answers the one reply where the two states agree.
    # The press of the one re-run of the head: a re-run of the failed jobs of the run of the red,
    # by its id with --failed, or of the own job of the red by its id with --job (that of attempt
    # 1 or of the re-run), which GitHub re-runs with the jobs that need it, the aggregate among
    # them. A call naming both a run and a job, or --job with --failed, is one gh refuses in words
    # the rig has not measured, and no route takes it.
      ("^gh run rerun (?:" + ($run_id|tostring) + "(?=.*\\s--failed(?:\\s|$))(?!.*\\s--job\\s)(?=\\s|$)"
       + "|(?=--)(?=(?:\\S+ )*--job (?:" + ($job_id|tostring) + "|" + ($job_id + 4 | tostring)
       + ")(?:\\s|$))(?!(?:\\S+ )*--failed(?:\\s|$)))") as $rerun
    | def moving($match; $replies):
        {match:$match} + (if $replies[0] == $replies[1] then $replies[0]
                          else {follows:$rerun, replies:$replies} end);
      def ids($xs): "(?:" + ($xs | map(tostring) | join("|")) + ")";
      # A `gh run view` that names its run as the positional argument, and no job.
      def view($id): "^gh run view " + ($id|tostring) + "(?=\\s|$)(?!.*\\s--job\\s)";
      # A `gh run view` or `gh run rerun` that names its job with --job and no run.
      def job_flag($id): "(?=--)(?=(?:\\S+ )*--job " + $id + "(?:\\s|$))";
      # A `gh run view` that names its job with --job, with or without a run: gh reads the job,
      # then the run the job belongs to, whatever run the call names (measured: `gh run view
      # 9001002 --job 8001003` asks for job 8001003 and then for run 7001003).
      def view_job($id): "^gh run view(?: \\d+)?(?=.*\\s--job " + $id + "(?:\\s|$))";
      def logs: "(?=.*\\s--log(?:-failed)?(?:\\s|$))(?!.*\\s--json\\s)";
      def plain: "(?!.*\\s--(?:log|log-failed|json)(?:\\s|$))";
    # The log a run or a job prints, as gh does, a line per log line, `<job>\t<step>\t<line>`: the
    # failing line of the world in the failing step of each failed job, and no line of a job that
    # passed, which is all the log the world holds. Each log line carries the time GitHub stamps
    # on it, seven digits after the second, and the first log line of each step the BOM GitHub
    # puts there, which gh 2.98.0 prints as it stands (measured on a live job of
    # cybermonklabs/legion, 2026-11-18, runview-job-log of the measurements); the world logs no
    # ESC byte, so gh never refuses one for its terminal escape sequences. The log of attempt 1 of
    # the run of the red is past retention in world `h`, so its read answers what gh 2.98.0 prints
    # for the 410 GitHub answers there (measured on a run past retention): the URL of the log of
    # the run gh asked for, or of the attempt where the call names one.
      def log_line: if .name == $required and $paired then $lanes
        elif .name == "e2e" then "The e2e lane ended with no result." else $log end;
      def log_expired($run; $attempt):
        {stderr:("failed to get run log: HTTP 410: Server Error (https://api.github.com/repos/"
                 + $repo + "/actions/runs/" + ($run|tostring)
                 + (if $attempt == null then "" else "/attempts/" + ($attempt|tostring) end)
                 + "/logs)\n"),
         exit:1};
      def log_reply($jobs):
        if $log_gone and any($jobs[]; .run_id == $run_id and .run_attempt == 1
                                      and .conclusion == "failure")
        then log_expired($run_id; null)
        else {stdout:([$jobs[] | select(.conclusion == "failure")
                       | .name + "\t" + .steps[1].name + "\t" + "\ufeff"
                         + (.completed_at | sub("Z$"; ".0000000Z")) + " " + log_line]
                      | join("\n"))} end;
      # A job as a state of the head holds it, with the run of that state it belongs to; a job
      # that state does not hold yet (an attempt 2 before the re-run) answers $miss, the error gh
      # prints for a job it cannot find or the 404 of GitHub.
      def job_in($state; $id):
        [$runs[].states[$state] | .run as $run | .attempts[][] | select(.id == $id)
         | {job:., run:$run}] | first;
      def job_route($match; $id; reply; $miss):
        moving($match; [0, 1] | map(job_in(.; $id) as $j | if $j != null then ($j | reply) else $miss end));
      def run_route($match; $w; reply): moving($match; $w.states | map(reply));
      def check_in($state; $id):
        [(if $state == 0 then $red_checks else $settled_checks end)[], $uncounted[]
         | select(.id == $id)] | first;
      def check_route($match; $id; reply; $miss):
        moving($match; [0, 1] | map(check_in(.; $id) as $c
                                   | if $c == null then $miss else ($c | reply) end));
'
}

# run_routes MODEL prints, as one JSON list, the routes of the head's runs, jobs and check runs in
# the world of MODEL (a file red_model printed): the press of the re-run and gh's help and usage
# around it, each read of a run, a job, a check run or a log of the head as the state of the head
# has it, and GitHub's answer, or gh's refusal, for each of those reads of an id the head has not.
# red_fixture puts them ahead of worker_fixture's routes.
run_routes() {
  jq -n --slurpfile model "$1" \
    --arg rerun_help "$(rerun_help)" --arg rerun_usage "$(rerun_usage)" --arg view_usage "$(view_usage)" \
    --arg run_view_program "$(run_view_program)" --arg run_list "$(run_list_program)" \
    --arg runs_query "$(runs_query_program)" "$(red_model_defs)$(route_defs)"'
      ($runs[0]) as $red_run
    | ([$runs[].id]) as $run_ids
    | ([$settled_checks[], $uncounted[] | .id] | unique) as $check_ids
    | [{match:"^gh run rerun(?=.*\\s--help(?:\\s|$))", stdout:$rerun_help},
       # A re-run presses the re-run of the head only where it re-runs the failed jobs of the run
       # the red of the world belongs to, or the own job of the red: the re-run route takes those calls
       # alone, so its count, which every read of the head follows, moves on them and on nothing
       # else. Before these routes any `gh run rerun` pressed the head, so a re-run of a check run
       # id (trial-d-head-5, trial-e-head-2, trial-g-head-2) and `gh run rerun --help`
       # (trial-i-nos1-1, trial-j-nos1-1) each settled the head and published its settlement. A
       # re-run of every other run or job of the head (the spent e2e run of `e`, the by-hand run of
       # `k`, the `ci` run of `f`, the lint job) would make a state of it this world does not hold,
       # and is not modeled; a run or a job the head has none of is answered the 404 gh prints.
       ({match:("^gh run rerun " + job_flag("(?<job>\\d+)") + "(?!(?:\\S+ )*--job " + ids($all_jobs | map(.id))
                + "(?:\\s|$))")} + no_job($repo; "$<job>")),
       ({match:("^gh run rerun (?!" + ids($run_ids) + "(?:\\s|$))(?<run>\\d+)(?=\\s|$)(?!.*\\s--job\\s)")}
        + no_run($repo; "$<run>")),
       {match:"^gh run rerun(?! \\d)(?!.*\\s--job\\s)", stderr:($rerun_usage + "\n"), exit:1},
       # gh 2.98.0 prints nothing for a re-run it requested where no terminal is there (measured).
       {match:$rerun, stdout:"", publish:published($latest)},
       # The same re-run through the REST API, a POST of the failed jobs of the run of the red or
       # of the own job of the red: it presses the re-run as `gh run rerun` does, counting as a call of
       # that route (gh-standin.ts, `counts_as`), and answers what GitHub answers, an empty object.
       {match:api_post("(?:actions/runs/" + ($run_id|tostring) + "/rerun-failed-jobs"
                       + "|actions/jobs/" + ids([$job_id, $job_id + 4]) + "/rerun)"),
        stdout:{}, counts_as:$rerun, publish:published($latest)},
       # Every read of a run or of a job answers the runs and the jobs of the head alone, each as
       # the state of the head has it: its log, its fields under --json, or the text gh 2.98.0
       # prints for it (measured, $run_view_program); a run or a job of any other id is refused as
       # gh refuses it, and its REST read answers the 404 of GitHub. A job read under --json is
       # not modeled. A read that names an attempt (--attempt) is the log of attempt 1 of the run
       # of the red in world `h`, past retention, whose 410 names that attempt (measured); any other
       # read of an attempt is not modeled.
       (if $log_gone
        then {match:(view($run_id) + "(?=.*\\s--attempt 1(?:\\s|$))" + logs)} + log_expired($run_id; 1)
        else empty end),
       {match:"^gh run view(?=.*\\s--attempt\\s)", unmodeled:true},
       ($all_jobs[] | .id as $id
        | job_route(view_job($id|tostring) + logs; $id;
                    log_reply([.job]); no_job($repo; $id|tostring)),
          (job_route(view_job($id|tostring) + plain; $id;
                     {stdout:.}; no_job($repo; $id|tostring)) + {program:$run_view_program})),
       ({match:view_job("(?<job>\\d+)")} + no_job($repo; "$<job>")),
       ({match:("^gh run view (?!" + ids($run_ids) + "(?:\\s|$))(?<run>\\d+)(?=\\s|$)(?!.*\\s--job\\s)")}
        + no_run($repo; "$<run>")),
       {match:"^gh run view(?! \\d)(?!.*\\s--job\\s)", stderr:($view_usage + "\n"), exit:1},
       ($runs[] | . as $w
        | run_route(view($w.id) + logs; $w; latest as $jobs | log_reply($jobs)),
          run_route(view($w.id) + "(?=.*\\s--json\\s)(?!.*\\s--log(?:-failed)?(?:\\s|$))"; $w;
                    latest as $jobs | {stdout:(.run | gh_run($jobs))}),
          (run_route(view($w.id) + plain; $w; {stdout:{run:.run, jobs:latest}})
           + {program:$run_view_program})),
       (moving("^gh run list(?: --|$)"; [0, 1] | map(. as $s | {stdout:[$runs[].states[$s] | .run | gh_run([])
                                                                        | del(.jobs)]}))
        + {program:$run_list}),
       ($runs[] | . as $w | ($w.id|tostring) as $id
        | run_route(api("actions/runs/" + $id); $w; {stdout:.run}),
          run_route(api("actions/runs/" + $id + "/jobs"); $w;
                    latest as $jobs | {stdout:{total_count:($jobs|length), jobs:$jobs}}),
          (range(1; ($w.states[1].attempts|length) + 1) as $n
           | run_route(api("actions/runs/" + $id + "/attempts/" + ($n|tostring)); $w;
                       if .attempts[$n - 1] == null then github("attempt-404")
                       elif $n == (.attempts|length) then {stdout:.run}
                       else {stdout:(.run + {run_attempt:$n, conclusion:"failure",
                                             updated_at:.run.created_at})} end),
             run_route(api("actions/runs/" + $id + "/attempts/" + ($n|tostring) + "/jobs"); $w;
                       if .attempts[$n - 1] == null then github("attempt-jobs-404")
                       else {stdout:{total_count:(.attempts[$n - 1]|length),
                                     jobs:.attempts[$n - 1]}} end)),
          ([range(1; ($w.states[1].attempts|length) + 1)] as $attempts
           | ({match:api("actions/runs/" + $id + "/attempts/(?!" + ids($attempts) + "(?:[/?\\s]|$))\\d+")}
              + github("attempt-404")),
             ({match:api("actions/runs/" + $id + "/attempts/(?!" + ids($attempts) + "(?:[/?\\s]|$))\\d+/jobs")}
              + github("attempt-jobs-404")))),
       # A run of no id the head has: the 404 GitHub answers for each read of it the rig answers
       # (measured), any other read of it not modeled.
       ("actions/runs/(?!" + ids($run_ids) + "(?:[/?\\s]|$))\\d+") as $other_run
       | ({match:api($other_run)} + github("run-404")),
         ({match:api($other_run + "/jobs")} + github("run-jobs-404")),
         ({match:api($other_run + "/attempts/\\d+")} + github("run-attempt-404")),
         ({match:api($other_run + "/attempts/\\d+/jobs")} + github("run-attempt-jobs-404")),
         ({match:api($other_run + "/logs")} + github("run-logs-404")),
         ({match:api_post($other_run + "/rerun-failed-jobs")} + github("rerun-404")),
         ({match:api_post($other_run + "/rerun")} + github("run-rerun-404")),
       # A job and its log through the REST API: the log of a failed job holds the failing line of
       # the world in its failing step and the close the runner writes, that of a job that passed
       # its step and no line of its own, which is all the log the world holds. The log of attempt
       # 1 of the run of the red is past retention in world `h`, which GitHub answers with a 410
       # (measured by the lead under gh 2.98.0 on a job past retention, 2026-11-18). A job the
       # state of the head does not hold yet, or of no id the head has, answers the 404 of GitHub.
       # The log is as GitHub sends a job log (measured on a live job of cybermonklabs/legion,
       # 2026-11-18, joblog-live of the measurements): one BOM at its start, each line stamped with
       # the time, seven digits after the second, and a newline after each line, the last one too.
       # The world logs no ESC byte, so gh never refuses the log for its terminal escape sequences.
       ($all_jobs[] | .id as $id
        | job_route(api("actions/jobs/" + ($id|tostring)); $id; {stdout:.job}; github("job-404")),
          job_route(api("actions/jobs/" + ($id|tostring) + "/logs"); $id;
            .job | (.completed_at | sub("Z$"; ".0000000Z")) as $ts
            | if $log_gone and .run_id == $run_id and .run_attempt == 1
                 and .conclusion == "failure"
              then github("joblog-410")
              else {stdout:("\ufeff\($ts) ##[group]\(.steps[1].name)\n"
                            + (if .conclusion == "failure"
                               then "\($ts) \(log_line)\n\($ts) ##[error]Process completed with exit code 1.\n"
                               else "" end)
                            + "\($ts) ##[endgroup]\n")} end; github("joblog-404"))),
       ("actions/jobs/(?!" + ids($all_jobs | map(.id)) + "(?:[/?\\s]|$))\\d+") as $other_job
       | ({match:api($other_job)} + github("job-404")),
         ({match:api($other_job + "/logs")} + github("joblog-404")),
         ({match:api_post($other_job + "/rerun")} + github("jobrerun-404")),
       # A check run by its id, and its annotations: none for a check that passed, and for one
       # that failed the annotation the runner writes for any failed step, which names no test.
       ($check_ids[] | . as $id
        | check_route(api("check-runs/" + ($id|tostring)); $id; {stdout:.}; github("checkrun-404")),
          check_route(api("check-runs/" + ($id|tostring) + "/annotations"); $id;
                      {stdout:(if .conclusion == "failure"
                               then [{path:".github", start_line:1, end_line:1,
                                      start_column:null, end_column:null,
                                      annotation_level:"failure", title:"",
                                      message:"Process completed with exit code 1.",
                                      raw_details:""}]
                               else [] end)}; github("annotations-404"))),
       ("check-runs/(?!" + ids($check_ids) + "(?:[/?\\s]|$))\\d+") as $other_check
       | ({match:api($other_check)} + github("checkrun-404")),
         ({match:api($other_check + "/annotations")} + github("annotations-404")),
       (moving(api("actions/runs"); [0, 1] | map(. as $s
          | [$runs[].states[$s].run] as $all
          | {stdout:{total_count:($all|length), workflow_runs:$all}}))
        + {program:$runs_query})]'
}

# required_set_fixture CHECK BASE BASE_SHA puts the reads of the required set of the base branch BASE
# ahead of every route of $R/fixtures.json, each answering that BASE requires CHECK alone, as
# red_fixture's `gh pr checks --required` route answers it (red_fixture binds the one CHECK both
# take): the rules GitHub applies to the base, through its rulesets (`rules/branches/<base>`),
# which the prompt of the reviewer reads (packages/daemon/internal/prompts/go/reviewer.md, "CI is
# yours to read"); the rulesets of the repository, their list and the one ruleset, which applies
# to the default branch and holds that same rule; the base branch, read for its protection, which
# the prompt reads too; and the protection of the base on its own endpoint, the same required
# checks in the shape that endpoint answers them in. They read nothing of the world but the
# repository (worker_repo), BASE and BASE_SHA, which red_fixture reads from world.json. Each
# answers unfiltered, since the stand-in applies the `--jq` of a call itself, and the branch
# answers its name, its commit (BASE_SHA, so a read of `.commit.sha` names the base rather than
# null, which sent runs of rounds 6 and 9 on to read `commits/null/check-runs`) and its protection.
# The required status checks of the protection answer on their own endpoint too, and its contexts.
# They come ahead of every other route: in the batch of
# 2026-11-17 the `--jq` text of the two reads of the prompt held `required_status_checks`, so the
# generic `(check-runs|status)` route took both and each filter, run over a check-runs answer,
# exited 1, and the rules read without its `--jq` (trial-i-head-3), the protection read
# (trial-i-head-1) and the rulesets list (trial-i-head-5) each fell to the 404, so every reviewer
# read the base as requiring nothing. Each match is a route_defs `api` read, which names the
# repository and ends where its own path ends, so none takes the path of another: the branch
# route never takes the `/protection` below it, nor the rulesets list the one ruleset, and the
# rules path has its `rules/` between the repository and `branches/`.
required_set_fixture() {
  jq --arg repo "$worker_repo" --arg required "$1" --arg base "$2" --arg base_sha "$3" \
    "$(route_defs)"'
    ("https://api.github.com/repos/" + $repo) as $api
    | ($api + "/branches/" + $base + "/protection") as $protection_url
    | {contexts:[$required], checks:[{context:$required, app_id:null}]} as $status_checks
    | ({url:($protection_url + "/required_status_checks"), strict:false,
        contexts_url:($protection_url + "/required_status_checks/contexts")} + $status_checks)
      as $required_status_checks
    | {id:1, name:("Require " + $required), target:"branch", source_type:"Repository",
       source:$repo, enforcement:"active"} as $ruleset
    | {type:"required_status_checks",
       parameters:{strict_required_status_checks_policy:false,
                   required_status_checks:[{context:$required}]}} as $rule
    | ("branches/" + ($base | literal)) as $branch
    | [{match:api("rules/" + $branch),
        stdout:[$rule + {ruleset_source_type:$ruleset.source_type, ruleset_source:$ruleset.source,
                         ruleset_id:$ruleset.id}]},
       {match:api("rulesets"), stdout:[$ruleset]},
       {match:api("rulesets/" + ($ruleset.id|tostring)),
        stdout:($ruleset + {bypass_actors:[],
                            conditions:{ref_name:{include:["~DEFAULT_BRANCH"], exclude:[]}},
                            rules:[$rule]})},
       {match:api($branch),
        stdout:{name:$base, commit:{sha:$base_sha, url:($api + "/commits/" + $base_sha)},
                protected:true,
                protection:{enabled:true,
                            required_status_checks:({enforcement_level:"non_admins"}
                                                    + $status_checks)},
                protection_url:$protection_url}},
       {match:api($branch + "/protection"),
        stdout:{url:$protection_url, required_status_checks:$required_status_checks,
                enforce_admins:{url:($protection_url + "/enforce_admins"), enabled:false}}},
       {match:api($branch + "/protection/required_status_checks"), stdout:$required_status_checks},
       {match:api($branch + "/protection/required_status_checks/contexts"), stdout:[$required]}]
      as $required_set
    | .routes = $required_set + .routes' \
    "$R/fixtures.json" >"$R/fixtures.required.json"
  mv "$R/fixtures.required.json" "$R/fixtures.json"
}

# red_report: the rig reports the check world `l`'s counted set had not reported, between that
# world's two turns and after the role's first turn has ended. Each route red_fixture marked
# `unreported` drops its first answer, the one that reported no red, and the mark with it — the
# gh stand-in keeps that mark through its own writes of fixtures.json — so the counted set now
# reports the check red before the re-run and green after it, as every other world's counted
# reads already do. Then the settlement the head's checks would raise goes to the run's listener
# (the last ENVOY_URL of <run>/pane.env, which boot_worker copied from the batch's services file),
# the envelope red_fixture wrote to <run>/reported.json, through the gh stand-in's own publish
# (`gh-standin.ts --publish`), the one the re-run's own route publishes with; a publish that
# fails, or that a listener holds past the stand-in's five seconds, is noted and the run goes on,
# since the turn below runs whether the wake reached the role or not.
# <run>/reported-at is the moment the counted set reported it, which score.ts's redAtTheHead
# reads the role's re-run and its first turn against.
# The rewrite of fixtures.json holds the run's fixtures lock, an exclusive flock(2) on
# <run>/fixtures.lock, from its read of the file to its rename, as every gh stand-in call holds it
# (gh-standin.ts): a call that read the file before the rewrite and wrote it back after would put
# the dropped answers back, and a rewrite over a call's own write would lose that call's count. A
# lock, read or write that fails stops the report there and fails it.
red_report() {
  local url
  (
    flock --exclusive 9 &&
      jq '.routes |= map(if .unreported then .replies |= .[1:] | del(.unreported) else . end)' \
        "$R/fixtures.json" >"$R/fixtures.reported.json" &&
      mv "$R/fixtures.reported.json" "$R/fixtures.json"
  ) 9>>"$R/fixtures.lock" || return 1
  date -u +%Y-%m-%dT%H:%M:%SZ >"$R/reported-at"
  url=$(sed -n 's/^ENVOY_URL=//p' "$R/pane.env" | tail -n 1)
  SKILL_SCENARIO_RUN=$R bun "$here/gh-standin.ts" --publish "$R/reported.json" ||
    note "the report of the check reached no listener at $url"
}

# rerun_pressed SECONDS waits up to SECONDS for the role's press of the re-run to reach the run's
# calls.jsonl, which the gh stand-in appends to as it answers each call: the record of a call
# whose route published the head's settlement (gh-standin.ts, `published`), which only the two
# press routes of red_fixture do. A call that pressed nothing (`gh run rerun --help`, a re-run of
# a run or a job the head has none of, a call naming neither) published nothing, so it wakes
# nothing: the second turn starts on a settlement that came. The scorer counts presses its own
# way (score.ts, RERUN_PRESS): the role's decision to re-run, whatever it named. The wait is
# bounded and its answer is not a requirement: a role that re-ran nothing is a run the scorer
# fails, never a rig failure, so this answers 1 and the run ends on its one turn's result.
rerun_pressed() {
  local deadline=$((SECONDS + $1))
  while :; do
    if jq -e -s 'any(.[]; .as == "gh" and .published != null)' "$R/calls.jsonl" >/dev/null 2>&1; then
      return 0
    fi
    [ "$SECONDS" -lt "$deadline" ] || return 1
    sleep 2
  done
}

# run_red_at_the_head NAME N INDEX START WORLD: run_tester_proof's run with the world's red at the
# head (red_fixture), for the one role that holds the phase in it, which the world's facts name
# (red_world, read from world.json), and never two workers. A world that expects a re-run takes a
# second turn once the re-run has reached calls.jsonl: the role ended its first turn waiting, and
# the re-run's route has already published the settlement, which the role's subscription carries
# to it. A world whose counted set the rig reports (`report`, world `l`) takes its second turn on
# the rig's own report of the check instead (red_report), the role having ended its first turn
# waiting for the counted set to report it.
# A world whose re-run never came, and a second turn that settles nothing, are the run's result,
# which the scorer reads; neither fails the rig.
# <run>/turns.txt has one line per turn, `<turn> <assignment|settlement|report> <the moment the
# turn ended, RFC 3339> <exit line>`, and each turn's output is <run>/out-<turn>.txt.
run_red_at_the_head() {
  local name=$1 n=$2 index=$3 start=$4 world=$5 role phase turns report wake told='' state project port pr head check version
  worker_world "$index" "$start" "$world"
  read -r role phase turns report < <(jq -r '"\(.role) \(.phase) \(.turns) \(.report)"' "$R/world.json")
  # The architect's assignment for the world's phase, in run_tester_proof's shape.
  #
  # World l's task names the failing check, as the daemon's own task names the checks a red moved
  # the issue for (effects.go). Without it the role has no sign of the red at all, since the
  # counted set does not report it yet and the commit endpoint is the one read the page forbids
  # judging on, so the world asked the role to wait for something it could not know about: four
  # of its five head runs of 2026-11-12 completed a green round, correctly by every rule the page
  # gives them.
  [ "$report" != true ] || told="The daemon reports $check red at that head."
  architect_assignment "$role" "$told"
  boot_worker "$role"
  launch "$name"
  : >"$R/turns.txt"
  record_turn 1 assignment
  # The second turn, the wake the settlement would bring: the daemon's own task for a phase already
  # under way (workflow.ResumePhaseTask), since the role keeps its session, its phase and its task
  # across the wait. A minute is the bound, the re-run having reached calls.jsonl inside the turn
  # that pressed it. World `l` waits on no re-run: the rig reports the check the counted set had
  # not reported, which is that world's own wake (red_report).
  wake=''
  if [ "$report" = true ]; then
    red_report >>"$R/fixture.log" 2>&1 || fail "the world $world report failed; see $R/fixture.log"
    wake=report
  elif [ "$turns" = 2 ] && rerun_pressed 60; then
    wake=settlement
  elif [ "$turns" = 2 ]; then
    note "$name: no re-run reached calls.jsonl; the run ends on its one turn"
  fi
  if [ -n "$wake" ]; then
    printf 'Continue greet(): a greeting helper for the widgets CLI. Issue: %s. Phase: %s. Resume the existing phase work.\n' \
      "$worker_key" "$phase" >"$R/prompt.txt"
    launch "$name" --continue
    record_turn 2 "$wake"
  fi
  # What the run left on its issue: every handoff record, each with its body, for redAtTheHead.
  seed_ts capture-handoffs "$R/fixture.json" "$R"
}

# run_prefix SCENARIO prints the prefix a run of SCENARIO carries in its directory and its tmux
# session name. Every scenario uses its own name, save red-at-the-head, which uses `trial`: its
# page, skills/legion-worker/references/red-at-the-head.md, is the file under test, and when that
# name sat in an agent-visible path (a run's cwd, HOME, TMPDIR, LEGION_WORKSPACE, PATH and
# SKILL_SCENARIO_RUN all live under the run directory) it cued the change arm — 119 of 120
# record-batch runs opened the page before any CI read, which lifted that arm alone. `trial` names
# neither the scenario nor the page.
run_prefix() {
  case $1 in red-at-the-head) echo trial ;; *) echo "$1" ;; esac
}

cmd_run() {
  [ $# = 5 ] || fail "usage: rig.sh run <ask-on-message|measure-before-ask|tester-proof|brainstorm|askgate|spec-writing|spec-rewrite|red-at-the-head> <label> <n> <index> <start>"
  local scenario=$1 n=$3 index=$4 start=$5 name
  load_label "$2"
  [[ $n =~ ^[0-9]+$ ]] || fail "run number '$n' is not a number"
  [[ $index =~ ^[0-9]+$ && $start =~ ^[0-9]+$ ]] || fail "run index '$index' or batch start '$start' is not a number"
  [ -f "$services_env" ] || fail "a run needs its batch's services: run \`rig.sh batch $scenario 1 $2\`"
  # An askgate run's name carries its case, so the batches of the cases, each numbered from 1,
  # never remove each other's runs.
  if [ "$scenario" = askgate ]; then
    case ${SKILL_SCENARIOS_ASKGATE_CASE:-} in
    facts | link | upload | nochange | nochange-rebuttal | nochange-block | untried | tried)
      name=$(run_prefix "$scenario")-$SKILL_SCENARIOS_ASKGATE_CASE-${P##*/}-$n
      ;;
    *) fail "SKILL_SCENARIOS_ASKGATE_CASE is '${SKILL_SCENARIOS_ASKGATE_CASE:-}', not facts, link, upload, nochange, nochange-rebuttal, nochange-block, untried or tried" ;;
    esac
  elif [ "$scenario" = red-at-the-head ]; then
    # A red-at-the-head run's name carries its world, for the same reason. The worlds are
    # red_world's, the one place they are written.
    red_world "${SKILL_SCENARIOS_RED_WORLD:-}" >/dev/null ||
      fail "SKILL_SCENARIOS_RED_WORLD is '${SKILL_SCENARIOS_RED_WORLD:-}', not $(red_worlds)"
    name=$(run_prefix "$scenario")-$SKILL_SCENARIOS_RED_WORLD-${P##*/}-$n
  else
    name=$(run_prefix "$scenario")-${P##*/}-$n
  fi
  R=$work/runs/$name
  evidence=$R
  on_exit stop_run
  rm -rf "$R"
  mkdir -p "$R/bin" "$R/sessions" "$R/cwd" "$R/logs"
  cp "$P/built-from" "$R/built-from"
  case $scenario in
  ask-on-message) run_ask_on_message "$name" ;;
  measure-before-ask) run_measure_before_ask "$name" ;;
  tester-proof) run_tester_proof "$name" "$n" "$index" "$start" ;;
  brainstorm) run_brainstorm "$name" ;;
  askgate) run_askgate "$name" ;;
  spec-writing | spec-rewrite) run_spec "$scenario" "$name" ;;
  red-at-the-head) run_red_at_the_head "$name" "$n" "$index" "$start" "$SKILL_SCENARIOS_RED_WORLD" ;;
  *) fail "unknown scenario $scenario" ;;
  esac
  echo "$name: $(tail -n 1 "$R/out.txt")"
}

# A batch's exit: its runs (each run's shell, daemon stand-in and fixture commands), the agents they
# started, every process that carries a run's SKILL_SCENARIO_RUN, and the services.
# shellcheck disable=SC2329 # on_exit's trap runs it
stop_batch() {
  local s sessions=()
  stop_tree "${runs_pid:-}"
  for s in $(tmux list-sessions -F '#{session_name}' 2>/dev/null || true); do
    case $s in "$tag-$(run_prefix "$batch_scenario")"-*) sessions+=("$s") ;; esac
  done
  for s in "${sessions[@]}"; do stop_panes "$s"; done
  stop_env_processes "$work/runs"
  services_down
}

cmd_batch() {
  [ $# -ge 3 ] || fail "usage: rig.sh batch <ask-on-message|measure-before-ask|tester-proof|brainstorm|askgate|spec-writing|spec-rewrite|red-at-the-head> <runs> <label>..."
  local runs=$2 label n index=0 start
  batch_scenario=$1
  case $batch_scenario in ask-on-message | measure-before-ask | tester-proof | brainstorm | askgate | spec-writing | spec-rewrite | red-at-the-head) ;; *) fail "unknown scenario $batch_scenario" ;; esac
  [ "$batch_scenario" != askgate ] || case ${SKILL_SCENARIOS_ASKGATE_CASE:-} in
    facts | link | upload | nochange | nochange-rebuttal | nochange-block | untried | tried) ;;
    *) fail "an askgate batch needs SKILL_SCENARIOS_ASKGATE_CASE set to facts, link, upload, nochange, nochange-rebuttal, nochange-block, untried or tried (got '${SKILL_SCENARIOS_ASKGATE_CASE:-}')" ;;
  esac
  [ "$batch_scenario" != askgate ] || case ${SKILL_SCENARIOS_ASKGATE_MODE:-} in
    '' | warn | block) ;;
    *) fail "an askgate batch takes SKILL_SCENARIOS_ASKGATE_MODE unset, warn or block (got '$SKILL_SCENARIOS_ASKGATE_MODE')" ;;
  esac
  [ "$batch_scenario" != red-at-the-head ] || red_world "${SKILL_SCENARIOS_RED_WORLD:-}" >/dev/null ||
    fail "a red-at-the-head batch needs SKILL_SCENARIOS_RED_WORLD set to $(red_worlds) (got '${SKILL_SCENARIOS_RED_WORLD:-}')"
  [[ $runs =~ ^[0-9]+$ ]] || fail "run count '$runs' is not a number"
  # A spec batch numbers its run one above its first label's highest (next_run, below), so a second
  # label or a second run could take the number of a run cmd_run would then remove: one run of one
  # label. Each run has a Dispatch of its own (own_dispatch), so no run sees a sibling's issue.
  case $batch_scenario in spec-writing | spec-rewrite)
    [ $((runs * ($# - 2))) = 1 ] ||
      fail "a $batch_scenario batch takes one run of one label (got $runs of $(($# - 2))); run each label as its own batch"
    ;;
  esac
  shift 2
  local -a daemon_modules=("$root/packages/daemon")
  for label in "$@"; do
    load_label "$label"
    daemon_modules+=("$co/packages/daemon")
  done
  # A spec run reads its source and the label's superpowers skills: refused here, before any
  # service starts. Its number is one above the label's highest, so no batch removes an earlier run.
  local first=1
  case $batch_scenario in spec-writing | spec-rewrite)
    source_dir "$batch_scenario" "${SKILL_SCENARIOS_SOURCE:-}" >/dev/null
    require_superpowers "$batch_scenario"
    first=$(next_run "$batch_scenario" "$1")
    ;;
  esac
  lock_services
  on_exit stop_batch
  services_up
  # Only a tester-proof or red-at-the-head run boots a worker (boot_worker), whose daemon stand-in
  # and pane compile daemon-pane.go, so no other scenario's batch warms it.
  case $batch_scenario in tester-proof | red-at-the-head)
    warm_daemon_panes "${daemon_modules[@]}"
    ;;
  esac
  start=$(date +%s)
  for label in "$@"; do
    for n in $(seq "$first" $((first + runs - 1))); do
      index=$((index + 1))
      printf '%s %s %s %s %s\n' "$batch_scenario" "$label" "$n" "$index" "$start"
    done
  done | xargs -r -P "${SKILL_SCENARIOS_PARALLEL:-5}" -L 1 bash "$here/rig.sh" run &
  runs_pid=$!
  wait "$runs_pid"
}

# Bash reads a script as it runs it, so the dispatch and its exit are one command line: a batch
# outlives any edit to this file, and resuming at its old offset would run whatever lies there now.
case "${1:-}" in
profile) shift && cmd_profile "$@" ;;
live-read) shift && cmd_live_read "$@" ;;
batch) shift && cmd_batch "$@" ;;
run) shift && cmd_run "$@" ;;
score) shift && bun "$here/score.ts" runs "$work/runs" "$@" ;;
judge) shift && cmd_judge "$@" ;;
judge-omp) shift && cmd_judge_omp "$@" ;;
*) sed -n '2,/^set -euo/p' "${BASH_SOURCE[0]}" | sed '$d' | sed 's/^# \{0,1\}//' && exit 2 ;;
esac; exit
