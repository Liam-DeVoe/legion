#!/usr/bin/env bash
# Writes what `git merge-tree --write-tree -z` prints for each shape of merge that
# internal/jjmerge/parse_test.go reads, one file <shape>.z per shape, into the directory named as
# the one argument:
#
#   bash capture.sh <dir>
#
# The files beside this script came from git 2.47.3, the worker image's. When that pin moves, run
# it into this directory with the new git, and write the ids the new files hold
# (`tr '\0' '\n' < <shape>.z`) into parse_test.go.
#
# It reads no configuration. Each history is built in a new repository under a temporary
# directory, and git runs under `env -i`: none of the caller's variables reach it (a pod's git
# carries settings in GIT_CONFIG_KEY_n), no user or system configuration file is read
# (GIT_CONFIG_GLOBAL=/dev/null, GIT_CONFIG_NOSYSTEM=1), no user or system attributes file
# (HOME is the empty temporary directory, GIT_ATTR_NOSYSTEM=1), and the author, the committer and
# their dates are fixed. Commits are written with `git commit-tree`, because a pod's `git commit`
# adds the session's trailers. So the same git writes the same bytes: two runs give files that
# `cmp` finds identical.
#
# merge-tree gets the option list `legion merge` gives it, in the same order (jjmerge's
# mergeTreeOptions): git's merge options at their documented defaults, an empty merge.default,
# which names no driver, core.attributesFile at /dev/null, and diff.algorithm at histogram.
# merge-ort uses histogram whatever a configuration says. git 2.47.3, the worker image's git,
# reads diff.algorithm and dies on a value it cannot parse, and a newer git may not (2.56.1
# merges, measured in CI). The entry keeps the merge from depending on that. So each file holds
# the merge that `legion merge` reads, and a configuration could change it only as it changes
# that merge.
set -euo pipefail

if [ $# -ne 1 ]; then
  echo "usage: capture.sh <dir>" >&2
  exit 2
fi
mkdir -p "$1"
out=$(cd "$1" && pwd)
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT

# g runs git in the current directory with no environment but this one.
g() {
  env -i PATH="$PATH" HOME="$scratch" LC_ALL=C \
    GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 GIT_ATTR_NOSYSTEM=1 \
    GIT_AUTHOR_NAME=capture GIT_AUTHOR_EMAIL=capture@example.com GIT_AUTHOR_DATE='1767225600 +0000' \
    GIT_COMMITTER_NAME=capture GIT_COMMITTER_EMAIL=capture@example.com GIT_COMMITTER_DATE='1767225600 +0000' \
    git "$@"
}

# repo <shape>: makes the repository of one shape and enters its work tree.
repo() {
  mkdir "$scratch/$1"
  cd "$scratch/$1"
  g -c init.defaultBranch=main init -q
}

# commit <branch> [<parent>]: commits the work tree, every file in it, to refs/heads/<branch>,
# with the branch's name as the message. HEAD stays on main, the base.
commit() {
  local tree id
  g add -A
  tree=$(g write-tree)
  if [ $# -eq 2 ]; then
    id=$(g commit-tree "$tree" -p "$2" -m "$1")
  else
    id=$(g commit-tree "$tree" -m "$1")
  fi
  g update-ref "refs/heads/$1" "$id"
}

# side <branch>: commits the work tree as one side, a child of main, then puts the work tree
# back to main for the other side.
side() {
  commit "$1" main
  g reset -q --hard
}

# capture <shape>: writes what merge-tree prints for the merge of a and b to <dir>/<shape>.z.
# Exit 1 is a merge with conflicts; any status but 0 and 1 is a failure.
capture() {
  local status=0
  g -c merge.renames=true -c merge.directoryRenames=conflict -c merge.renameLimit=7000 \
    -c merge.conflictStyle=merge -c merge.renormalize=false -c merge.default= \
    -c core.attributesFile=/dev/null -c diff.algorithm=histogram \
    merge-tree --write-tree -z a b >"$out/$1.z" || status=$?
  if [ "$status" -gt 1 ]; then
    echo "capture.sh: git merge-tree of $1 exited $status" >&2
    exit 1
  fi
}

# clean: each side changes a different file.
repo clean
printf 'one\n' >f1
printf 'two\n' >f2
commit main
printf 'one changed\n' >f1
side a
printf 'two changed\n' >f2
side b
capture clean

# content: both sides change the same line of f.
repo content
printf 'base\n' >f
commit main
printf 'ours\n' >f
side a
printf 'theirs\n' >f
side b
capture content

# modify-delete: a changes f, b deletes it, so git gives stages 1 and 2 only.
repo modify-delete
printf 'base\n' >f
commit main
printf 'ours\n' >f
side a
rm f
side b
capture modify-delete

# rename-rename: a renames f to g, b renames f to h: stage 1 at f, 2 at g, 3 at h.
repo rename-rename
printf 'one\ntwo\nthree\n' >f
commit main
mv f g
side a
mv f h
side b
capture rename-rename

# file-directory: a adds the file d, b adds d/x, so git moves a's file to d~a, at stage 2 alone.
repo file-directory
printf 'keep\n' >keep
commit main
printf 'file\n' >d
side a
mkdir d
printf 'in a directory\n' >d/x
side b
capture file-directory

# binary: both sides change a file that holds NUL bytes.
repo binary
printf 'bin\0ary\nbase\n' >bin
commit main
printf 'bin\0ary\nours\n' >bin
side a
printf 'bin\0ary\ntheirs\n' >bin
side b
capture binary

# odd-path: a content conflict at a path that starts with a dash and holds a space, a tab and a
# newline, which -z prints unquoted.
repo odd-path
odd=$'-odd path\twith tab\nand newline'
printf 'base\n' >"$odd"
commit main
printf 'ours\n' >"$odd"
side a
printf 'theirs\n' >"$odd"
side b
capture odd-path

echo "capture.sh: wrote 7 files with $(g version) into $out"
