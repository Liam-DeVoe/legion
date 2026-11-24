# The release_push_test filter's gate: what job test reads.
GATE = harness("release_push_test", "Test release scripts", f"{SCRIPTS}/release-push.test.sh", [
    (f"{SCRIPTS}/release-push.sh", "the script under test"),
    (f"{SCRIPTS}/gh-read.sh", "release-push.sh's `restart`, `release` and `release-section` each source it for their gh "
                              "calls, which the stub gh answers"),
    (f"{SCRIPTS}/release-bump.sh", "release-push.sh's `version` runs it, and the version cases read its refusals"),
    (f"{SCRIPTS}/npm-published-version.sh", "release-push.sh's `version` runs it for `--first-npm`, and the version cases drive it against a stub npm"),
    *ci_retry("release-push.sh's `version` sources it for `--first-npm`, and the version cases read its retries"),
    (".github/workflows/release.yaml", "release-push.test.sh's worker_image_release case reads that job's step out of it and runs it"),
    (".github/workflows/release-envoy-listener.yaml",
     "release-push.test.sh's Determine latest case reads release_publish's step out of it and runs its image read"),
    ("packages/pi-legion/.changie.yaml", "release-push.test.sh's fold-notes case for the shipped configuration copies it into its fixture"),
])
