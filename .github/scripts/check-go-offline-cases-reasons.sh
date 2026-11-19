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

# file_reason_at <name> <line>: a fixture whose tools/list-deps.sh holds its file reason on <line>,
# comment lines above it, and one go command after it, run through the check.
file_reason_at() {
  root=$(fixture "$1")
  {
    echo '#!/usr/bin/env bash'
    for ((n = 2; n < $2; n++)); do echo "# line $n"; done
    echo '# go-offline-file: a person runs this by hand.'
    echo 'go list -deps ./... >/dev/null'
  } | put "$root" tools/list-deps.sh
  run_check "$root"
}

echo "case: a file reason counts within the file's first 40 lines, and not on line 41"
file_reason_at file-reason-line-40 40
check "on line 40: exits 0" "$(is "$status" 0)"
check "on line 40: covers the command" "$(contains "$out" 'is in a file whose reason covers it (1)')"
file_reason_at file-reason-line-41 41
refused tools/list-deps.sh 42

# reached_reason <name> <workflow>: a fixture whose tools/inner.sh holds a file reason and a go
# command, run by tools/outer.sh, which a step of <workflow> runs ("ci" adds it to ci.yaml,
# "nightly" to a workflow outside the required set); run through the check.
reached_reason() {
  root=$(fixture "$1")
  put "$root" tools/inner.sh <<'EOF'
#!/usr/bin/env bash
# go-offline-file: a person runs this by hand; no CI job runs it.
go list -deps ./... >/dev/null
EOF
  printf '#!/usr/bin/env bash\nbash "$(dirname "$0")/inner.sh"\n' | put "$root" tools/outer.sh
  if [ "$2" = ci ]; then
    printf '      - name: List the modules\n        run: bash tools/outer.sh\n' >>"$root/.github/workflows/ci.yaml"
  else
    printf '      - name: List the modules\n        run: bash tools/outer.sh\n' | nightly "$root"
  fi
  run_check "$root"
}

echo "case: a go-offline-file reason in a script a required job reaches through another script is refused"
reached_reason file-reason-reached ci
check "fails" "$(is "$status" 1)"
check "refuses the reason, naming the step that reaches it" \
  "$(contains "$out" 'tools/inner.sh:1: a go-offline-file reason, which this file may not carry: a step of a required workflow reaches it (.github/workflows/ci.yaml job lint, step "List the modules")')"
check "and refuses the command it would cover" "$(contains "$out" 'tools/inner.sh:3: a go command with no GOPROXY=off')"
reached_reason file-reason-reached-outside nightly
check "reached only from a workflow outside the required set: exits 0" "$(is "$status" 0)"
check "and its reason covers the command" "$(contains "$out" 'is in a file whose reason covers it (1)')"

echo "case: a go-offline-file reason in a program a required job runs, or in TypeScript a required test run loads, is refused"
root=$(fixture file-reason-program)
put "$root" tools/list_deps.py <<'EOF'
# go-offline-file: a person runs this by hand.
import subprocess
subprocess.run(["go", "list", "-deps", "./..."])
EOF
# ci.yaml names tools/run.sh alone, which hands the program to python3.
printf '#!/usr/bin/env bash\npython3 "$(dirname "$0")/list_deps.py"\n' | put "$root" tools/run.sh
printf '      - name: List the modules\n        run: bash tools/run.sh\n' >>"$root/.github/workflows/ci.yaml"
run_check "$root"
check "a python3 program: refused" \
  "$(contains "$out" 'tools/list_deps.py:1: a go-offline-file reason, which this file may not carry: a step of a required workflow reaches it')"
root=$(fixture file-reason-test-run)
put "$root" packages/web/src/build.ts <<'EOF'
// go-offline-file: a person runs this by hand.
export const build = () => Bun.spawnSync(["go", "build", "./..."]);
EOF
printf '      - name: Test the web package\n        run: bun test\n        working-directory: packages/web\n' \
  >>"$root/.github/workflows/ci.yaml"
run_check "$root"
check "TypeScript under a bun test run's directory: refused" \
  "$(contains "$out" 'packages/web/src/build.ts:1: a go-offline-file reason, which this file may not carry: a step of a required workflow reaches it')"
