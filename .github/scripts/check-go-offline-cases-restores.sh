# shellcheck shell=bash
# Cases of check-go-offline.sh's rule 3: a fetch step of a required job comes after the job's
# restore of ~/go/pkg/mod. Sourced by check-go-offline.test.sh, whose fixtures, run_check and
# counters they use; not run on its own.

echo "case: a required job whose fetch step comes before its module cache restore is refused"
root=$(fixture fetch-first)
cat >>"$root/.github/workflows/ci.yaml" <<'EOF'
      - name: Fetch the modules the tests build
        run: .github/scripts/go-fetch.sh deps packages/app ./cmd/app
      - uses: actions/cache/restore@v4
        with:
          path: |
            ~/go/pkg/mod
            ~/.cache/go-build
          key: go-app
EOF
run_check "$root"
check "fails" "$(is "$status" 1)"
check "names the job and the fetch step" \
  "$(contains "$out" 'job lint, step "Fetch the modules the tests build", runs go-fetch.sh with no step before it that restores ~/go/pkg/mod')"

echo "case: the same job with the restore first passes, and lists the fetch step with that restore"
root=$(fixture fetch-after)
cat >>"$root/.github/workflows/ci.yaml" <<'EOF'
      - name: Restore the module cache
        uses: actions/cache/restore@v4
        with:
          path: |
            ~/go/pkg/mod
            ~/.cache/go-build
          key: go-app
      - name: Fetch the modules the tests build
        run: .github/scripts/go-fetch.sh deps packages/app ./cmd/app
EOF
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "lists the pair" \
  "$(contains "$out" '^  .github/workflows/ci.yaml job lint: "Fetch the modules the tests build" after "Restore the module cache"')"
check "counts it" "$(contains "$out" 'each of the 1 fetch steps of a required job comes after a restore of ~/go/pkg/mod')"

echo "case: a restore of another path does not count"
root=$(fixture fetch-other-cache)
cat >>"$root/.github/workflows/ci.yaml" <<'EOF'
      - uses: actions/cache@v4
        with:
          path: ~/.cache/go-build
          key: go-build
      - name: Fetch the modules the tests build
        run: .github/scripts/go-fetch.sh deps packages/app ./cmd/app
EOF
run_check "$root"
check "fails" "$(is "$status" 1)"
check "names the fetch step" "$(contains "$out" 'step "Fetch the modules the tests build", runs go-fetch.sh with no step before it')"
