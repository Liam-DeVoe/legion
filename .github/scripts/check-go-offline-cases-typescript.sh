# shellcheck shell=bash
# Cases of check-go-offline.sh's rule 1 in TypeScript and JavaScript test files: a bare site, the
# offline and the reasoned form, and one planted case for each TypeScript form the check's header
# names. Sourced by check-go-offline.test.sh, whose fixtures, run_check and counters they use; not
# run on its own.

echo "case: a TypeScript test that runs go list -deps with no rule is refused"
root=$(fixture ts-bare)
put "$root" packages/web/deps.test.ts <<'EOF'
import { expect, test } from "bun:test";

test("deps", () => {
  const listed = Bun.spawnSync(["go", "list", "-deps", "./..."]);
  expect(listed.exitCode).toBe(0);
});
EOF
run_check "$root"
refused packages/web/deps.test.ts 4

echo "case: the TypeScript test with GOPROXY: \"off\" in the spawn's environment passes"
root=$(fixture ts-offline)
put "$root" packages/web/deps.test.ts <<'EOF'
import { expect, test } from "bun:test";

test("deps", () => {
  const listed = Bun.spawnSync(["go", "list", "-deps", "./..."], {
    env: { ...process.env, GOPROXY: "off" },
  });
  expect(listed.exitCode).toBe(0);
});
EOF
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "counts the command as offline" "$(contains "$out" 'runs with GOPROXY=off (1), carries a reason (0)')"

echo "case: the TypeScript test with a reason on the line above passes"
root=$(fixture ts-reasoned)
put "$root" packages/web/deps.test.ts <<'EOF'
import { expect, test } from "bun:test";

test("deps", () => {
  // go-offline: it lists the standard library, which reads no module.
  const listed = Bun.spawnSync(["go", "list", "-deps", "std"]);
  expect(listed.exitCode).toBe(0);
});
EOF
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "counts the command as reasoned" "$(contains "$out" 'runs with GOPROXY=off (0), carries a reason (1)')"

echo "case: a TypeScript argv array with -C and a directory before its subcommand is a site"
root=$(fixture ts-chdir)
put "$root" packages/web/deps.test.ts <<'EOF'
import { expect, test } from "bun:test";

test("deps", () => {
  const argv = ["go", "-C", moduleDir, "list", "-deps", "./..."];
  expect(Bun.spawnSync(argv).exitCode).toBe(0);
});
EOF
run_check "$root"
refused packages/web/deps.test.ts 4

echo "case: TypeScript: the cmd of a Bun.spawnSync Biome writes one element a line"
refuses ts-biome-cmd packages/web/biome.test.ts 2 <<'EOF'
test("build", () => {
  Bun.spawnSync({
    cmd: [
      "go",
      "build",
      "./cmd/listener",
    ],
  });
});
EOF

echo "case: TypeScript: an argv Biome writes one element a line"
refuses ts-biome-argv packages/web/argv.test.ts 2 <<'EOF'
test("deps", () => {
  Bun.spawnSync(
    [
      "go",
      "list",
      "-deps",
      "./...",
    ],
    { cwd: dir }
  );
});
EOF

echo "case: TypeScript: a spawn does not borrow the next one's GOPROXY: \"off\""
refuses ts-neighbour packages/web/neighbour.test.ts 2 2 <<'EOF'
test("deps", () => {
  Bun.spawnSync(["go", "list", "-deps", "./..."]);
  Bun.spawnSync(["go", "build", "./..."], {
    env: { ...process.env, GOPROXY: "off" },
  });
});
EOF

echo "case: TypeScript: GOPROXY: \"off\" before a spread of the environment is overridden"
refuses ts-before-spread packages/web/spread.test.ts 2 <<'EOF'
test("build", () => {
  Bun.spawnSync(["go", "build", "./..."], {
    env: { GOPROXY: "off", ...process.env },
  });
});
EOF

echo "case: TypeScript: execSync of a shell string that runs go after cd"
refuses ts-execsync packages/web/execsync.test.ts 2 <<'EOF'
test("build", () => {
  execSync("cd sub && go build ./...");
});
EOF

echo "case: TypeScript: exec of a go command (Node's child_process)"
refuses ts-exec packages/web/exec.test.ts 2 <<'EOF'
test("deps", () => {
  exec("go list -deps ./...", () => {});
});
EOF

echo "case: TypeScript: Bun's \$\`go ...\`"
refuses ts-bun-shell packages/web/shell.test.ts 2 <<'EOF'
test("deps", async () => {
  await $`go list -deps ./...`;
});
EOF

echo "case: TypeScript: Bun's \$\` with cd before go, and with -C and a substitution"
refuses ts-bun-shell-cd packages/web/shellcd.test.ts 2 <<'EOF'
test("build", async () => {
  await $`cd ${dir} && go build ./...`;
});
EOF
refuses ts-bun-shell-chdir packages/web/shellchdir.test.ts 2 <<'EOF'
test("build", async () => {
  await $`go -C ${dir} build ./cmd/listener`;
});
EOF

echo "case: TypeScript: Bun's \$\` with a .env({...}) that ends in GOPROXY: \"off\" is offline"
passes_offline ts-bun-shell-env packages/web/shellenv.test.ts <<'EOF'
test("deps", async () => {
  await $`go list -deps ./...`.env({ ...process.env, GOPROXY: "off" });
});
EOF

echo "case: TypeScript: a go Bun.which found, bound to a name"
refuses ts-which packages/web/which.test.ts 3 <<'EOF'
test("build", () => {
  const go = Bun.which("go");
  Bun.spawn([go!, "build", "./..."]);
});
EOF

echo "case: TypeScript: go after env in an argv, and a shell's -c"
refuses ts-env-argv packages/web/env.test.ts 2 <<'EOF'
test("build", () => {
  Bun.spawnSync(["env", "GOFLAGS=-mod=mod", "go", "build", "./..."]);
});
EOF
refuses ts-bash-c packages/web/bash.test.ts 2 <<'EOF'
test("build", () => {
  Bun.spawnSync(["bash", "-c", "go build ./..."]);
});
EOF

echo "case: TypeScript: Node's spawnSync(\"go\", [...]) with GOPROXY: \"off\" in its own env is offline"
passes_offline ts-node-spawn packages/web/node.test.ts <<'EOF'
test("deps", () => {
  spawnSync("go", ["list", "./..."], { env: { ...process.env, GOPROXY: "off" } });
});
EOF

echo "case: TypeScript: a .mts test is read"
refuses ts-mts packages/web/deps.test.mts 2 <<'EOF'
test("deps", () => {
  Bun.spawnSync(["go", "list", "-deps", "./..."]);
});
EOF
