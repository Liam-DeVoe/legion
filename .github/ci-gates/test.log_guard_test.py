# Both guard harnesses run in one step, so ci.yaml carries one output and one input for them
# (its 64,000-byte limit, check-workflow-files.sh): the first harness is harness()'s, the second
# a subject run by the same step.
GATE = harness("log_guard_test", "Test the log guard and the check that every network command a step runs is guarded",
        f"{SCRIPTS}/ci-log-guard.test.sh", [
    (f"{SCRIPTS}/ci-log-guard.sh", "the library under test"),
    (".github/workflows/release.yaml", "ci-log-guard.test.sh reads its npm publish steps' inline guard out of it"),
    (".github/workflows/release-envoy-listener.yaml", "ci-log-guard.test.sh reads its Determine latest step's inline guard out of it"),
    (f"{SCRIPTS}/check-log-guard.test.sh", "the step runs it after ci-log-guard.test.sh"),
    (f"{SCRIPTS}/check-log-guard.sh", "check-log-guard.test.sh's script under test, symlinked into every fixture"),
    (f"{SCRIPTS}/shell_commands.py", "the command scanner the check imports, symlinked in beside it"),
    (".github/log-guard-allow.tsv", "check-log-guard.test.sh runs the check over a copy of this repository's .github"),
    (".github/workflows/**", "check-log-guard.test.sh runs the check over a copy of this repository's .github"),
    (".github/actions/**", "check-log-guard.test.sh runs the check over a copy of this repository's .github"),
])
