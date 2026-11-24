#!/usr/bin/env bash
# Resolves the Dispatch API's generated files in a merge (or rebase) of main into a branch made
# before main changed how they are kept. Run it from anywhere in the checkout, after
# `git merge origin/main` (or a rebase step) stops on conflicts, or after a merge that did not stop:
#
#   packages/contracts/scripts/adopt-api-layout.sh
#
# It changes only these generated files, and stages what it changes. Each other conflict stays
# yours: it lists them when it ends. Run it again after you resolve a Go file it names.
#
#   1. packages/contracts/src/dispatch-api.generated.ts is in no commit on main (.gitignore): every
#      `bun install` renders it. A branch that changed it meets main's deletion as a conflict, so
#      the file leaves the index here and stays on disk, ignored. This refuses to run before main's
#      root .gitignore names the file, where that would only delete the file from the branch. It
#      reads that line itself: a local or global exclude that ignores the file is not main's layout.
#   2. packages/contracts/schemas/dispatch-openapi.json is emitted from the Go route table, so a
#      conflict in it is resolved by emitting it again from the merged Go
#      (`go run ./cmd/dispatch openapi --out ...`), never by hand. That waits until no Go file of
#      packages/envoy is still in conflict, since the document is emitted from them.
#   3. The generated types are rendered again from the document the tree now holds, so a type
#      check reads the merged API.
#
# A step that main's later layout changes add (the document's line format, one file per route)
# is added here by the change that makes it.
set -euo pipefail

root=$(git rev-parse --show-toplevel)
cd "$root"
generated=packages/contracts/src/dispatch-api.generated.ts
document=packages/contracts/schemas/dispatch-openapi.json

unmerged() { git diff --name-only --diff-filter=U -- "$@"; }

# Step 1.
if ! grep -qxF -- "$generated" .gitignore; then
  echo "adopt-api-layout: the root .gitignore does not name $generated, so main's layout is not in this" \
    "tree yet: merge origin/main (or rebase onto it) first, then run this again" >&2
  exit 1
fi
if [ -n "$(git ls-files -- "$generated")" ]; then
  git rm -q --cached -- "$generated"
  echo "adopt-api-layout: $generated left the index (every bun install renders it); the file stays on disk"
fi

# Step 2.
if [ -n "$(unmerged "$document")" ]; then
  go_conflicts=$(unmerged packages/envoy)
  if [ -n "$go_conflicts" ]; then
    echo "adopt-api-layout: $document is emitted from the Go route table, and these files of it are" \
      "still in conflict; resolve them, then run this again:" >&2
    sed 's/^/  /' <<<"$go_conflicts" >&2
    exit 1
  fi
  (cd packages/envoy && go run ./cmd/dispatch openapi --out "../../$document")
  git add -- "$document"
  echo "adopt-api-layout: $document emitted again from the merged route table and staged"
fi

# Step 3.
if [ -n "$(unmerged "$document")" ] || ! bun packages/contracts/scripts/gen-api.ts; then
  echo "adopt-api-layout: $generated was not rendered: run \`bun install\` at the repository root" \
    "once the document has no conflict" >&2
  exit 1
fi
echo "adopt-api-layout: $generated rendered from $document"

left=$(git diff --name-only --diff-filter=U)
if [ -n "$left" ]; then
  echo "adopt-api-layout: still in conflict, yours to resolve:"
  sed 's/^/  /' <<<"$left"
fi
