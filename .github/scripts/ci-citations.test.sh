#!/usr/bin/env bash
# Tests for ci_citations.py, which holds each citation token under .github to the file it
# names and refuses a citation of any other shape there:
# - a token whose name its file holds passes and is counted; after a rename in that file it
#   fails, naming the path and the name; a name that only a comment of the file holds fails, and
#   so do a text name that is the start of a longer word and an identifier that is a prefix;
# - a token whose path is no tracked file fails: a path never tracked, a file name without its
#   root path, a file deleted, a file renamed with git mv; and a file added elsewhere under the
#   same name changes no token;
# - the cited file's comments are removed in one pass that steps over its strings, so a comment
#   marker inside a string removes nothing (the shape of the playwright config that review
#   6033494723 found), in TypeScript, shell and Python;
# - each spelling of a line number beside a path word is refused, and so is a token whose name
#   is a line number;
# - a backticked text that is no token, right after a path word (a possessive, a parenthesis or
#   a line break between them included), is refused with the message that names the token to
#   write, and so is a backticked text before in and a path; one further on in the sentence is
#   not, which is the stated limit of that rule;
# - a file outside .github is not read, and ordinary text that names no path passes;
# - a scan that finds no token fails rather than passing;
# - and this repository's own .github passes, with at least 700 tokens.
#
# This file is under .github, so the real scan reads it as text. Every token, line number and
# backticked text a case needs is built at run time from bt, h and c below, so this file's own
# text holds none of them.
#
# Each case builds a miniature git repository under a temporary directory and symlinks the real
# script into it, so the script under test is the file CI runs: it resolves its root from its own
# path, which the symlink puts at the fixture root, and it reads no symlink.
#
# Run from anywhere: .github/scripts/ci-citations.test.sh
# CI runs it in the lint job, before the check itself.
set -euo pipefail
shopt -s inherit_errexit

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo=$(cd "$script_dir/../.." && pwd)
check_script="$script_dir/ci_citations.py"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# shellcheck source=.github/scripts/test-lib.sh
source "$script_dir/test-lib.sh"

bt='`'
h='#'
c=':'

# tok <path> <name>: a citation token.
tok() { printf '%s%s%s%s%s' "$bt" "$1" "$h" "$2" "$bt"; }

# fixture <name>: a git repository holding the script and the files the cases cite. Echoes its
# root. packages/app/main_test.go defines TestReadsTheConfig, runs a test titled "the agent
# rows", names TestOldName only in a comment and holds the string setup-golang;
# playwright.config.ts, run.sh and tool.py each hold a comment marker inside a string ahead of
# a name.
fixture() {
  local root="$work/$1"
  mkdir -p "$root/.github/scripts" "$root/.github/ci-notes" "$root/.github/docker" "$root/.github/workflows" \
    "$root/packages/app"
  ln -s "$check_script" "$root/.github/scripts/ci_citations.py"
  cat > "$root/packages/app/main_test.go" <<'EOF'
package app

// TestOldName was renamed.
var tool = "setup-golang"
func TestReadsTheConfig(t *testing.T) { runtime.Caller(0); t.Run("the agent rows", nil) }
EOF
  cat > "$root/packages/app/playwright.config.ts" <<'EOF'
// The suites: ls e2e/*.e2e.ts lists them.
const SUITES = "e2e/*.spec.ts";
const HOME = "http://localhost:3000"; const ROW_TIMEOUT = 5000;
export default { projects: [{ name: "iphone" }] };
/* The end of the projects. */
EOF
  cat > "$root/packages/app/run.sh" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "a # in a string"; held_after_hash=1
# only_in_a_comment
EOF
  cat > "$root/packages/app/tool.py" <<'EOF'
MARK = "# in a string"; HELD_AFTER_MARK = 1
# PY_ONLY_IN_A_COMMENT
EOF
  printf 'FROM scratch\nRUN true\n' > "$root/.github/docker/browser.Dockerfile"
  printf '* @owner\n' > "$root/.github/CODEOWNERS"
  printf 'dist/\n' > "$root/.gitignore"
  printf 'jobs:\n  x:\n    runs-on: ubuntu-24.04\n' > "$root/.github/workflows/ci.yaml"
  echo "$root"
}

# track <root>: tracks every file of the fixture, as the script reads tracked files only.
track() {
  git -C "$1" init -q
  git -C "$1" add -A
}

run_check() { # run_check <root>
  set +e
  out=$(cd "$1" && python3 .github/scripts/ci_citations.py 2>&1)
  status=$?
  set -e
}

notes() { # notes <root>: the fixture's citing Markdown file under .github, from stdin
  cat > "$1/.github/ci-notes/notes.md"
}

# refused <line> <text>: true when the output refuses notes.md at that line with a message
# containing the text, for check.
refused() {
  grep -F -- ".github/ci-notes/notes.md$c$1: " <<<"$out" | grep -qF -- "$2" && echo true || echo false
}

app="packages/app/main_test.go"
pw="packages/app/playwright.config.ts"

echo "case: a token whose name its file holds passes, and is counted"
root=$(fixture held)
notes "$root" <<EOF
The config is read by $(tok "$app" TestReadsTheConfig),
$(tok "$app" runtime.Caller) and $(tok "$app" '"the agent rows"').
EOF
track "$root"
run_check "$root"
check "passes" "$(is "$status" 0)"
check "counts three tokens in one file" "$(contains "$out" '^3 citation tokens in 1 of the [0-9]* text files under .github/')"

echo "case: the same token after a rename in the cited file fails, naming the path and the name"
sed -i 's/TestReadsTheConfig/TestReadsItsConfig/' "$root/$app"
run_check "$root"
check "fails" "$(is "$status" 1)"
check "names the path and the name" "$(refused 1 "${bt}$app${bt} holds no ${bt}TestReadsTheConfig${bt}")"
check "and holds the other two" "$(is "$(grep -c 'holds no' <<<"$out")" 1)"

echo "case: a name only a comment holds, a text that starts a longer word or an identifier prefix fails"
root=$(fixture comments)
notes "$root" <<EOF
$(tok "$app" TestOldName)

$(tok "$app" setup-go)

$(tok "$app" TestReads)
EOF
track "$root"
run_check "$root"
check "fails" "$(is "$status" 1)"
check "on the name its comment alone holds" "$(refused 1 "holds no ${bt}TestOldName${bt}")"
check "on the text that starts a longer word" "$(refused 3 "holds no ${bt}setup-go${bt}")"
check "on the identifier that is a prefix" "$(refused 5 "holds no ${bt}TestReads${bt}")"

echo "case: a token whose path is no tracked file fails"
root=$(fixture untracked)
notes "$root" <<EOF
$(tok packages/app/gone_test.go TestGone)

$(tok main_test.go TestReadsTheConfig)

$(tok packages/app/run.sh held_after_hash)

$(tok "$app" TestReadsTheConfig)
EOF
track "$root"
git -C "$root" rm -qf packages/app/run.sh
git -C "$root" mv "$app" packages/app/reads_test.go
run_check "$root"
check "fails" "$(is "$status" 1)"
check "on a path never tracked" "$(refused 1 "${bt}packages/app/gone_test.go${bt} names no tracked file")"
check "on a file name without its root path" "$(refused 3 "${bt}main_test.go${bt} names no tracked file")"
check "on a deleted file" "$(refused 5 "${bt}packages/app/run.sh${bt} names no tracked file")"
check "on a file git mv renamed" "$(refused 7 "${bt}$app${bt} names no tracked file")"

echo "case: a file added elsewhere under the same name changes no token"
root=$(fixture shadowed)
notes "$root" <<EOF
$(tok "$app" TestReadsTheConfig) and $(tok .github/workflows/ci.yaml runs-on)
EOF
track "$root"
run_check "$root"
before=$out
mkdir -p "$root/packages/zz"
cp "$root/.github/workflows/ci.yaml" "$root/packages/zz/ci.yaml"
printf 'package zz\n' > "$root/packages/zz/main_test.go"
git -C "$root" add -A
run_check "$root"
check "passes" "$(is "$status" 0)"
check "with the same count as before" "$(is "$out" "$before")"

echo "case: the cited file's comments are removed in one pass that steps over its strings"
root=$(fixture strings)
notes "$root" <<EOF
$(tok "$pw" '"iphone"') and $(tok "$pw" ROW_TIMEOUT), after a /* and a // inside strings;
$(tok packages/app/run.sh held_after_hash), after a # inside a string; and
$(tok packages/app/tool.py HELD_AFTER_MARK), after a # inside a Python string.

$(tok packages/app/run.sh only_in_a_comment)

$(tok packages/app/tool.py PY_ONLY_IN_A_COMMENT)
EOF
track "$root"
run_check "$root"
check "fails on the two names only a comment holds" "$(is "$(grep -c 'holds no' <<<"$out")" 2)"
check "the shell comment" "$(refused 5 "holds no ${bt}only_in_a_comment${bt}")"
check "the Python comment" "$(refused 7 "holds no ${bt}PY_ONLY_IN_A_COMMENT${bt}")"
check "and holds every name after a marker inside a string" "$(is "$(grep -c 'notes.md.[123]: ' <<<"$out")" 0)"

echo "case: a line number beside a path word is refused, in every spelling"
root=$(fixture lines)
# One spelling a paragraph: the line each one starts on is the line the refusal names.
spellings=(
  "$app${c}4 reads it"
  "$app${c}L4 reads it"
  "$app${h}L4 reads it"
  "$app${h}4 reads it"
  "$app L4 reads it"
  "$app line 4 reads it"
  "$app (line 4) reads it"
  "$app's line 4 reads it"
  "$app, line 4, reads it"
  "lines 3 to 4 of $app read it"
  "$app (l. 4) reads it"
  "$app (${h}4) reads it"
  "$app line4 reads it"
  "it is $app${c}"
  "$app reads it (${c}4)"
  "$app reads it at ${c}4"
  "$app and ${c}4 read it"
  "$app, ${c}4, reads it"
  "$(tok "$app" 4) reads it"
  "browser.Dockerfile${c}2, CODEOWNERS${c}1 and .gitignore${c}1 read it"
)
for spelling in "${spellings[@]}"; do
  printf '%s\n\n' "$spelling"
done > "$root/.github/ci-notes/notes.md"
# The wrap: the number starts the line after the one that ends with the path and a colon.
sed -i '28s/^$/4 is the line/' "$root/.github/ci-notes/notes.md"
track "$root"
run_check "$root"
check "fails" "$(is "$status" 1)"
for i in "${!spellings[@]}"; do
  check "refuses: ${spellings[$i]}" "$(refused $((2 * i + 1)) 'by a line number; write it as')"
done
check "names the path a file name resolves to" \
  "$(refused 39 "cites ${bt}.github/docker/browser.Dockerfile${bt} by a line number; write it as $(tok .github/docker/browser.Dockerfile Name)")"
check "and every kind of path word" "$(is "$(grep -c "notes.md${c}39: " <<<"$out")" 3)"

echo "case: a backticked text that is no token, right beside a path word, is refused"
root=$(fixture nontoken)
notes "$root" <<EOF
The job is ci.yaml's ${bt}x${bt}.

The job ${bt}x${bt} of ci.yaml.

The step is ${bt}ci.yaml${bt} (${bt}y${bt}).

The job is in ci.yaml
${bt}z${bt}, on the next line.
EOF
track "$root"
run_check "$root"
check "fails" "$(is "$status" 1)"
check "names the token to write" "$(refused 1 "write it as $(tok .github/workflows/ci.yaml x)")"
check "refuses the text written before in, of or from and the path" \
  "$(refused 3 "${bt}x${bt} is followed by ci.yaml, and is no citation token: write it as $(tok .github/workflows/ci.yaml x)")"
check "reads a path in backticks and a name in parentheses" "$(refused 5 "write it as $(tok .github/workflows/ci.yaml y)")"
check "and a name on the line after the path" "$(refused 8 "write it as $(tok .github/workflows/ci.yaml z)")"
check "and refuses nothing else" "$(is "$(grep -c 'no citation token' <<<"$out")" 4)"

echo "case: a backticked text three words or more after a path word is not read, the stated limit of rule 4b"
# This pass is the stated limit of rule 4b, the rule that refuses a backticked text beside a path
# word, and not an accident: the rule reads a backticked text only right after a path word or
# before in, of or from and one, and reads any other as prose, so a citation written further from
# its path is neither held nor refused. ci_citations.py's docstring and ci.md say so. The name is
# one ci.yaml does not hold, so the pass shows the text was not read as a citation; the same name
# right after the path is then refused, so the pass is the rule's reach, not a file left unread.
root=$(fixture beyond)
notes "$root" <<EOF
$(tok "$app" TestReadsTheConfig)

ci.yaml holds, among others, the ${bt}no_such_output${bt} output.
EOF
track "$root"
run_check "$root"
check "passes" "$(is "$status" 0)"
check "and counts the one token" "$(contains "$out" '^1 citation tokens in 1 of')"
notes "$root" <<EOF
$(tok "$app" TestReadsTheConfig)

ci.yaml's ${bt}no_such_output${bt} output.
EOF
run_check "$root"
check "while the same name right after the path is refused" \
  "$(refused 3 "write it as $(tok .github/workflows/ci.yaml no_such_output)")"

echo "case: a file outside .github is not read"
root=$(fixture outside)
notes "$root" <<EOF
$(tok "$app" TestReadsTheConfig)
EOF
cat > "$root/packages/app/README.md" <<EOF
$(tok "$app" TestGone), $(tok packages/app/gone.go Gone), $app${c}4 and $app ${bt}Gone${bt}.
EOF
track "$root"
run_check "$root"
check "passes" "$(is "$status" 0)"
check "and counts the one token under .github" "$(contains "$out" '^1 citation tokens in 1 of')"

echo "case: ordinary text that names no path passes"
root=$(fixture ordinary)
notes "$root" <<EOF
$(tok "$app" TestReadsTheConfig)

The proxy listens on ${c}8080, exits (exit ${c}1) when refused, and runs hourly at ${c}15.
See line 3 of the output, or lines 2 and 3; ${bt}read -r line 2>/dev/null${bt} reads one.
A text further on in a sentence that names ci.yaml, such as ${bt}x${bt}, is no name beside it,
and nor is a path that a longer backticked text holds, as ${bt}ci.yaml x${bt} ${bt}y${bt} does.
EOF
track "$root"
run_check "$root"
check "passes" "$(is "$status" 0)"

echo "case: a scan that finds no token fails rather than passing"
root=$(fixture empty)
printf '# Notes\n' > "$root/.github/ci-notes/notes.md"
track "$root"
run_check "$root"
check "fails" "$(is "$status" 1)"
check "says it found no token" "$(contains "$out" '^found no citation token in the [0-9]* text files under .github/')"

echo "case: this repository's own .github holds every token and no citation of another shape"
run_check "$repo"
check "passes" "$(is "$status" 0)"
count=$(sed -n 's/^\([0-9][0-9]*\) citation tokens in .*/\1/p' <<<"$out")
check "over at least 700 tokens, so a scan that stopped finding them fails here" "$(is "$(( ${count:-0} >= 700 ))" 1)"

summary "ci_citations.py holds each citation token under .github to its file and refuses every other shape"
