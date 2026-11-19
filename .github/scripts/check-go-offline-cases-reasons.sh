# shellcheck shell=bash
# Cases of check-go-offline.sh's file reasons: where a go-offline-file reason is refused, and where
# it covers its file's go commands. Sourced by check-go-offline.test.sh, whose fixtures, run_check
# and counters they use; not run on its own.

echo "case: a go-offline-file reason in a test file is refused, and its command with it"
root=$(fixture file-reason-test)
put "$root" tools/deps.test.sh <<'EOF'
#!/usr/bin/env bash
# go-offline-file: a person runs this by hand.
go list -deps ./... >/dev/null
EOF
run_check "$root"
check "fails" "$(is "$status" 1)"
check "refuses the reason, saying why" \
  "$(contains "$out" 'tools/deps.test.sh:1: a go-offline-file reason, which this file may not carry: it is a test file')"
check "and refuses the command it would cover" "$(contains "$out" 'tools/deps.test.sh:3: a go command with no GOPROXY=off')"

echo "case: a go-offline-file reason in a file ci.yaml names is refused"
root=$(fixture file-reason-ci)
put "$root" tools/list-deps.sh <<'EOF'
#!/usr/bin/env bash
# go-offline-file: a person runs this by hand.
go list -deps ./... >/dev/null
EOF
printf '      - run: bash tools/list-deps.sh\n' >>"$root/.github/workflows/ci.yaml"
run_check "$root"
check "fails" "$(is "$status" 1)"
check "refuses the reason, naming ci.yaml" \
  "$(contains "$out" 'tools/list-deps.sh:1: a go-offline-file reason, which this file may not carry: a required workflow names it (.github/workflows/ci.yaml)')"
check "and refuses the command" "$(contains "$out" 'tools/list-deps.sh:3: a go command with no GOPROXY=off')"

echo "case: a go-offline-file reason in a file a workflow ci.yaml calls names is refused"
root=$(fixture file-reason-called)
put "$root" tools/list-deps.sh <<'EOF'
#!/usr/bin/env bash
# go-offline-file: a person runs this by hand.
go list -deps ./... >/dev/null
EOF
printf '  checks:\n    uses: ./.github/workflows/ci-job-checks.yaml\n' >>"$root/.github/workflows/ci.yaml"
put "$root" .github/workflows/ci-job-checks.yaml <<'EOF'
name: CI job checks
on: workflow_call
jobs:
  checks:
    runs-on: ubuntu-24.04
    steps:
      - run: tools/list-deps.sh
EOF
run_check "$root"
check "fails" "$(is "$status" 1)"
check "refuses the reason, naming the called workflow" \
  "$(contains "$out" 'a required workflow names it (.github/workflows/ci-job-checks.yaml)')"

echo "case: a go-offline-file reason in a file only a workflow outside the required set, and a comment of ci.yaml, name passes"
root=$(fixture file-reason-allowed)
put "$root" tools/list-deps.sh <<'EOF'
#!/usr/bin/env bash
# go-offline-file: a person runs this by hand.
go list -deps ./... >/dev/null
EOF
printf '      # tools/list-deps.sh is a person'"'"'s.\n' >>"$root/.github/workflows/ci.yaml"
nightly "$root" <<'EOF'
      - run: bash tools/list-deps.sh
EOF
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "counts the command as covered by its file's reason" "$(contains "$out" 'is in a file whose reason covers it (1)')"
