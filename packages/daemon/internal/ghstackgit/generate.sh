#!/usr/bin/env bash
# Writes shapes.go beside this script: every argument list the gh-stack release the worker image
# pins (packages/daemon/docker/worker.Dockerfile, ARG GH_STACK_TOOL) starts git with, read by
# gen/main.go from the Go source of that release's internal/git, of the cli/cli git client it runs
# git through, and of go-gh's internal/git, at the versions its go.mod names. Run it again whenever
# the pin moves; cmd/legion's TestGhStackGitShapesAreThePinnedReleases fails until it has been.
#
# It reads the three repositories at their tags over the network, and needs git and go.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
dockerfile="$here/../../docker/worker.Dockerfile"
version="$(sed -n 's|^ARG GH_STACK_TOOL=github:github/gh-stack@||p' "$dockerfile")"
[ -n "$version" ] || { echo "no ARG GH_STACK_TOOL pin of github/gh-stack in $dockerfile" >&2; exit 1; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
git clone --quiet --depth 1 --branch "v$version" https://github.com/github/gh-stack "$work/gh-stack"
module() {
  sed -n "s|^[[:space:]]*$1 \\(v[^ ]*\\).*|\\1|p" "$work/gh-stack/go.mod"
}
cli="$(module github.com/cli/cli/v2)"
gogh="$(module github.com/cli/go-gh/v2)"
[ -n "$cli" ] && [ -n "$gogh" ] || { echo "gh-stack v$version's go.mod names no cli/cli or go-gh" >&2; exit 1; }
git clone --quiet --depth 1 --branch "$cli" https://github.com/cli/cli "$work/cli"
git clone --quiet --depth 1 --branch "$gogh" https://github.com/cli/go-gh "$work/go-gh"

(cd "$here" && go run ./gen -gh-stack "$work/gh-stack" -cli "$work/cli" -go-gh "$work/go-gh" -version "$version") >"$work/shapes.go"
gofmt "$work/shapes.go" >"$here/shapes.go"
echo "wrote $here/shapes.go: $(grep -c '^	{' "$here/shapes.go") shapes from gh-stack v$version, cli/cli $cli and go-gh $gogh"
