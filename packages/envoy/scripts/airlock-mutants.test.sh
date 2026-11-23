#!/usr/bin/env bash
# airlock-mutants.test.sh: what airlock-mutants.sh counts for a mutant held to the tests it names,
# run against a fixture tree and a stand-in `go`, so no real package, database or model is used.
#
# The real script is copied into a fixture packages/envoy that holds one file, fixture.go, and three
# requests-* mutants of it. The stand-in go answers `go test -run '^(<Test>)$'` from the copy it
# runs in: TestFixtureEnds passes while fixture.go says ENDS=yes and fails once a patch made it say
# ENDS=no, and every named test fails when BROKER_TEST_DATABASE_URL names port 1, as a real test
# does when its database refuses the connection. It holds the script to:
#   - a mutant its named test catches, on a database that answers, is PASS and the run exits 0;
#   - the same mutant on a database that refuses connections is UNKNOWN ("its named tests fail
#     without the mutant") and the run exits 3, since the failure is the database's and not the
#     mutant's: the named tests are run on the unpatched copy first and must pass there;
#   - a mutant its named test does not catch is FAIL and the run exits 1, so the first run on the
#     unpatched copy hides no hole;
#   - with no database, the mutant is UNKNOWN and the run exits 3; with --skip-database-mutants it
#     is SKIPPED, and a run in which no mutant ran at all still exits 3, as UNKNOWN.
set -uo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

fx="$work/repo"
envoy="$fx/packages/envoy"
mkdir -p "$envoy/scripts" "$envoy/internal/cipher/requests/testdata/mutants" "$work/bin"
cp "$script_dir/airlock-mutants.sh" "$envoy/scripts/"
printf 'package requests\n\n// ENDS=yes\n' >"$envoy/internal/cipher/requests/fixture.go"

mutant() { # <name> <the line its patch writes in place of "// ENDS=yes"> <the test it names>
	cat >"$envoy/internal/cipher/requests/testdata/mutants/$1.patch" <<EOF
# $1: a fixture mutant.
# must fail: $3

--- a/packages/envoy/internal/cipher/requests/fixture.go
+++ b/packages/envoy/internal/cipher/requests/fixture.go
@@ -1,3 +1,3 @@
 package requests
 
-// ENDS=yes
+$2
EOF
}
mutant requests-fixture-caught '// ENDS=no' TestFixtureEnds
mutant requests-fixture-missed '// ENDS=yes, said again' TestFixtureEnds

cat >"$work/bin/go" <<'EOF'
#!/usr/bin/env bash
# The stand-in go: `go test ... -run '^(<Test>|...)$' <package>`, answered from fixture.go.
[ "$1" = test ] || { echo "stand-in go: only go test is answered, not $*" >&2; exit 2; }
run=""
while [ $# -gt 0 ]; do
	[ "$1" = -run ] && run="$2"
	shift
done
tests="${run#^(}"
tests="${tests%)\$}"
status=0
for test in ${tests//|/ }; do
	echo "=== RUN   $test"
	if [[ "${BROKER_TEST_DATABASE_URL:-}" == *:1/* ]] || ! grep -q '^// ENDS=yes' internal/cipher/requests/fixture.go; then
		echo "--- FAIL: $test (0.00s)"
		status=1
	else
		echo "--- PASS: $test (0.00s)"
	fi
done
exit "$status"
EOF
chmod +x "$work/bin/go"

failures=0
check() { # <label> <want exit> <want text> <run's output> <run's exit>
	if [[ "$5" == "$2" && "$4" == *"$3"* ]]; then
		printf 'PASS: %s\n' "$1"
	else
		printf 'FAIL: %s — exit %s (want %s); output:\n%s\n' "$1" "$5" "$2" "$4"
		failures=$((failures + 1))
	fi
}
run() { # <database url, or "" for none> <args...>
	local url="$1"
	shift
	if [ -n "$url" ]; then
		PATH="$work/bin:$PATH" BROKER_TEST_DATABASE_URL="$url" TMPDIR="$work" bash "$envoy/scripts/airlock-mutants.sh" "$@" 2>&1
	else
		env -u BROKER_TEST_DATABASE_URL PATH="$work/bin:$PATH" TMPDIR="$work" bash "$envoy/scripts/airlock-mutants.sh" "$@" 2>&1
	fi
}

live='postgres://fixture@127.0.0.1:5432/fixture?sslmode=disable'
dead='postgres://nobody:x@127.0.0.1:1/none?sslmode=disable&connect_timeout=2'

out="$(run "$live" requests-fixture-caught)"
check "a mutant its named test catches, on a database that answers, is caught" 0 "PASS: requests-fixture-caught" "$out" $?

out="$(run "$dead" requests-fixture-caught)"
check "the same mutant on a database that refuses connections is UNKNOWN and exits 3" 3 \
	"UNKNOWN: requests-fixture-caught — its named tests fail without the mutant" "$out" $?

out="$(run "$live" requests-fixture-missed)"
check "a mutant its named test does not catch fails the run" 1 "FAIL: requests-fixture-missed" "$out" $?

out="$(run "" requests-fixture-caught)"
check "with no database the mutant is UNKNOWN and the run exits 3" 3 "UNKNOWN: requests-fixture-caught — not run" "$out" $?

out="$(run "" --skip-database-mutants requests-fixture-caught)"
check "a run whose every mutant was skipped exits 3 as UNKNOWN" 3 "UNKNOWN: no mutant ran" "$out" $?

out="$(run "" --skip-database-mutants requests-fixture-caught requests-fixture-missed)"
check "the SKIPPED line names the skipped mutant" 3 "SKIPPED: requests-fixture-caught" "$out" $?

if [ "$failures" -gt 0 ]; then
	printf '%d check(s) of airlock-mutants.sh failed\n' "$failures"
	exit 1
fi
printf 'every check of airlock-mutants.sh passed\n'
