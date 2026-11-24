#!/usr/bin/env bash
# Prints the daemon API contract number of a commit, or of the working tree when given none: the
# number a daemon built from it speaks and its plugins declare in `legion.daemonApiVersion`.
#
# usage: scripts/daemon-contract.sh [<rev>]
#   <rev>  the commit to read; with none, the working tree of the checkout that holds this script
#   exit: 0 printed, 1 the tree holds no contract (it names why), 2 a usage error
#
# The number is the greatest of packages/contracts/fixtures/daemon-api/released-contracts.json,
# the numbered entries frozen at the cutover to entry files (api.LastNumberedContract, L), plus
# the count of entry fixtures under packages/contracts/fixtures/daemon-api/contracts/, which holds
# one for each entry and nothing else; an absent directory is no entry. A tree from before that
# cutover has no released-contracts.json, and its number is version.go's
# `const DaemonAPIVersion = <n>` line. The merge order changes no build's own number, only which
# entry each number names, so this reads no history: given no <rev> it reads files alone and
# works in a tree with no .git, as the worker image's plugin stage is. Given a <rev> it reads that
# commit's objects, never the working tree.
#
# Run from anywhere: scripts/daemon-contract.sh [<rev>]
set -euo pipefail

readonly name=daemon-contract.sh
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly root
readonly released=packages/contracts/fixtures/daemon-api/released-contracts.json
readonly fixtures=packages/contracts/fixtures/daemon-api/contracts
readonly version=packages/daemon/internal/api/version.go

usage() {
  sed -n '/^# usage: /,/^#   exit: /p' "$0" | sed 's/^# \{0,1\}//' >&2
  exit 2
}

die() {
  echo "${name}: $*" >&2
  exit 1
}

[[ $# -le 1 ]] || usage
[[ ${1:-} != -* ]] || usage

# greatest <where>: the greatest number of the released-contracts.json read on standard input.
greatest() {
  jq -er 'if type == "array" and length > 0 and all(.[]; type == "number") then max else empty end' ||
    die "${released} of $1 is no array of contract numbers"
}

# constant <where>: the number of the `const DaemonAPIVersion = <n>` line read on standard input.
constant() {
  local n
  n=$(sed -En 's/^const DaemonAPIVersion = ([0-9]+)$/\1/p')
  [[ -n $n ]] || die "$1 holds neither ${released} nor a \`const DaemonAPIVersion = <n>\` line in ${version}"
  echo "$n"
}

if [[ $# == 1 ]]; then
  rev=$(git -C "$root" rev-parse --verify --quiet "${1}^{commit}") || die "$1 names no commit of ${root}"
  if git -C "$root" cat-file -e "${rev}:${released}" 2>/dev/null; then
    last=$(git -C "$root" cat-file blob "${rev}:${released}" | greatest "$1")
    count=$(git -C "$root" ls-tree --name-only "$rev" "${fixtures}/" | awk '/[.]json$/ { n++ } END { print n + 0 }')
    echo $((last + count))
  else
    git -C "$root" cat-file blob "${rev}:${version}" 2>/dev/null | constant "$1"
  fi
elif [[ -f "$root/$released" ]]; then
  last=$(greatest 'the working tree' <"$root/$released")
  shopt -s nullglob
  entries=("$root/$fixtures"/*.json)
  echo $((last + ${#entries[@]}))
elif [[ -f "$root/$version" ]]; then
  constant 'the working tree' <"$root/$version"
else
  die "the working tree at ${root} holds neither ${released} nor ${version}"
fi
