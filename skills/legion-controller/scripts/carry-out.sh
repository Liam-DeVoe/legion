#!/bin/sh
# carry-out.sh: rebuilds the work a Legion issue pod's workspace holds and GitHub lacks into a git
# bundle, from the two tar archives that step 1 of docs/kubernetes.md, "Carrying unpushed work out
# of an issue's pod", copies out of the pod. It is that section's step 2 for a controller pod,
# which has no Docker: the rebuild runs in a user, network, mount and PID namespace of its own,
# under an empty environment, where the docs run a throwaway container. The controller's procedure
# is skills/legion-controller/references/operations.md, "A pod delete with nothing lost".
#
# Usage: carry-out.sh <KEY> <workspace path in the pod> <git.tar> <tree.tar> <out.bundle>
#
# Put the archives and the bundle under $TMPDIR: the namespace hides /run and $HOME. A refusal
# prints one "refused: ..." line and exits 1. When nothing below the workspace's working copy is
# unpushed, git refuses the empty bundle ("Refusing to create empty bundle") and the script exits
# with git's status.
#
# The script runs in three stages, which CARRY_OUT_STAGE names. Unset, it checks its arguments and
# runs itself again under unshare. In `namespace` it checks that it is isolated, mounts a tmpfs
# over /run and over $HOME, and runs itself again under `env -i`. In `rebuild` it checks that it
# is isolated again and runs the docs' step 2. A stage it does not start in its namespace refuses.
set -eu

usage='usage: carry-out.sh <KEY> <workspace path in the pod> <git.tar> <tree.tar> <out.bundle>'

refuse() {
  echo "refused: $*" >&2
  exit 1
}

# isolated refuses unless this shell is process 1 of a PID namespace of its own, the /proc it
# reads is that namespace's own, and its network namespace holds no interface but lo.
isolated() {
  [ "$$" -eq 1 ] ||
    refuse "this shell is process $$, not process 1 of its own PID namespace: run carry-out.sh with CARRY_OUT_STAGE unset"
  read -r self _ </proc/self/stat
  [ "$self" = 1 ] || refuse "/proc is not the namespace's own: it shows this shell as process $self"
  interfaces=$(sed -n '3,$s/^ *\([^:]*\):.*/\1/p' /proc/net/dev)
  [ "$interfaces" = lo ] ||
    refuse "the network namespace holds interfaces other than lo: $(printf '%s' "$interfaces" | tr '\n' ' ')"
}

case ${CARRY_OUT_STAGE-} in
'')
  [ $# -eq 5 ] || refuse "$usage"
  printf '%s\n' "$1" | grep -Eqx '[A-Z][A-Z0-9]*-[0-9]+' || refuse "$1 is not an issue key such as ACME-41"
  case $2 in /*) ;; *) refuse "$2 is not an absolute path" ;; esac
  for archive in "$3" "$4"; do
    [ -f "$archive" ] || refuse "$archive is not a file"
  done
  [ -d "$(dirname -- "$5")" ] || refuse "$(dirname -- "$5") is not a directory"
  script=$(readlink -f -- "$0")
  git=$(readlink -f -- "$3")
  tree=$(readlink -f -- "$4")
  out=$(readlink -f -- "$5")
  # The rebuild never runs without its namespace: where unshare is refused, nothing runs.
  if ! refusal=$(unshare --user --map-root-user --net --mount --pid --fork --mount-proc true 2>&1); then
    refuse "unshare refused the namespace the rebuild runs in, so nothing was rebuilt: $refusal"
  fi
  CARRY_OUT_STAGE=namespace
  export CARRY_OUT_STAGE
  exec unshare --user --map-root-user --net --mount --pid --fork --mount-proc \
    /bin/sh "$script" "$1" "$2" "$git" "$tree" "$out"
  ;;
namespace)
  isolated
  [ $# -eq 5 ] || refuse "$usage"
  case ${HOME-} in '' | /) refuse "HOME is '${HOME-}': the namespace mounts a tmpfs over it" ;; esac
  # -n: util-linux's mount would otherwise write its utab under the new /run.
  mount -n -t tmpfs -o mode=0700 carry-out-run /run || refuse "cannot mount a tmpfs over /run"
  mount -n -t tmpfs -o mode=0700 carry-out-home "$HOME" || refuse "cannot mount a tmpfs over $HOME"
  for hidden in /run /var/run/legion /var/run/secrets "$HOME"; do
    [ ! -e "$hidden" ] || [ -z "$(ls -A -- "$hidden")" ] ||
      refuse "$hidden still shows entries inside the namespace"
  done
  [ -f "$3" ] && [ -f "$4" ] && [ -d "$(dirname -- "$5")" ] ||
    refuse "the namespace hides $3, $4 or the directory of $5: put them under \$TMPDIR, outside /run and \$HOME"
  home=$(mktemp -d /run/home.XXXXXX)
  scratch=$(mktemp -d "$(dirname -- "$5")/carry-out.XXXXXX")
  exec /usr/bin/env -i PATH=/usr/bin:/bin HOME="$home" GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null \
    CARRY_OUT_STAGE=rebuild /bin/sh "$0" "$1" "$2" "$3" "$4" "$5" "$scratch"
  ;;
rebuild)
  isolated
  [ $# -eq 6 ] || refuse "$usage"
  ;;
*)
  refuse "CARRY_OUT_STAGE is '$CARRY_OUT_STAGE': run carry-out.sh with it unset"
  ;;
esac

# The docs' step 2, from here on in a fresh directory the namespace stage made.
K=$1 ws=$2 git=$3 tree=$4 out=$5 scratch=$6
trap 'cd / && rm -rf -- "$scratch"' EXIT
cd "$scratch"

# The entry-type checks on both archives, read from listings kept in files so that an archive tar
# cannot read is a refusal. `--quoting-style=c` puts each entry on one line and quotes its name and
# a symlink's target, and `--numeric-owner` keeps the archive's owner names, which hold any text,
# out of the listing, so the first quoted string of a line is its entry's name.
tar --quoting-style=c --numeric-owner -tvf "$git" >git.list || refuse "tar cannot list $git"
tar --quoting-style=c --numeric-owner -tvf "$tree" >tree.list || refuse "tar cannot list $tree"
bad=$(awk '$1 !~ /^[-d]/' git.list)
test -z "$bad" || refuse "$git holds $bad"
bad=$(awk '$1 !~ /^[-dl]/' tree.list)
test -z "$bad" || refuse "$tree holds $bad"

# The tree is unpacked into work/, beside work/.git. Step 1's --exclude flags keep .git and .jj out
# of an honest archive; an entry through either would plant a configuration into the rebuild's own
# repository, so the script refuses one at any depth, in any case, whatever its type.
sed -nE 's/^[^"]*"(([^"\\]|\\.)*)".*/\1/p' tree.list >names
[ "$(wc -l <names)" -eq "$(wc -l <tree.list)" ] || refuse "cannot read the name of each entry $tree holds"
bad=$(grep -iE '(^|/)\.(git|jj)(/|$)' names || true)
test -z "$bad" || refuse "$tree holds an entry through .git or .jj: $bad"

# The tree is unpacked in the controller's own pod, not in a throwaway container, so its symlinks
# are held to two rules. First, no entry may run through a symlink of the archive: a tar of a real
# directory never writes one under a link, and tar would write it where the link points rather
# than where its name says. Second, a relative target may not lead above the tree's root: it is
# walked one component at a time from the link's own directory, `.` dropped and `..` taking one
# away, and where the path so far is a symlink entry of the archive, the walk goes on from that
# entry's own target, from that entry's directory, so a link through `d -> .` that ends in `..` is
# caught; a walk that loops, or that passes through more than 40 links, is refused too. An in-tree
# link that climbs and comes back down, as every Bun workspace's node_modules holds, passes, and so
# does one whose target runs through another in-tree link, as node_modules/.bin's do.
# An absolute target, the link's own or one met on the walk, ends the walk and is no refusal, as a
# virtualenv's `bin/python -> /usr/bin/python3` is not: the first rule keeps every later entry from
# being written through any link, tar replaces a name it meets again rather than write through it,
# and git add stores a link as a link and does not follow it, so such a target carries no write out
# of the tree. The tree archive holds ignored files too, node_modules and a .venv among them, since
# step 1 leaves out only .git, .jj and .codegraph: they are checked here, and the repository's
# .gitignore keeps them out of the rebuilt commit.
# Names are compared with tar's `./` and every `.` or empty component left out, and in the C
# quoting of the listing, which writes `/` and `.` as they are. awk prints one line per finding,
# its kind first; a line it cannot read is a finding too.
awk '
function readq(s, i,   j, c) {
  for (j = i + 1; j <= length(s); j++) {
    c = substr(s, j, 1)
    if (c == "\\") { j++; continue }
    if (c == "\"") { qend = j; return substr(s, i + 1, j - i - 1) }
  }
  qend = 0
  return ""
}
function canon(p,   n, part, k, out) {
  n = split(p, part, "/")
  out = ""
  for (k = 1; k <= n; k++)
    if (part[k] != "" && part[k] != ".") out = (out == "" ? part[k] : out "/" part[k])
  return out
}
function walk(p, t,   st, depth, rem, i, c, cur, k, hops, seen) {
  depth = split(p, st, "/") - 1
  if (substr(t, 1, 1) == "/") return ""
  rem = t
  hops = 0
  while (rem != "") {
    i = index(rem, "/")
    if (i == 0) { c = rem; rem = "" } else { c = substr(rem, 1, i - 1); rem = substr(rem, i + 1) }
    if (c == "" || c == ".") continue
    if (c == "..") { if (depth == 0) return "leaves"; depth--; continue }
    st[++depth] = c
    cur = st[1]
    for (k = 2; k <= depth; k++) cur = cur "/" st[k]
    if (!(cur in linkto)) continue
    if ((cur SUBSEP rem) in seen) return "loops"
    seen[cur SUBSEP rem] = 1
    if (++hops > 40) return "hops"
    if (substr(linkto[cur], 1, 1) == "/") return ""
    depth--
    rem = (rem == "" ? linkto[cur] : linkto[cur] "/" rem)
  }
  return ""
}
{
  q = index($0, "\"")
  if (q == 0) { print "unread line " NR; next }
  name = readq($0, q)
  if (qend == 0) { print "unread line " NR; next }
  path[NR] = canon(name)
  if ($1 !~ /^l/) next
  rest = substr($0, qend + 1)
  if (substr(rest, 1, 5) != " -> \"") { print "unread line " NR; next }
  target[NR] = readq(rest, 5)
  if (qend == 0) { print "unread line " NR; next }
  linkto[path[NR]] = target[NR]
}
END {
  for (r = 1; r <= NR; r++) {
    if (!(r in path)) continue
    n = split(path[r], part, "/")
    prefix = ""
    for (k = 1; k < n; k++) {
      prefix = (k == 1 ? part[1] : prefix "/" part[k])
      if (prefix in linkto) { print "through " path[r] " under " prefix; break }
    }
    if (!(r in target)) continue
    found = walk(path[r], target[r])
    if (found != "") print found " " path[r] " -> " target[r]
  }
}' tree.list >findings
bad=$(sed -n 's/^unread //p' findings)
test -z "$bad" || refuse "cannot read the name of each entry and the target of each symlink $tree holds: $bad"
bad=$(sed -n 's/^leaves //p' findings)
test -z "$bad" || refuse "$tree holds a symlink whose target leaves the tree: $bad"
bad=$(sed -n 's/^loops //p' findings)
test -z "$bad" || refuse "$tree holds a symlink whose target loops: $bad"
bad=$(sed -n 's/^hops //p' findings)
test -z "$bad" || refuse "$tree holds a symlink whose target passes through more than 40 links: $bad"
bad=$(sed -n 's/^through //p' findings)
test -z "$bad" || refuse "$tree holds an entry through a symlink of the tree: $bad"

# tar runs as the namespace's uid 0, which restores each entry's owner and mode by default; the
# owner, uid 1000 of the pod, is not mapped here, so it takes neither, as the docs' non-root
# container did.
git init -q --bare pod.git
# shellcheck disable=SC2046 # packed-refs is a member name or nothing, as in the docs
tar -C pod.git --no-same-owner --no-same-permissions --exclude=objects/info -xf "$git" objects refs \
  $(tar -tf "$git" | grep -x packed-refs || true)
id=
for g in $(tar -tf "$git" | grep '^worktrees/[^/]*/gitdir$'); do
  case $(tar -xOf "$git" "$g") in "$ws/.git" | */"${ws#/legion/}/.git") id=${g%/gitdir} ;; esac
done
test -n "$id" || refuse "no worktree record names $ws"
base=$(tar -xOf "$git" "$id/HEAD")
echo "$base" | grep -Eqx '[0-9a-f]{40}' || refuse "$id/HEAD is not a commit id"
git -C pod.git update-ref refs/rescue-base "$base"
git init -q work && cd work
git fetch -q ../pod.git 'refs/jj/*:refs/jj/*' 'refs/heads/*:refs/rescue/*' \
  'refs/remotes/origin/*:refs/remotes/origin/*' refs/rescue-base:refs/rescue-base
git symbolic-ref HEAD "refs/heads/rescue-$K" && git reset -q "$base"
tar --no-same-owner --no-same-permissions -xf "$tree"
git -c core.excludesFile=/dev/null add -A
git status --short
git diff --cached --quiet ||
  git -c user.name=rescue -c user.email=rescue@invalid commit -q -m "Edits left in $K's workspace"
echo "unpushed commits of other branches, not in the bundle:"
git log --oneline --glob=refs/rescue/ --not --remotes=origin "rescue-$K"
git bundle create "$out" "rescue-$K" --not --remotes=origin
