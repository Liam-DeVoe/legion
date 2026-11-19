# shellcheck shell=bash
# Cases of check-go-offline.sh's rule 1 in shell test files: a bare site, the offline and the
# reasoned form, what starts nothing, and one planted case for each shell form the check's header
# names. Sourced by check-go-offline.test.sh, whose fixtures, run_check and counters they use; not
# run on its own.

echo "case: a shell test that runs go list -deps with no rule is refused"
root=$(fixture sh-bare)
put "$root" tools/deps.test.sh <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
go list -deps ./... >/dev/null
EOF
run_check "$root"
refused tools/deps.test.sh 3

echo "case: the shell test with GOPROXY=off on the command passes, and so does an export of it above"
root=$(fixture sh-offline)
put "$root" tools/deps.test.sh <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
GOPROXY=off go list -deps ./... >/dev/null
EOF
put "$root" tools/exported.test.sh <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
export GOPROXY=off
go list -deps ./... >/dev/null
EOF
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "counts both commands as offline" "$(contains "$out" 'runs with GOPROXY=off (2), carries a reason (0)')"

echo "case: the shell test with a reason on the line above passes"
root=$(fixture sh-reasoned)
put "$root" tools/deps.test.sh <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
# go-offline: it lists the standard library, which reads no module.
go list -deps std >/dev/null
EOF
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "counts the command as reasoned" "$(contains "$out" 'runs with GOPROXY=off (0), carries a reason (1)')"

echo "case: a shell go -C with a quoted directory before its subcommand is a site"
root=$(fixture sh-chdir)
put "$root" tools/deps.test.sh <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
go -C "$root/packages/app" list -deps ./... >/dev/null
EOF
run_check "$root"
refused tools/deps.test.sh 3

echo "case: a go command in a comment, a quoted string or a heredoc's body starts nothing"
root=$(fixture sh-prose)
put "$root" tools/prose.test.sh <<'EOF'
#!/usr/bin/env bash
# go list -deps ./... in a comment starts nothing.
echo "go list -deps ./..."
cat <<'TEXT'
go list -deps ./...
TEXT
EOF
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "counts no command at all" "$(contains "$out" 'every one of the 0 go commands')"


echo "case: shell: bash -c, eval, and a heredoc a shell reads"
refuses sh-bash-c tools/bash.test.sh 2 <<'EOF'
#!/usr/bin/env bash
bash -c 'go list -deps ./...'
EOF
refuses sh-eval tools/eval.test.sh 2 <<'EOF'
#!/usr/bin/env bash
eval "go list -deps ./..."
EOF
refuses sh-heredoc tools/heredoc.test.sh 3 <<'EOF'
#!/usr/bin/env bash
bash <<'SCRIPT'
go list -deps ./...
SCRIPT
EOF

echo "case: shell: the later command of a pipeline needs its own GOPROXY=off"
refuses sh-pipeline tools/pipeline.test.sh 3 2 <<'EOF'
#!/usr/bin/env bash
GOPROXY=off go list -deps ./... |
  xargs go build
EOF
refuses sh-pipeline-line tools/pipeline-line.test.sh 2 <<'EOF'
#!/usr/bin/env bash
GOPROXY=off go list -deps ./... | xargs go build
EOF

echo "case: shell: an export GOPROXY=off inside a function covers that function alone"
refuses sh-function-export tools/function.test.sh 6 <<'EOF'
#!/usr/bin/env bash
setup() {
  export GOPROXY=off
}
setup
go list -deps ./...
EOF
passes_offline sh-same-function tools/function.test.sh <<'EOF'
#!/usr/bin/env bash
build() {
  export GOPROXY=off
  go build ./...
}
EOF

echo "case: shell: the go binary as a path ending /go, and as a variable set to it"
refuses sh-goroot tools/goroot.test.sh 2 <<'EOF'
#!/usr/bin/env bash
"$GOROOT/bin/go" list -deps ./...
EOF
refuses sh-variable tools/variable.test.sh 3 <<'EOF'
#!/usr/bin/env bash
GO=$(command -v go)
"$GO" build ./...
EOF

echo "case: shell: env -u GOPROXY and a later GOPROXY undo an earlier GOPROXY=off"
refuses sh-env-unset tools/unset.test.sh 3 <<'EOF'
#!/usr/bin/env bash
export GOPROXY=off
env -u GOPROXY go list -deps ./...
EOF
refuses sh-later tools/later.test.sh 2 <<'EOF'
#!/usr/bin/env bash
GOPROXY=off GOPROXY=direct go list -deps ./...
EOF
