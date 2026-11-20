# shellcheck shell=bash
# shellcheck disable=SC2154 # check-omp-offline.test.sh initializes the shared harness variables.

# Cases of which files check-omp-offline.sh reads: a file that is no test code is not, a helper
# under __tests__ or tests/, a .spec. file and a JavaScript test are, nothing under testdata,
# node_modules, dist, .git or .jj is. Sourced by check-omp-offline.test.sh, whose fixtures,
# run_check, named and counters they use; not run on its own.

echo "case: a start in a file that is no test code is not read, in either language"
root=$(fixture non_test)
put "$root" packages/daemon/internal/podsafety/runner.go <<'EOF'
package podsafety

func Models(home string) error {
	cmd := exec.Command(os.Getenv("LEGION_TEST_OMP"), "models", "list")
	cmd.Env = []string{"HOME=" + home}
	return cmd.Run()
}
EOF
put "$root" packages/pi-envoy/src/run-pinned.ts <<'EOF'
export async function models() {
  await Bun.spawn([process.env.LEGION_TEST_OMP!, "models"], { env: { HOME: "/tmp/home" } }).exited;
}
EOF
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "counts only the fixture's starts" "$(contains "$out" '3 starts of Oh My Pi in 4 test files read')"

echo "case: a helper under __tests__ is test code"
root=$(fixture tests_helper)
put "$root" packages/pi-envoy/src/__tests__/omp-helper.ts <<'EOF'
export async function models(home: string) {
  await Bun.spawn([process.env.LEGION_TEST_OMP!, "models"], { env: { HOME: home } }).exited;
}
EOF
run_check "$root"
check "fails" "$(is "$status" 1)"
check "names its start" "$(named omp-helper.ts 2)"

echo "case: a helper under tests/, a .spec. file and a JavaScript test are test code; nothing under testdata, node_modules, dist, .git or .jj is read"
root=$(fixture ts_files)
put "$root" packages/pi-envoy/tests/omp-runner.ts <<'EOF'
export async function models(home: string) {
  await Bun.spawn([process.env.LEGION_TEST_OMP!, "models"], { env: { HOME: home } }).exited;
}
EOF
put "$root" packages/pi-envoy/extensions/models-omp.spec.ts <<'EOF'
test("models", async () => {
  await Bun.spawn([process.env.LEGION_TEST_OMP!, "models"], { env: { HOME: "/tmp/home" } }).exited;
});
EOF
put "$root" packages/pi-envoy/extensions/models-omp.test.js <<'EOF'
test("models", async () => {
  await Bun.spawn([process.env.LEGION_TEST_OMP, "models"], { env: { HOME: "/tmp/home" } }).exited;
});
EOF
for skipped in testdata node_modules dist .git .jj; do
  put "$root" "packages/pi-envoy/$skipped/skipped-omp.test.ts" <<'EOF'
test("models", async () => {
  await Bun.spawn([process.env.LEGION_TEST_OMP!, "models"], { env: { HOME: "/tmp/home" } }).exited;
});
EOF
done
run_check "$root"
check "fails" "$(is "$status" 1)"
check "names the start in the helper under tests/" "$(named tests/omp-runner.ts 2)"
check "names the start in the .spec. file" "$(named models-omp.spec.ts 2)"
check "names the start in the JavaScript test" "$(named models-omp.test.js 2)"
check "reads no file under the five skipped directories" "$(is "$(contains "$out" 'skipped-omp')" false)"
check "counts the three" "$(contains "$out" '3 problems over the 6 starts of Oh My Pi in 7 test files read')"
