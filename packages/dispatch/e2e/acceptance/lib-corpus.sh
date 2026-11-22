# shellcheck shell=bash
# The one way the acceptance drivers of dispatch://INFRA-1098 that read `bun run dispatch:local` make
# sure its database holds the seeded corpus of the graph model: the corpus the loader
# packages/envoy/cmd/dispatch-corpus writes (internal/dispatch/seedcorpus, whose package header
# records what it seeds, under which names and keys, and which parts this head leaves out). Sourced,
# never run, after lib.sh. No driver writes the corpus any other way.
#
# corpus_ensure SECTION runs `dispatch-corpus check` over the database DATABASE_URL names, for
# the project PROJECT names. Where the check finds no seeded issue in that project, the corpus is
# absent, and it runs `dispatch-corpus seed --test-copy` once: the database is a test copy, which
# `bun run dispatch:local` truncated and seeded with its own rows, and the seed's own self-check
# runs after it. Where the check finds the corpus and a read of it fails, the corpus is broken - a
# driver left it changed, or a seed stopped half way - and the run fails, printing the loader's
# lines, since a second seed of a project that holds the corpus is refused.
#
# corpus_claim SECTION WHAT ARGS… runs one of the loader's two claim commands, `end-claim --role
# <role>` or `restore-claim --role <role> --session <session>`, which end a role's claim and put it
# back through the store's writer: the outbox states a role's claim in a deployment, and
# `bun run dispatch:local` runs no bus and so no outbox.
#
# The loader's executable is the one e2e/corpus.ts runs: dispatch-corpus in the directory a caller
# hands its prebuilt binaries over in (DISPATCH_E2E_BROKER_BIN_DIR, refused with exit 2 where it
# holds none), and otherwise one built from this checkout into packages/envoy/out, which is
# ignored, under the lock e2e/corpus.ts takes, so the browser file and the drivers build it one at
# a time and never seed with a stale loader.
#
# The inputs are the caller's own variables, which each function reads when it runs, not when this
# file is read; lib.sh names the first two:
#   work          the run's directory (lib.sh's work_dir); the loader's output is kept here
#   database_url  the database the server at DISPATCH_URL serves, which the loader writes and reads
#   project       the project key (the check's PROJECT), which the corpus is written into
# What it leaves its caller: corpus_issue, the key of the seeded issue, which the self-check's last
# line names whether this run seeded it or found it; corpus_seeded, 1 where this run seeded the
# corpus and 0 where it found it; and $work/corpus.out, the loader's lines, which the caller may
# print. The rest of the corpus a driver reads from the store and the API by that key, since only a
# seed prints the names of what it wrote.
#
# A refused input exits 2 on stderr, as lib.sh's require_env does; a failed check prints lib.sh's
# `FAIL: <section> <what>: <observed>` and exits 1. Every check is `cmd || fail …`, never part of an
# `&&` list: under `set -e` a failing command anywhere but last in such a list does not stop the
# script.
: "${work:=}" "${database_url:=}" "${project:=}"

corpus_issue=""
corpus_seeded=0

# corpus_tools refuses, with exit 2, a run that can neither take the loader handed in nor build
# it: with no DISPATCH_E2E_BROKER_BIN_DIR the loader is built here, which needs go and flock. A
# driver calls it with its other input checks, before it writes anything.
corpus_tools() {
  [[ -n "${DISPATCH_E2E_BROKER_BIN_DIR:-}" ]] || require_commands go flock
}

# corpus_bin sets $corpus_binary to the loader's executable: the handed-in one, or one built from
# this checkout under the lock e2e/corpus.ts takes.
corpus_bin() {
  local section="$1" directory envoy
  if [[ -n "${DISPATCH_E2E_BROKER_BIN_DIR:-}" ]]; then
    corpus_binary="$DISPATCH_E2E_BROKER_BIN_DIR/dispatch-corpus"
    [[ -x "$corpus_binary" ]] || {
      printf 'DISPATCH_E2E_BROKER_BIN_DIR=%s holds no executable dispatch-corpus\n' "$DISPATCH_E2E_BROKER_BIN_DIR" >&2
      exit 2
    }
    return
  fi
  envoy="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../../envoy" && pwd)"
  directory="$envoy/out/dispatch-e2e-corpus"
  corpus_binary="$directory/dispatch-corpus"
  mkdir -p "$directory"
  (cd -- "$envoy" && flock "$directory/.build.lock" go build -o "$corpus_binary" ./cmd/dispatch-corpus) \
    >"$work/corpus-build.out" 2>&1 ||
    fail "$section" "build dispatch-corpus from this checkout" "go build exited $?: $(tail -c 1500 "$work/corpus-build.out")"
}

# corpus_run ARGS…: the loader with ARGS over $database_url, run from packages/envoy as
# e2e/corpus.ts runs it, its output in $work/corpus.out and its errors in $work/corpus.err; sets
# corpus_code to its exit status. psql's connection overrides do not reach it, as lib.sh's query
# keeps them from psql.
corpus_run() {
  local envoy fixtures
  envoy="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../../envoy" && pwd)"
  fixtures="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../fixtures" && pwd)"
  corpus_code=0
  (cd -- "$envoy" && env -u PGHOSTADDR -u PGSERVICE -u PGSERVICEFILE DATABASE_URL="$database_url" \
    "$corpus_binary" "$@" --project "$project" --fixtures "$fixtures") >"$work/corpus.out" 2>"$work/corpus.err" ||
    corpus_code=$?
}

# corpus_ensure SECTION: see the head of this file.
corpus_ensure() {
  local section="$1" summary
  corpus_bin "$section"
  corpus_run check
  if [[ "$corpus_code" != 0 ]]; then
    # The one refusal that means absent: issueTitled finds no issue of the seeded issue's title in
    # the project (seedcorpus/check.go). Any other refusal is a corpus that is there and broken.
    grep -q "holds 0 issues titled" "$work/corpus.err" ||
      fail "$section" "the database holds the seeded corpus in $project, whole" \
        "dispatch-corpus check exited $corpus_code: $(<"$work/corpus.err") $(grep -E '^(FAIL|check):' "$work/corpus.out")"
    corpus_run seed --test-copy
    [[ "$corpus_code" == 0 ]] ||
      fail "$section" "seed the corpus into $project" \
        "dispatch-corpus seed --test-copy exited $corpus_code: $(<"$work/corpus.err") $(grep -E '^(FAIL|check):' "$work/corpus.out")"
    corpus_seeded=1
  fi
  summary="$(grep -E '^check: [0-9]+ clauses on ' "$work/corpus.out" | tail -n 1)"
  [[ "$summary" =~ ^check:\ [0-9]+\ clauses\ on\ [^\ ]+\ \(([A-Z][A-Z0-9]*-[0-9]+)\ the\ seeded\ issue\):\ ([0-9]+)\ passed,\ ([0-9]+)\ left\ to\ a\ separate\ part,\ 0\ failed$ ]] ||
    fail "$section" "the loader's self-check names the seeded issue and fails no clause" "it printed: ${summary:-no summary line}"
  corpus_issue="${BASH_REMATCH[1]}"
  [[ "${BASH_REMATCH[2]}" -gt 0 ]] ||
    fail "$section" "the loader's self-check passes a clause" "it printed: $summary"
  if [[ "$corpus_seeded" == 1 ]]; then
    pass "$section" "seeded the corpus into $project, whose seeded issue is $corpus_issue: $summary"
  else
    pass "$section" "the database holds the corpus in $project, whose seeded issue is $corpus_issue: $summary"
  fi
}

# corpus_claim SECTION WHAT ARGS…: see the head of this file. A refused command fails WHAT, with
# what it printed; the lines it printed are left in $work/corpus.out.
corpus_claim() {
  local section="$1" what="$2"
  shift 2
  corpus_bin "$section"
  corpus_run "$@"
  [[ "$corpus_code" == 0 ]] ||
    fail "$section" "$what" "dispatch-corpus $1 exited $corpus_code: $(<"$work/corpus.err") $(<"$work/corpus.out")"
}
