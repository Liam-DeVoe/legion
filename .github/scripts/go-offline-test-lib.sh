#!/usr/bin/env bash
# Sourced by the harnesses of the two checks whose go commands run through go_offline.py,
# check-ci-filters.test.sh and check-legion-version-inputs.test.sh: a stand-in go that records the
# environment each go command of the check under test runs in, so a case of each harness can hold
# that environment to the one go_offline.py promises. A change that breaks the module's offline
# environment then fails both harnesses, not only a test of the module.
#
# go_spy <dir>: writes a stand-in `go` into <dir>/shim, for the caller to put first on PATH. It
# answers the toolchain lookup, `go env GOROOT`, with <dir>/root, and runs the real go for any
# other command. <dir>/root/bin/go, which the check then runs every go command with, appends one
# line per command to <dir>/log - its GOPROXY, its GOMODCACHE and how many files that cache holds
# when the command starts - and runs the real toolchain's go. The real one is what `go env GOROOT`
# names where the harness runs.
#
# go_spy_verdict <dir>: `offline` when the log holds at least one command and every one ran with
# GOPROXY=off over a module cache that is not this machine's and holds no file; else the first
# line that did not, or `no go command`.
#
# Not executable: `source "$(dirname "${BASH_SOURCE[0]}")/go-offline-test-lib.sh"`.

go_spy() {
  local dir=$1 real
  real="$(go env GOROOT)/bin/go"
  mkdir -p "$dir/shim" "$dir/root/bin"
  cat > "$dir/shim/go" <<SH
#!/usr/bin/env bash
if [ "\$*" = "env GOROOT" ]; then
  echo "$dir/root"
  exit 0
fi
exec "$real" "\$@"
SH
  cat > "$dir/root/bin/go" <<SH
#!/usr/bin/env bash
files=\$(find "\${GOMODCACHE:-/nonexistent}" -type f 2>/dev/null | wc -l)
printf '%s\t%s\t%s\n' "\${GOPROXY-unset}" "\${GOMODCACHE-unset}" "\$files" >> "$dir/log"
exec "$real" "\$@"
SH
  chmod +x "$dir/shim/go" "$dir/root/bin/go"
}

go_spy_verdict() {
  local dir=$1 ambient proxy cache files
  ambient=$(go env GOMODCACHE)
  if [ ! -s "$dir/log" ]; then
    echo "no go command"
    return
  fi
  while IFS=$'\t' read -r proxy cache files; do
    if [ "$proxy" != off ] || [ "$cache" = unset ] || [ "$cache" = "$ambient" ] || [ "$files" != 0 ]; then
      echo "GOPROXY=$proxy GOMODCACHE=$cache holding $files files"
      return
    fi
  done < "$dir/log"
  echo offline
}
