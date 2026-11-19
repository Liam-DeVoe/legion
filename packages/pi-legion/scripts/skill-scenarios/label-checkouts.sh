#!/usr/bin/env bash
# label-checkouts.sh: a fresh set of the ablate labels' checkouts, each a clone of its own at one
# commit with its label's cuts applied, and the proof that no text its role reads still states the
# label's rule.
#
#   label-checkouts.sh <repository> <commit> <patch>...
#
# <repository> is a clone or a worktree of cybermonklabs/legion that holds <commit>. Each <patch> is
# a file named <label>.patch, one for each label that label-rules.ts names (its RULES) and no other,
# and each a diff that `git apply` applies to <commit>. In order:
#   1. it clones <commit> once, with no patch, into a new directory (mktemp -d): the head that the
#      labels are cut from. That directory also gets labels.tsv, a line `<label>\t<checkout>` for
#      each label, so that no file under the labels' own directory names a label;
#   2. it clones <commit> once for each label into a second new directory, under a name of 16
#      random hexadecimal digits that names no label, and applies the label's patch there;
#   3. it runs `bun install --frozen-lockfile` in each label's checkout, which the rig's profile
#      build needs;
#   4. it runs `rule-search.ts <checkout> cut <label> <head>` over each label's checkout, which
#      fails where a text that the label's role reads still states its rule, or where a sentence
#      that the head's search cuts is still there.
# It prints the two directories and labels.tsv, and exits 0 only when every step held for every
# label. It removes nothing: the caller deletes the two directories when the runs are done.
set -euo pipefail

me="label-checkouts"
refuse() {
  echo "$me: $*" >&2
  exit 2
}

[ $# -ge 3 ] || refuse "usage: $me <repository> <commit> <label>.patch..."
repository=$1 commit=$2
shift 2
git -C "$repository" rev-parse --verify --quiet "$commit^{commit}" >/dev/null ||
  refuse "$repository holds no commit $commit"

head=$(mktemp -d)
labels=$(mktemp -d)
git clone --quiet --no-checkout "$repository" "$head/head"
git -C "$head/head" checkout --quiet --detach "$commit"
scenarios=$head/head/packages/pi-legion/scripts/skill-scenarios
search=$scenarios/rule-search.ts
[ -f "$search" ] && [ -f "$scenarios/label-rules.ts" ] ||
  refuse "$commit holds no rule-search.ts and label-rules.ts in packages/pi-legion/scripts/skill-scenarios"

# The labels label-rules.ts names at <commit>, each of which needs exactly one patch.
mapfile -t known < <(bun -e "import { RULES } from '$scenarios/label-rules.ts'; for (const label of Object.keys(RULES)) console.log(label);")
[ ${#known[@]} -gt 0 ] || refuse "label-rules.ts at $commit names no label"
declare -A patch_of=()
for patch in "$@"; do
  label=$(basename -- "$patch" .patch)
  [ -f "$patch" ] || refuse "no file $patch"
  [ -z "${patch_of[$label]:-}" ] || refuse "two patches for $label"
  patch_of[$label]=$(cd "$(dirname -- "$patch")" && pwd -P)/$(basename -- "$patch")
done
for label in "${known[@]}"; do
  [ -n "${patch_of[$label]:-}" ] || refuse "no patch for $label (want ${known[*]})"
done
[ ${#patch_of[@]} -eq ${#known[@]} ] || refuse "a patch names no label of label-rules.ts (its labels: ${known[*]})"

tsv=$head/labels.tsv
: >"$tsv"
for label in "${known[@]}"; do
  name=$(od -An -N8 -tx1 /dev/urandom | tr -d ' \n')
  checkout=$labels/$name
  git clone --quiet --no-checkout "$repository" "$checkout"
  git -C "$checkout" checkout --quiet --detach "$commit"
  git -C "$checkout" apply "${patch_of[$label]}"
  [ -n "$(git -C "$checkout" status --porcelain)" ] || refuse "the patch of $label changes nothing at $commit"
  (cd "$checkout" && bun install --frozen-lockfile >/dev/null)
  printf '%s\t%s\n' "$label" "$checkout" >>"$tsv"
done

failed=0
while IFS=$'\t' read -r label checkout; do
  if bun "$search" "$checkout" cut "$label" "$head/head" >"$head/cut-$label.txt" 2>&1; then
    echo "$me: $label: $(grep -E "^$label at " "$head/cut-$label.txt")"
  else
    failed=1
    echo "$me: $label: FAULT, rule-search.ts cut exited non-zero ($head/cut-$label.txt):" >&2
    grep -E '^FAULT' "$head/cut-$label.txt" >&2 || true
  fi
done <"$tsv"

echo "$me: head $head/head, labels $labels, list $tsv"
exit "$failed"
