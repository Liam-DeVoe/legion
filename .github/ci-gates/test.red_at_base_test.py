# A harness of the job that is not under .github/scripts and sources no test-lib.sh.
GATE = harness("red_at_base_test", "Test the red-at-base tool (a scratch repository)",
        "scripts/red-at-base.test.sh", [
    ("scripts/red-at-base.sh", "the script under test (scripts/red-at-base.test.sh:41 names it)"),
], lib=False)
