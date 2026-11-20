#!/usr/bin/env bash
# pid1-rows.sh runs the Bun rows that once failed where their test runner is PID 1 of its PID
# namespace, as a container with no init runs it: each judged a killed orphan gone only once
# something reaped it, or took its own parent for a fixed process, and nothing reaps an orphan
# there but the runner, which Bun does only for the children it started. It runs each twice in a
# user and PID namespace of its own with its own /proc (unshare --user --map-current-user --pid
# --fork --mount-proc, no privilege): once with `bun test` itself as PID 1, and once with a bash as
# PID 1 that runs it, which reaps as an init does. A run that passes on the host alone passes CI
# today, so a revert of any of these fixes is caught only here.
#
# A runner that refuses an unprivileged user namespace (an AppArmor restriction, a container that
# drops CAP_SYS_ADMIN from new namespaces) cannot run the rows so; it says so as a warning naming
# the refusal, and exits 0, since the host run of the same rows still runs in the job's own steps.
set -euo pipefail
cd "$(dirname "$0")/../.."

ns=(unshare --user --map-current-user --pid --fork --mount-proc)
if ! refusal=$("${ns[@]}" true 2>&1); then
  echo "::warning title=PID 1 rows not run::this runner refuses ${ns[*]}: ${refusal}"
  exit 0
fi

# Each row: the package directory it runs from (its bunfig.toml), its file, and a name filter.
rows=(
  ".|scripts/e2e/lib/run-processes.test.ts|"
  "packages/dispatch|e2e/__tests__/local-instance-support.test.ts|Supervisor.stop"
  "packages/envoy-client|src/__tests__/envoy-config.test.ts|a ! command in envoy.json"
  "packages/envoy-client|src/__tests__/process-ancestry.test.ts|"
  "packages/pi-shared|src/jj-attribution.test.ts|userConfigChain"
  "packages/pi-envoy|src/typed-shell.test.ts|running a person's command"
  "packages/claude-envoy|tests/codex-session.test.ts|"
)
status=0
for runner in bun bash; do
  for row in "${rows[@]}"; do
    IFS='|' read -r dir file filter <<<"$row"
    args=(bun test "$file")
    [ -z "$filter" ] || args+=(-t "$filter")
    echo "::group::${runner} as PID 1: ${dir}/${file}${filter:+ -t $filter}"
    if [ "$runner" = bun ]; then
      (cd "$dir" && "${ns[@]}" "${args[@]}") || status=1
    else
      (cd "$dir" && "${ns[@]}" bash -c '"$@"; exit $?' pid1 "${args[@]}") || status=1
    fi
    echo "::endgroup::"
  done
done
exit "$status"
