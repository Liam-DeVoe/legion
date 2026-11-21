#!/usr/bin/env bash
# red-at-base.sh - Runs each test a change adds at an older commit, the base, and says whether the
# test fails there. A new test proves its change only when it fails without the change.
#
# usage: bash scripts/red-at-base.sh --base <commit> --list <file>
#
# How it works. The tool reads the whole list first, and refuses it (exit 2, no test run) when a
# line is malformed or names a path the checkout does not have. Then, for each line, it makes a git
# worktree of the base in a directory of its own (mktemp -d under TMPDIR), copies the line's files
# into it from the checkout it runs in, runs the line's test there, prints one line that says how
# the test ended at the base, and removes the worktree before the next line, so that each line runs
# on the base and its own files alone. The verdicts:
#
#   RED           the test ran and failed
#   BUILD-FAILED  the line's files do not build at the base, such as a test that calls code the base
#                 does not have
#   GREEN         the test ran and passed at the base, so it does not tell the base from the change
#   NO-TEST       no test the line names ran to a pass or a fail: no test matched, each match
#                 skipped, the run failed before a test started, or the base does not build there
#                 without the line's files, so that a failure would prove nothing about the test
#
# RED and BUILD-FAILED count as red. The tool exits 0 when every line is red, 1 when a line is not,
# and 2 when it refuses its arguments or the list, or cannot make a worktree.
#
# The list holds one test on each line, its fields separated by blanks (a regex writes a blank as
# \s):
#
#   <kind> <dir> <test regex or case> <file>...
#
# Blank lines, and lines whose first field starts with #, are passed over; a list with no other line
# is refused, since a run over nothing proves nothing. Each path is relative to the top of the
# checkout. Each <file> is copied as the checkout's working tree holds it, committed or not, over the
# base's copy at the same path; a line names at least one.
#
#   go   <dir> is the package directory. Before it copies the files, the tool builds the package's
#        test binary at the base (go test -c), when the base has Go files there, so that a build
#        that fails after the copy is the failure of the line's files. The run is
#          go -C <dir> test -count=1 -v -run <regex> .
#        A package of the run that reports [build failed] or [setup failed] makes it BUILD-FAILED.
#        Otherwise only the tests at the depth of the regex count (^TestA$/^b$ names a subtest, so
#        a TestA that fails before its subtest starts counts for nothing): a --- FAIL of one is RED,
#        and so is a failed run that ended inside one (a panic, a timeout, an os.Exit); a --- PASS
#        with no fail is GREEN; skips alone, or no tests to run, are NO-TEST.
#   bun  <dir> is the directory bun test runs in. The run is
#          bun test --no-install ./<test file>... -t <regex>
#        over the line's files under <dir> whose names bun reads as tests (a.test.ts, a_spec.js and
#        the like); a bun line names at least one. A failed test is RED. An error outside any test,
#        which at the base is most often a file that does not load (it imports a name the base does
#        not export), is BUILD-FAILED; an import of a package that is not installed is NO-TEST. A
#        pass is GREEN; skips or todos alone, and "matched 0 tests", are NO-TEST.
#   e2e  <dir> is the e2e script, such as scripts/e2e/check-states.sh, and the third field is one
#        case, or several with commas between them. The run, at the top of the worktree, is
#          <NAME>_E2E_CASES=<cases> bash <script>
#        where NAME is the script's file name without .sh, in capitals, with each - as _:
#        scripts/e2e/check-states.sh reads CHECK_STATES_E2E_CASES. A case has started when the
#        script prints the line "== <case>". A run that started a case of the line and exits other
#        than 0 is RED; one that exits 0 and started each case of the line is GREEN; one that
#        started none is NO-TEST. An e2e line reads no BUILD-FAILED.
#
# What a run reads. Each run gets the tool's own environment, so a database URL, GOCACHE, GOTMPDIR
# and GOFLAGS reach the test as the caller set them. The tool needs no network: go runs with
# GOPROXY=off, so a module the module cache does not hold fails the run (NO-TEST) instead of a
# download, and bun runs with --no-install. GOWORK is unset, so go reads the base's own go.work. A
# worktree of the base holds no installed packages (git ignores node_modules), so the tool mirrors
# each node_modules directory of the checkout into the worktree. A symlink is copied as the link it
# is: a relative link to a workspace package of the repository resolves to the base's copy of that
# package, and a link into an installed package resolves to the checkout's install. A scope
# directory (@name) and .bin are mirrored entry by entry. Any other directory, such as bun's store
# node_modules/.bun or a package a hoisted install put there, becomes a link to the checkout's; a
# dot directory other than .bin and .bun, a cache that a tool writes, is left out.
#
# What it writes. Nothing in the checkout's working tree: the worktree and each file of a run are
# in the tool's own directory under TMPDIR, which must not be inside the checkout. git worktree add
# writes the worktree's entry in the repository's git directory. After each line, and at any exit,
# the tool removes the worktree with git worktree remove --force, and then the entry by its own
# path once it has read that the entry still names this worktree. It does not run git worktree
# prune, which has no form for one entry and would also drop the entries of other checkouts whose
# directories are away. A trap does the removal at each exit; on INT or TERM it waits for the
# running test to end first.
#
# stdout holds one line for each line of the list, as that line ends:
#   <VERDICT> line <n>: <kind> <dir> <test> -- <what decided it>
# where <n> is the line's number in the list file. stderr holds each run's own output, and a count
# at the end.
set -euo pipefail

usage() {
  cat <<'EOF'
usage: bash scripts/red-at-base.sh --base <commit> --list <file>

Runs each test of the list at the base commit, and prints RED, BUILD-FAILED, GREEN or NO-TEST for
each line. A line is <kind> <dir> <test regex or case> <file>..., its kind one of go, bun and e2e.
The header of this script says how each kind runs and how each verdict is read.
exit: 0 every line is red (RED or BUILD-FAILED), 1 a line is not, 2 a refused argument or list
EOF
}

refuse() {
  echo "red-at-base.sh: $*" >&2
  exit 2
}

base=''
list=''
while (($# > 0)); do
  case $1 in
  --base | --list)
    (($# >= 2)) || {
      usage >&2
      refuse "$1 needs a value"
    }
    if [[ $1 == --base ]]; then base=$2; else list=$2; fi
    shift 2
    ;;
  -h | --help)
    usage
    exit 0
    ;;
  *)
    usage >&2
    refuse "$1 is no argument of this tool"
    ;;
  esac
done
[[ -n $base ]] || {
  usage >&2
  refuse 'no --base: name the commit to run the tests at'
}
[[ -n $list ]] || {
  usage >&2
  refuse 'no --list: name the file of the tests to run'
}
[[ -f $list && -r $list ]] || refuse "the list $list is no readable file"
[[ $base != -* ]] || refuse "--base $base names no commit"

repo="$(git rev-parse --show-toplevel 2>/dev/null)" || refuse 'the current directory is in no git checkout'
readonly repo
base_commit="$(git -C "$repo" rev-parse --verify --quiet "${base}^{commit}")" ||
  refuse "--base $base names no commit of this checkout"
readonly base_commit

# safe_path <line number> <path>: a path relative to the top of the checkout that stays in it.
safe_path() {
  [[ -n $2 && $2 != /* && /$2/ != */../* ]] || refuse "line $1 of $list: $2 is no path inside the checkout"
}

# ---- the list, read and checked whole before any test runs --------------------------------------
line_numbers=()
kinds=()
dirs=()
tests=()
line_files=()
bun_tests=()
number=0
while IFS= read -r raw || [[ -n $raw ]]; do
  number=$((number + 1))
  read -r -a field <<<"$raw"
  ((${#field[@]} > 0)) || continue
  [[ ${field[0]} != \#* ]] || continue
  ((${#field[@]} >= 4)) ||
    refuse "line $number of $list has ${#field[@]} fields; a line is <kind> <dir> <test regex or case> <file>..."
  kind=${field[0]}
  dir=${field[1]%/}
  test=${field[2]}
  files=("${field[@]:3}")
  safe_path "$number" "$dir"
  for path in "${files[@]}"; do
    safe_path "$number" "$path"
    [[ -f ${repo}/${path} ]] || refuse "line $number of $list: $path is no file of the checkout"
  done
  case $kind in
  go | bun)
    [[ -d ${repo}/${dir} ]] || refuse "line $number of $list: $dir is no directory of the checkout"
    ;;
  e2e)
    [[ $dir == *.sh && -f ${repo}/${dir} ]] ||
      refuse "line $number of $list: $dir is no e2e script (a .sh file) of the checkout"
    [[ $test =~ ^[A-Za-z0-9._-]+(,[A-Za-z0-9._-]+)*$ ]] ||
      refuse "line $number of $list: $test is no case name, nor several with commas between them"
    ;;
  *)
    refuse "line $number of $list: $kind is no kind; a kind is go, bun or e2e"
    ;;
  esac
  bun_files=''
  if [[ $kind == bun ]]; then
    for path in "${files[@]}"; do
      relative=$path
      if [[ $dir != . ]]; then
        [[ $path == "$dir"/* ]] || continue
        relative=${path#"$dir"/}
      fi
      case ${relative##*/} in
      *.test.* | *_test.* | *.spec.* | *_spec.*)
        case ${relative##*.} in
        ts | tsx | js | jsx | mjs | cjs | mts | cts) bun_files+=" ./${relative}" ;;
        esac
        ;;
      esac
    done
    [[ -n $bun_files ]] ||
      refuse "line $number of $list: no file of the line under $dir is a test file bun runs (such as a.test.ts)"
  fi
  line_numbers+=("$number")
  kinds+=("$kind")
  dirs+=("$dir")
  tests+=("$test")
  line_files+=("${files[*]}")
  bun_tests+=("${bun_files# }")
done <"$list"
((${#kinds[@]} > 0)) || refuse "the list $list holds no line to run, and a run over nothing proves nothing"

# ---- the tool's own directory, and its removal at every exit -------------------------------------
tmp="$(mktemp -d "${TMPDIR:-/tmp}/red-at-base.XXXXXX")"
readonly tmp
case "$(realpath "$tmp")/" in
"$(realpath "$repo")"/*)
  rmdir -- "$tmp"
  refuse "TMPDIR (${TMPDIR:-/tmp}) is inside the checkout, and the tool writes nothing there"
  ;;
esac
readonly wt="${tmp}/base"
readonly log="${tmp}/run.log"
# The worktree's entry in the repository's git directory, and what its gitdir file held when the
# tool made it: the entry is removed by this path, and only while it still names this worktree.
admin=''
admin_gitdir=''
wt_real=''

drop_worktree() {
  if [[ -e $wt ]]; then
    git -C "$repo" worktree remove --force "$wt" >/dev/null 2>&1 || true
  fi
  if [[ -e $wt ]]; then
    chmod -R u+w -- "$wt" 2>/dev/null || true
    rm -rf -- "$wt"
  fi
  if [[ -n $admin && -f ${admin}/gitdir && "$(<"${admin}/gitdir")" == "$admin_gitdir" ]]; then
    rm -rf -- "$admin"
    # git removes the directory of the entries when its last entry goes; rmdir removes it only empty.
    rmdir -- "$(dirname -- "$admin")" 2>/dev/null || true
  fi
  admin=''
}

# shellcheck disable=SC2329 # the EXIT trap calls it
cleanup() {
  drop_worktree
  rm -rf -- "$tmp"
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

make_worktree() {
  git -C "$repo" worktree add --detach --quiet "$wt" "$base_commit" >&2 ||
    refuse "git worktree add of $base_commit at $wt failed"
  admin="$(git -C "$wt" rev-parse --absolute-git-dir)"
  admin_gitdir="$(<"${admin}/gitdir")"
  wt_real="$(realpath "$wt")"
}

# inside <path>: true when the path, each symlink of it that exists resolved, is in the worktree.
inside() {
  local resolved
  resolved="$(realpath -m -- "$1")"
  [[ $resolved == "$wt_real" || $resolved == "$wt_real"/* ]]
}

# copy_files <line number> <path>...: each file of the line, from the checkout over the base's copy.
copy_files() {
  local number=$1 path
  shift
  for path in "$@"; do
    inside "${wt}/${path}" ||
      refuse "line $number of $list: $path leads out of the worktree of the base, through a symlink of the base"
    mkdir -p -- "$(dirname -- "${wt}/${path}")"
    cp -p -- "${repo}/${path}" "${wt}/${path}"
    echo "   copied $path" >&2
  done
}

# mirror_dir <from> <to>: one node_modules directory of the checkout, as the header's "What a run
# reads" says.
mirror_dir() {
  local from=$1 to=$2 entry name
  mkdir -- "$to"
  for entry in "$from"/* "$from"/.[!.]* "$from"/..?*; do
    [[ -e $entry || -L $entry ]] || continue
    name=${entry##*/}
    if [[ -L $entry ]]; then
      cp -P -- "$entry" "${to}/${name}"
    elif [[ -d $entry && ($name == @* || $name == .bin) ]]; then
      mirror_dir "$entry" "${to}/${name}"
    elif [[ -d $entry && ($name != .* || $name == .bun) ]]; then
      ln -s -- "$entry" "${to}/${name}"
    elif [[ -f $entry ]]; then
      cp -p -- "$entry" "${to}/${name}"
    fi
  done
}

# mirror_installed_packages: the node_modules directory beside each package.json the checkout
# tracks, wherever the base has that directory and no node_modules of its own there.
mirror_installed_packages() {
  local manifest directory
  while IFS= read -r -d '' manifest; do
    directory="$(dirname -- "$manifest")"
    [[ -d ${repo}/${directory}/node_modules && -d ${wt}/${directory} ]] || continue
    [[ ! -e ${wt}/${directory}/node_modules && ! -L ${wt}/${directory}/node_modules ]] || continue
    inside "${wt}/${directory}/node_modules" || continue
    mirror_dir "${repo}/${directory}/node_modules" "${wt}/${directory}/node_modules"
  done < <(git -C "$repo" ls-files -z -- package.json '*/package.json')
}

status=0
# run_logged <directory> <command>...: the command, in the directory and with no input, its output
# in the run's log, which then goes to stderr; its exit status in status. The output goes to a file
# and not through a pipe, so that a process a test leaves behind cannot hold the tool.
run_logged() {
  local directory=$1
  shift
  {
    printf '   run in %s:' "$directory"
    printf ' %q' "$@"
    echo
  } >&2
  status=0
  (cd -- "$directory" && exec "$@") </dev/null >"$log" 2>&1 || status=$?
  cat -- "$log" >&2
  echo "   exit $status" >&2
}

# first_error: the run's first compiler error line, or else its first line.
first_error() {
  awk '/^[^ \t#].*:[0-9]+:[0-9]+: / { print; found = 1; exit }
    first == "" && NF { first = $0 }
    END { if (!found) print first }' "$log"
}

# go_verdict <regex> <exit status>: "<VERDICT><tab><what decided it>" for a go test -v run.
go_verdict() {
  RUN_REGEX=$1 RUN_STATUS=$2 awk '
    function depth_of(name) { return gsub(/\//, "/", name) + 1 }
    # The depth of the tests a go test -run regex names: go splits the regex at each / outside []
    # and (), and at each | there into alternatives, and the shallowest alternative counts.
    function regex_depth(re,   i, c, brackets, parens, depth, least) {
      brackets = 0; parens = 0; depth = 1; least = 0
      for (i = 1; i <= length(re); i++) {
        c = substr(re, i, 1)
        if (c == "\\") { i++ }
        else if (c == "[") { brackets++ }
        else if (c == "]") { if (--brackets < 0) brackets = 0 }
        else if (brackets == 0 && c == "(") { parens++ }
        else if (brackets == 0 && c == ")") { parens-- }
        else if (brackets == 0 && parens == 0 && c == "/") { depth++ }
        else if (brackets == 0 && parens == 0 && c == "|") {
          if (least == 0 || depth < least) least = depth
          depth = 1
        }
      }
      if (least == 0 || depth < least) least = depth
      return least
    }
    BEGIN { want = regex_depth(ENVIRON["RUN_REGEX"]); status = ENVIRON["RUN_STATUS"] + 0 }
    first == "" && NF { first = $0 }
    /^FAIL\t.* \[(build|setup) failed\]$/ { unbuilt = 1 }
    /GOPROXY=off|toolchain not available/ && offline == "" { offline = $0 }
    compile == "" && /^[^ \t#].*:[0-9]+:[0-9]+: / { compile = $0 }
    panic == "" && /^panic: / { panic = $0 }
    /^ *=== RUN / {
      if (depth_of($3) >= want && !($3 in started)) { started[$3] = 1; order[++runs] = $3 }
    }
    /^ *--- (PASS|FAIL|SKIP): / {
      if ($2 == "FAIL:" && shallower == "") { shallower = $0; sub(/^ +/, "", shallower) }
      if (depth_of($3) < want) next
      ended[$3] = 1
      line = $0
      sub(/^ +/, "", line)
      if ($2 == "FAIL:") { fails++; if (fail == "") fail = line }
      else if ($2 == "PASS:") { passes++; if (pass == "") pass = line }
      else { skips++; if (skip == "") skip = line }
    }
    END {
      if (unbuilt && offline != "") {
        print "NO-TEST\tthe build needs what the module cache does not hold, and the tool reads no network: " offline
        exit
      }
      if (unbuilt) { print "BUILD-FAILED\t" (compile != "" ? compile : "the test build failed"); exit }
      if (fails) { print "RED\t" fail; exit }
      if (status != 0) {
        for (i = 1; i <= runs; i++)
          if (!(order[i] in ended)) {
            print "RED\t" order[i] " started and did not end: " (panic != "" ? panic : "exit " status)
            exit
          }
        if (passes) { print "GREEN\t" pass ", and then the run failed outside the tests (exit " status ")"; exit }
        print "NO-TEST\tthe run failed (exit " status ") before a test the regex names started: " (shallower != "" ? shallower : first)
        exit
      }
      if (passes) { print "GREEN\t" pass; exit }
      if (skips) { print "NO-TEST\tevery test the regex names skipped: " skip; exit }
      print "NO-TEST\tno tests to run"
    }' "$log"
}

# bun_verdict <exit status>: "<VERDICT><tab><what decided it>" for a bun test run, read from bun's
# summary: a failed test counts once in "fail", and an error outside any test once in "fail" and
# once in "error".
bun_verdict() {
  RUN_STATUS=$1 awk '
    BEGIN { status = ENVIRON["RUN_STATUS"] + 0 }
    NF == 2 && $1 ~ /^[0-9]+$/ && $2 ~ /^(pass|fail|skip|todo|error|errors)$/ {
      count[$2 == "errors" ? "error" : $2] = $1 + 0
    }
    /matched 0 tests|had no matches/ && nomatch == "" { nomatch = $0 }
    /Cannot find package/ && missing == "" { missing = $0 }
    /^\(fail\) / && fail == "" { fail = $0 }
    /^(error|[A-Za-z]*Error): / && error == "" { error = $0 }
    first == "" && NF && !/^bun test v/ { first = $0 }
    END {
      if (nomatch != "") { print "NO-TEST\t" nomatch; exit }
      if (missing != "") { print "NO-TEST\ta package the test imports is not installed for the base: " missing; exit }
      if (count["fail"] - count["error"] > 0) { print "RED\t" (fail != "" ? fail : count["fail"] " fail"); exit }
      if (count["error"] > 0) { print "BUILD-FAILED\t" (error != "" ? error : "an error outside any test"); exit }
      if (status != 0) { print "NO-TEST\tbun test failed with no test result (exit " status "): " (error != "" ? error : first); exit }
      if (count["pass"] > 0) { print "GREEN\t" count["pass"] " pass"; exit }
      print "NO-TEST\tno test the regex names ran to a pass or a fail (" count["skip"] + 0 " skip, " count["todo"] + 0 " todo)"
    }' "$log"
}

# e2e_verdict <cases> <exit status>: "<VERDICT><tab><what decided it>" for a run of an e2e script.
e2e_verdict() {
  RUN_CASES=$1 RUN_STATUS=$2 awk '
    BEGIN { n = split(ENVIRON["RUN_CASES"], want, ","); status = ENVIRON["RUN_STATUS"] + 0 }
    {
      for (i = 1; i <= n; i++)
        if (!(i in began) && ($0 == "== " want[i] || index($0, "== " want[i] " ") == 1)) { began[i] = 1; last = want[i] }
    }
    /^FAIL / && fail == "" { fail = $0 }
    END {
      for (i = 1; i <= n; i++)
        if (i in began) started++
        else absent = absent (absent == "" ? "" : ",") want[i]
      if (!started) {
        print "NO-TEST\tno line == <case> for " ENVIRON["RUN_CASES"] ": the script started no case of the line (exit " status ")"
        exit
      }
      if (status != 0) { print "RED\t" (fail != "" ? fail : "exit " status " after == " last); exit }
      if (absent != "") { print "NO-TEST\tthe script exited 0 and never started " absent; exit }
      print "GREEN\tevery case of the line passed: " ENVIRON["RUN_CASES"]
    }' "$log"
}

# ---- each line, at the base ---------------------------------------------------------------------------
echo "red-at-base.sh: ${#kinds[@]} lines of $list, at the base $base ($base_commit), from the checkout $repo" >&2
red=0
build_failed=0
green=0
no_test=0
for i in "${!kinds[@]}"; do
  number=${line_numbers[i]}
  kind=${kinds[i]}
  dir=${dirs[i]}
  test=${tests[i]}
  read -r -a files <<<"${line_files[i]}"
  {
    echo
    echo "== line $number: $kind $dir $test ${line_files[i]}"
  } >&2
  make_worktree
  result=''
  if [[ $kind == go ]] && compgen -G "${wt}/${dir}/*.go" >/dev/null; then
    run_logged "$wt" env -u GOWORK GOPROXY=off go -C "${wt}/${dir}" test -c -o /dev/null .
    if ((status != 0)); then
      result="NO-TEST"$'\t'"the base does not build in $dir without the line's files, so no failure there would be the test's: $(first_error)"
    fi
  fi
  if [[ -z $result ]]; then
    copy_files "$number" "${files[@]}"
    mirror_installed_packages
    case $kind in
    go)
      run_logged "$wt" env -u GOWORK GOPROXY=off go -C "${wt}/${dir}" test -count=1 -v -run "$test" .
      result="$(go_verdict "$test" "$status")"
      ;;
    bun)
      read -r -a bun_files <<<"${bun_tests[i]}"
      run_logged "${wt}/${dir}" bun test --no-install "${bun_files[@]}" -t "$test"
      result="$(bun_verdict "$status")"
      ;;
    e2e)
      name=${dir##*/}
      name=${name%.sh}
      name=${name^^}
      run_logged "$wt" env "${name//[^A-Z0-9]/_}_E2E_CASES=${test}" bash "$dir"
      result="$(e2e_verdict "$test" "$status")"
      ;;
    esac
  fi
  drop_worktree
  [[ -n $result ]] || result="NO-TEST"$'\t'"the tool could not read the run's log"
  verdict=${result%%$'\t'*}
  case $verdict in
  RED) red=$((red + 1)) ;;
  BUILD-FAILED) build_failed=$((build_failed + 1)) ;;
  GREEN) green=$((green + 1)) ;;
  *) no_test=$((no_test + 1)) ;;
  esac
  printf '%s line %s: %s %s %s -- %s\n' "$verdict" "$number" "$kind" "$dir" "$test" "${result#*$'\t'}"
done

{
  echo
  echo "red-at-base.sh: $((red + build_failed)) of ${#kinds[@]} lines are red at $base_commit" \
    "(RED $red, BUILD-FAILED $build_failed, GREEN $green, NO-TEST $no_test)"
} >&2
if ((red + build_failed == ${#kinds[@]})); then
  exit 0
fi
exit 1
