#!/usr/bin/env bash
# Fetches origin's main and the legion-envoy-v* release tags, with their history but none of their
# files (--filter=blob:none), into a checkout of one commit: what build-dispatch-build.sh reads to
# resolve the two-build legs' earlier builds, and `git archive` fetches a release's files when it
# builds one. ci.yaml's go-envoy-held job runs it after the dispatch-two-builds legs' checkout
# (.github/actions/go-envoy-held/dispatch-two-builds says why). Three attempts of 30 s each; between
# two it ends every git process on the runner, which runs this one leg, and deletes the locks a
# killed git can leave. Run from the checkout's root; the repository is public, so it needs no
# token.
set -uo pipefail

for attempt in 1 2 3; do
  args=(--no-tags --filter=blob:none)
  [[ -f .git/shallow ]] && args+=(--unshallow)
  if timeout -k 5 30 git fetch "${args[@]}" origin "$(git rev-parse HEAD)" \
    '+refs/heads/main:refs/remotes/origin/main' \
    '+refs/tags/legion-envoy-v*:refs/tags/legion-envoy-v*'; then
    echo "attempt $attempt: $(git rev-list --count HEAD) commits in the history of HEAD, $(git tag --list 'legion-envoy-v*' | wc -l) release tags"
    exit 0
  fi
  echo "attempt $attempt did not complete within 30 s"
  pkill -KILL -x git || true
  pkill -KILL -f git-remote-http || true
  while pgrep -x git > /dev/null || pgrep -f git-remote-http > /dev/null; do sleep 1; done
  find .git -name '*.lock' -delete
done
echo "::error::Three fetches of main's history and the release tags did not complete within 30 s each."
exit 1
