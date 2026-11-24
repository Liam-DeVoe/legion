"""The facts about ci.yaml's layout that the layout itself cannot say, which
.github/scripts/check-ci-layout.sh's rules hold the workflows to (ci_layout.py and
ci_workflow_rules.py read them; the header of check-ci-layout.sh numbers the rules).

Each name here is a pin a person sets by reading the layout, and a rule fails when the workflows
stop saying what the pin says:

  DAEMON_RACE_STEP, DAEMON_RACE_FILL_JOB   rule 16: the go-daemon step that runs the daemon's
            packages under the race detector, and the ci-caches.yaml job whose race build fills the
            cache that step restores; the two must name one package list, in one order.
  RACE_FILL_SCRIPTS   rule 16: a ci-caches.yaml job that fills a race step's cache through the
            script that step runs: (the fill job, the script, the directory, the one call).
  DARWIN_VETS   rule 20: the three steps that vet a Go module for darwin, each with the module's
            directory; each runs DARWIN_VET_SCRIPT and takes its tag sets from go_build_tags.py.
  LIVE_GROUPS   rule 27: the subtest groups kind's legs split by name: the file that declares
            `liveGroups`, the test that runs them, and the job and action whose legs run them.
  NO_RETRY_SCRIPT   rule 24: the script whose runs against a mutant take no retry, by
            `export GO_TEST_RETRY=off`; kind's runtime legs, the other exception, are the steps of
            LIVE_GROUPS' action whose -run selects one group.
  EVENT_GATES   rule 28: the gates on the event or a label that R9 names, each where it is.
  ARM_JOBS   rule 26: a job whose runner is part of what it proves.
  MAIN_CHECK_RUN_NAMES   rule 30: the check-run names of ci.yaml at main 51d1a21ff7 and those later
            commits of main add (the plan's Appendix A), each of which the layout still carries,
            but ci-filters / ci-filters, whose job R9 deletes.
  CARRIER_NAMES   rule 32: the carriers whose old name is not `<base> / <base>`, by base.
  TIMED_STEPS   rule 31: the steps that carried a step-level timeout-minutes at main 51d1a21ff7
            and in the jobs this change added (the plan's Appendix B), each where it stands now
            and how many of it there are.
"""

DAEMON_RACE_STEP = (".github/actions/go-daemon/action.yml", "Test the concurrent packages under the race detector")
DAEMON_RACE_FILL_JOB = "daemon"
RACE_FILL_SCRIPTS = [("envoy-infra", "packages/envoy/infra/scripts/race-unit.sh", "packages/envoy/infra",
                      "scripts/race-unit.sh compile")]

DARWIN_VETS = [
    (".github/actions/lint/typecheck/action.yml", "Vet the Go daemon for darwin", "packages/daemon"),
    (".github/actions/go-envoy/envoy-go-build/action.yml", "Vet envoy for darwin, its tagged tests included",
     "packages/envoy"),
    (".github/actions/go-envoy/envoy-infra-build/action.yml", "Vet envoy-infra for darwin, its live-test tags included",
     "packages/envoy/infra"),
]


def darwin_vet_script(workdir: str) -> str:
    """The run: of a darwin vet step in `workdir`: the tag sets go_build_tags.py prints for the
    module, each a whole `-tags=` flag, taken into a variable first so a failure of the script
    fails the step before go runs, then one vet of every package per set."""
    up = "/".join([".."] * len(workdir.split("/")))
    return (f"flags=$(python3 {up}/.github/scripts/go_build_tags.py . darwin arm64)\n"
            "while IFS= read -r flag; do\n"
            '  echo "Vetting for darwin with $flag"\n'
            '  GOOS=darwin GOARCH=arm64 go vet "$flag" ./...\n'
            'done <<< "$flags"')


LIVE_GROUPS = {
    "file": "packages/daemon/internal/runtime/sandbox/live_test.go",
    "test": "TestStage4aSandboxRuntimeLive",
    "job": "kind",
    "action": ".github/actions/kind/action.yml",
}

NO_RETRY_SCRIPT = "packages/envoy/scripts/airlock-mutants.sh"

# Each (file, job, step name or None for the job's own `if:`, the term): a gate R9 names, which
# skips its job, leg or step on a pull_request or a merge_group run because of the event. The
# full-ci hold and kind's and agent-harness's selection are outputs of the changes job, which a
# job's own gate reads, and no `if:` of this list.
EVENT_GATES = [
    # worker-image builds on a pull request alone; a merge group's change is built on main.
    (".github/workflows/ci.yaml", "worker-image", None, "github.event_name == 'pull_request'"),
    # The merge queue's cancel act runs on a merge group alone, on its first attempt.
    (".github/workflows/ci.yaml", "cancel-started", None, "github.event_name == 'merge_group'"),
    # ci-ok and pr-title refuse a re-run attempt of a merge-group run in a step.
    (".github/workflows/ci.yaml", "ci-ok", "Refuse a re-run attempt of a merge-group run",
     "github.event_name == 'merge_group'"),
    (".github/workflows/ci.yaml", "pr-title", "Refuse a re-run attempt of a merge-group run",
     "github.event_name == 'merge_group'"),
    # ci-ok holds a failing verdict of a merge group's first attempt until its group is the front
    # of the queue (.github/scripts/ci-ok-hold.sh); a pull request has no queue to wait for.
    (".github/workflows/ci.yaml", "ci-ok", "Hold a failing verdict until this group is the front of the queue",
     "github.event_name == 'merge_group'"),
    # dispatch-two-builds-same runs on a merge group alone (a leg the matrix excludes otherwise).
    (".github/workflows/ci.yaml", "go-envoy-held", "strategy.matrix.exclude",
     "github.event_name != 'merge_group' && 'dispatch-two-builds-same' || ''"),
    # dispatch-browser-pr runs the pull-request lane of the browser suite on a pull request alone,
    # beside dispatch-browser's full lane, which the merge queue runs (main's #5175).
    (".github/workflows/ci.yaml", "dispatch-browser-pr", None, "github.event_name == 'pull_request'"),
    # worker-image.yaml's attestation of a pushed image, which its pull-request build never asks for.
    (".github/workflows/worker-image.yaml", "attest", None, "github.event_name != 'pull_request'"),
]

ARM_JOBS = {"airlock-arm": "ubuntu-24.04-arm"}

# Rule 32: a carrier whose old check-run name is not `<base> / <base>` for the legs it reads,
# by the base of those legs. main's #5077 named its race job `daemon-go / daemon-go-race`; the
# race step passes 22 minutes in one leg, so it is split into `daemon-go-race (i/2)` legs
# (departure 10) under a carrier that keeps main's name.
CARRIER_NAMES = {"daemon-go-race": "daemon-go / daemon-go-race"}

MAIN_CHECK_RUN_NAMES = [
    "changes", "lint / lint", "lint / private-names", "ci-images / ci-images", "tool-fallback / tool-fallback",
    "tool-fallback-arm / tool-fallback-arm", "typecheck / typecheck", "test / test", "daemon-go / daemon-go",
    "plugin-pack / plugin-pack", "private-names-port / private-names-port", "docs / build", "docs / private-names",
    "contracts / contracts", "test-fixtures / test-fixtures", "conflicts-script / conflicts-script",
    "sessions-script / sessions-script", "broker-client / broker-client", "envoy-client / envoy-client",
    "pi-shared / pi-shared", "pi-envoy / pi-envoy", "pi-legion / pi-legion", "dispatch-ui / dispatch-ui",
    "dispatch-browser / binaries", "dispatch-browser / shard", "dispatch-browser / alone",
    "dispatch-browser / multiprocess", "dispatch-browser / embedder", "dispatch-browser / visual",
    "dispatch-browser / merge", "claude-envoy / claude-envoy", "claude-envoy / private-names",
    "envoy-go-checks / envoy-go-checks", "envoy-go / envoy-go", "envoy-infra-go / envoy-infra-go",
    "text-checks / text-checks", "envoy-go-race / envoy-go-race", "envoy-go-race-api / envoy-go-race-api",
    "envoy-go-claim-model / envoy-go-claim-model", "envoy-go-race-docs / envoy-go-race-docs",
    "envoy-go-docs-finalizers / envoy-go-docs-finalizers", "dispatch-memory / dispatch-memory",
    "dispatch-multiprocess / dispatch-multiprocess", "dispatch-two-builds / dispatch-two-builds",
    "dispatch-two-builds / dispatch-two-builds-same", "listener-server-change / listener-server-change",
    "broker-multiprocess / broker-multiprocess", "airlock-lean / airlock-lean", "envoy-smoke / envoy-smoke",
    "envoy-e2e-local / envoy-e2e-local", "envoy-session-stall / envoy-session-stall",
    "proof-sdk-offline / proof-sdk-offline", "worker-image / build", "worker-image / attest",
    "browser-image / build", "browser-image / attest", "pr-title", "cancel-started", "ci-ok",
    # The names main's commits after 7cb1f229367c add (the plan's Appendix A, t35): #5077's race job
    # and #5175's pull-request halves.
    "daemon-go / daemon-go-race", "tooling-checks / tooling-checks", "pi-legion-build / pi-legion-build",
    "envoy-go-build / envoy-go-build", "envoy-infra-build / envoy-infra-build", "dispatch-browser-pr / binaries",
    "dispatch-browser-pr / shard", "dispatch-browser-pr / embedder", "dispatch-browser-pr / merge",
]

CI = ".github/workflows/ci.yaml"
_X64 = {"if": "matrix.x64"}
_ARM = {"if": "matrix.arm"}
# Each (file, job or None for an action's step, what picks the step out, minutes, how many): a
# uses: step of ci.yaml keeps its own timeout-minutes; a run: body moved into an action keeps a
# `timeout <minutes>m` command in its body. The count holds the number of such steps, so two timed
# steps put under one calling step with one bound are one fewer.
TIMED_STEPS = (
    [(CI, "tool-fallback", {**_X64, "uses": f"./.github/actions/{action}"}, 11, count)
     for action, count in (("install-changie", 3), ("install-crane", 3), ("install-docker-plugin", 4),
                           ("install-elan", 3), ("install-helm", 3), ("install-jj", 3), ("install-pulumi", 3),
                           ("install-uv", 3), ("setup-bun", 3))]
    + [(CI, "tool-fallback", {**_X64, "uses": f"./base/.github/actions/{action}"}, 11, 1)
       for action in ("install-changie", "install-crane", "install-elan", "install-jj", "install-pulumi",
                      "install-uv", "setup-bun")]
    + [(CI, "tool-fallback", {**_ARM, "uses": f"./.github/actions/{action}"}, 11, 2)
       for action in ("install-changie", "install-crane", "install-elan", "install-jj", "setup-bun")]
    + [
        (".github/actions/go-envoy-held/envoy-go/action.yml", None, {"name": "Test the database skip counter"}, 15, 1),
        (CI, "go-envoy-held", {"name": "Count the database broker suites' skips"}, 30, 1),
        (".github/actions/go-envoy-held/envoy-go/action.yml", None,
         {"name": "Prove the hub behind the NATS auth service on the local stack (callout-check.sh)"}, 6, 1),
        # dispatch-two-builds and dispatch-two-builds-same share these three steps of ci.yaml.
        (CI, "go-envoy-held", {"id": "checkout"}, 2, 1),
        (CI, "go-envoy-held", {"name": "End the git processes and the workspace a first attempt cut at its budget leaves"},
         2, 1),
        (CI, "go-envoy-held", {"name": "Check out again, after a first attempt that did not complete"}, 2, 1),
        (".github/actions/go-envoy-held/dispatch-two-builds/action.yml", None, {"name": "Install Playwright Chromium"},
         4, 1),
        (".github/actions/go-envoy-held/dispatch-two-builds/action.yml", None,
         {"name": "Read the connections the two-build stack's Postgres must allow"}, 2, 1),
        # One step of kind's job, for every leg.
        (CI, "kind", {"name": "Download the worker image the image job built"}, 10, 1),
        # One step of agent-harness's action, for its session-settings and session-scripts legs.
        (".github/actions/agent-harness/action.yml", None, {"name": "Install Playwright Chromium"}, 4, 1),
    ]
)
