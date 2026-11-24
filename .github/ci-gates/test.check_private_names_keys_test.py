# The check_private_names_keys_test filter's gate: what job test reads.
GATE = harness("check_private_names_keys_test", "Test the private-name check's issue-key rule", f"{SCRIPTS}/check-private-names-keys.test.sh", [
    (f"{SCRIPTS}/check-private-names-test-lib.sh", "check-private-names-keys.test.sh:26 sources it for its fixtures"),
    (TEST_LIB, "check-private-names-test-lib.sh:37 sources it for check/is/summary"),
    (f"{SCRIPTS}/check-private-names.sh", "the script under test"),
    (f"{SCRIPTS}/check-pr-text.sh", "check-private-names-test-lib.sh's run_pr_text runs it, over a pull request's new text"),
    (f"{SCRIPTS}/gh-read.sh", "check-pr-text.sh sources it from its own directory for every gh read"),
], lib=False)
GATE = with_reads(GATE, reads(*ci_retry("check-pr-text.sh sources it from its own directory for every read of the API")))
