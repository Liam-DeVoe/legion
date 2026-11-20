# shellcheck shell=bash
# shellcheck disable=SC2154 # check-go-offline.test.sh initializes the shared harness variables.

# Cases of check-go-offline.sh's rule 7: each offline go command a step of a required Go job runs
# loads only packages a fetch before it in the job names, by its packages (a /... pattern ends at a
# nested module), its tests, its tags and its platform; a command it cannot read, or one no fetch
# holds, passes only with a go-offline reason; a step that goes online on purpose is not read.
# Sourced by check-go-offline.test.sh, whose fixtures, run_check and counters they use; not run on
# its own.

# fetched <name> <fetch file> <steps>: a fixture whose job lint restores ~/go/pkg/mod, runs the
# fetch file <fetch file> (as .github/go-fetch/lint.txt), then <steps> (indented to sit under
# `steps:`); packages/app is a Go module. Run through the check.
fetched() {
  local root
  root=$(fixture "$1")
  printf 'module example.com/app\n\ngo 1.21\n' | put "$root" packages/app/go.mod
  printf '%s\n' "$2" | put "$root" .github/go-fetch/lint.txt
  {
    cat <<'EOF'
      - name: Restore the module cache
        uses: actions/cache/restore@v4
        with:
          path: ~/go/pkg/mod
          key: go-app
      - name: Fetch the modules the tests build
        run: .github/scripts/go-fetch.sh file .github/go-fetch/lint.txt
EOF
    printf '%s\n' "$3"
  } >>"$root/.github/workflows/ci.yaml"
  run_check "$root"
}

TEST_X='      - name: Test the app
        run: go test -count=1 ./internal/x/
        working-directory: packages/app'

echo "case: a go step whose packages a deps line of the fetch before it holds passes, and is counted"
fetched fetched-held 'deps packages/app -test ./...' "$TEST_X"
check "exits 0" "$(is "$status" 0)"
check "counts it" "$(contains "$out" 'each of the 1 offline go commands the steps of required Go jobs run loads only packages a fetch before it in its job names (0 say')"

echo "case: a go step whose packages no deps line holds is refused, naming the line to add"
fetched fetched-short 'deps packages/app -test ./other/' "$TEST_X"
check "fails" "$(is "$status" 1)"
check "names the job, the step, the command and the line" \
  "$(contains "$out" 'job lint, step "Test the app", runs `go test -count=1 ./internal/x/`, which loads packages/app/internal/x, and no fetch before it in the job names them: add `deps packages/app -test ./internal/x`')"

echo "case: a go test needs the line's -test"
fetched fetched-no-test 'deps packages/app ./...' "$TEST_X"
check "fails" "$(is "$status" 1)"
check "names the line with -test" "$(contains "$out" 'add `deps packages/app -test ./internal/x`')"

echo "case: a go step's tags must be the line's"
VET_LIVE='      - name: Vet the live tests
        run: go vet -tags live ./...
        working-directory: packages/app'
fetched fetched-tags-other 'deps packages/app -test ./...' "$VET_LIVE"
check "untagged line: fails" "$(is "$status" 1)"
check "names the tagged line to add" "$(contains "$out" 'add `deps packages/app -test -tags live ./...`')"
fetched fetched-tags-same 'deps packages/app -test -tags live ./...' "$VET_LIVE"
check "the same tags: exits 0" "$(is "$status" 0)"

echo "case: a go step's GOOS must be the line's"
VET_DARWIN='      - name: Vet for darwin
        run: GOOS=darwin go vet ./...
        working-directory: packages/app'
fetched fetched-goos-other 'deps packages/app -test ./...' "$VET_DARWIN"
check "a line for this platform: fails" "$(is "$status" 1)"
check "names the darwin line to add" "$(contains "$out" 'add `GOOS=darwin deps packages/app -test ./...`')"
fetched fetched-goos-same 'GOOS=darwin deps packages/app -test ./...' "$VET_DARWIN"
check "a darwin line: exits 0" "$(is "$status" 0)"

echo "case: a /... pattern ends at a nested module, as go's does"
fetched fetched-nested 'deps packages/app -test ./...' '      - name: Test the nested module
        run: go test ./...
        working-directory: packages/app/sub'
check "with no go.mod there, the parent line holds it: exits 0" "$(is "$status" 0)"
root=$(fixture fetched-nested-module)
printf 'module example.com/app\n\ngo 1.21\n' | put "$root" packages/app/go.mod
printf 'module example.com/sub\n\ngo 1.21\n' | put "$root" packages/app/sub/go.mod
printf 'deps packages/app -test ./...\n' | put "$root" .github/go-fetch/lint.txt
cat >>"$root/.github/workflows/ci.yaml" <<'EOF'
      - name: Restore the module cache
        uses: actions/cache/restore@v4
        with:
          path: ~/go/pkg/mod
          key: go-app
      - name: Fetch the modules the tests build
        run: .github/scripts/go-fetch.sh file .github/go-fetch/lint.txt
      - name: Test the nested module
        run: go test ./...
        working-directory: packages/app/sub
EOF
run_check "$root"
check "with its own go.mod, it does not: fails" "$(is "$status" 1)"
check "names the nested module's line" "$(contains "$out" 'add `deps packages/app/sub -test ./...`')"

echo "case: a fetch after the go step holds nothing for it"
root=$(fixture fetched-after)
printf 'module example.com/app\n\ngo 1.21\n' | put "$root" packages/app/go.mod
printf 'deps packages/app -test ./...\n' | put "$root" .github/go-fetch/lint.txt
cat >>"$root/.github/workflows/ci.yaml" <<'EOF'
      - name: Restore the module cache
        uses: actions/cache/restore@v4
        with:
          path: ~/go/pkg/mod
          key: go-app
      - name: Test the app
        run: go test -count=1 ./internal/x/
        working-directory: packages/app
      - name: Fetch the modules the tests build
        run: .github/scripts/go-fetch.sh file .github/go-fetch/lint.txt
EOF
run_check "$root"
check "fails" "$(is "$status" 1)"
check "names the step before the fetch" "$(contains "$out" 'job lint, step "Test the app", runs `go test -count=1 ./internal/x/`, which loads packages/app/internal/x, and no fetch before it')"

echo "case: a go step this cannot read is refused, and passes with a reason that names its fetch"
fetched fetched-unread 'deps packages/app -test ./...' '      - name: Test one package
        run: go test -count=1 "$PKG"
        working-directory: packages/app'
check "fails" "$(is "$status" 1)"
check "names the command it cannot read" \
  "$(contains "$out" 'job lint, step "Test one package", runs `go test -count=1 \$PKG`, whose packages, tags, directory or GOOS, GOARCH, CGO_ENABLED or GOWORK this rule cannot read')"
fetched fetched-unread-reason 'deps packages/app -test ./...' '      # go-offline: PKG is one package under packages/app, which the deps line of lint.txt holds.
      - name: Test one package
        run: go test -count=1 "$PKG"
        working-directory: packages/app'
check "with a reason above its step's name: exits 0" "$(is "$status" 0)"
check "counted as one that says why" "$(contains "$out" 'loads only packages a fetch before it in its job names (1 say which fetch covers them')"
fetched fetched-short-reason 'deps packages/app -test ./other/' '      - name: Test the app
        run: |
          # go-offline: internal/x imports nothing outside the standard library.
          go test -count=1 ./internal/x/
        working-directory: packages/app'
check "a command no line holds, with a reason on the line above it: exits 0" "$(is "$status" 0)"

echo "case: a step that sends its go online on purpose is not held to the fetch"
fetched fetched-online 'deps packages/app -test ./other/' '      # go-offline: the pack goes online as the release does, and runs no test.
      - name: Pack the app
        env:
          GOPROXY: https://proxy.golang.org
        run: go build -o out/app ./cmd/app
        working-directory: packages/app'
check "exits 0" "$(is "$status" 0)"
check "and counts nothing of it" "$(contains "$out" 'each of the 0 offline go commands')"

echo "case: a cd before the go command is its directory, and go run's arguments are no packages"
fetched fetched-cd 'deps packages/app -test ./internal/x/
deps packages/app ./cmd/tool' '      - name: Test from the root
        run: |
          cd packages/app && go test -count=1 ./internal/x/
          go run ./cmd/tool check --root ../..'
check "exits 0" "$(is "$status" 0)"
check "counts both" "$(contains "$out" 'each of the 2 offline go commands')"

echo "case: a cd inside a subshell holds for that subshell alone"
fetched fetched-subshell 'deps packages/app -test ./internal/x/' '      - name: Test in a subshell, then outside it
        run: |
          (cd packages/app && go test -count=1 ./internal/x/)
          go test -count=1 ./internal/x/'
check "fails" "$(is "$status" 1)"
check "names the command after the subshell, run from the root" \
  "$(contains "$out" 'step "Test in a subshell, then outside it", runs `go test -count=1 ./internal/x/`, which loads internal/x, and no fetch before it')"
check "and only it, the one inside the subshell held" "$(contains "$out" '^1 problems over')"

echo "case: a race step's fetch line cut to one of its packages is refused, naming the rest"
fetched fetched-cut 'deps packages/app -test ./b/' '      - name: Race the app
        run: go test -count=1 -race ./a/... ./b/
        working-directory: packages/app'
check "fails" "$(is "$status" 1)"
check "names the packages the line lacks" "$(contains "$out" 'which loads packages/app/a/... packages/app/b, and no fetch before it in the job names them')"
