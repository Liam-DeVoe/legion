import { expect, test } from "bun:test";
import { chmodSync, copyFileSync, mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { requireHostTools } from "@legion/test-fixtures/host-tools";
import { temporaryDirectories } from "@legion/test-fixtures/temporary-directories";
import { versionControlEnvironment } from "@legion/test-fixtures/version-control-environment";

// label-texts.sh is the one entry the rig calls in a label's checkout. Each row copies it into a
// label of its own, a directory laid out as a checkout, so the row decides what that label holds.
const entry = path.join(import.meta.dir, "label-texts.sh");
const scripts = path.join("packages", "pi-legion", "scripts", "skill-scenarios");
const tests = [
  ["workflow", "rig_world_test.go", "TestRigWorld"],
  ["daemon", "rig_launch_test.go", "TestRigLaunch"],
  ["api", "rig_plan_test.go", "TestRigPlanCheck"],
] as const;

const directory = temporaryDirectories("label-texts");

type Environment = Record<string, string | undefined>;

/** A label holding label-texts.sh and nothing else, and its own home for git. */
function bareLabel(): { label: string; script: string; env: Environment } {
  const label = directory("label");
  mkdirSync(path.join(label, scripts), { recursive: true });
  const script = path.join(label, scripts, "label-texts.sh");
  copyFileSync(entry, script);
  const env = { ...process.env, ...versionControlEnvironment(directory("home")) };
  return { label, script, env };
}

function run(cwd: string, env: Environment, ...argv: string[]) {
  const ran = Bun.spawnSync(argv, { cwd, env, stdout: "pipe", stderr: "pipe" });
  return { status: ran.exitCode, stdout: ran.stdout.toString(), stderr: ran.stderr.toString() };
}

test("build in a label without the three test files exits 2, naming each missing test", () => {
  requireHostTools("bash");
  const { label, script, env } = bareLabel();

  const built = run(label, env, "bash", script, "build");

  expect(built.status).toBe(2);
  for (const [pkg, file, name] of tests) {
    expect(built.stderr).toContain(`packages/daemon/internal/${pkg}/${file}`);
    expect(built.stderr).toContain(name);
  }
});

// A test binary exits 0 when -test.run selects nothing, printing a warning and PASS. The label
// below holds the three test files, and its built world binary is such a binary: a planted
// executable that prints what a Go test binary prints when it selects no test, so the row needs
// no Go toolchain and controls exactly that output.
test("world refuses a binary that ran no test, naming the missing PASS line", () => {
  requireHostTools("bash", "git");
  const { label, script, env } = bareLabel();
  for (const [pkg, file] of tests) {
    const dir = path.join(label, "packages", "daemon", "internal", pkg);
    mkdirSync(dir, { recursive: true });
    writeFileSync(path.join(dir, file), `package ${pkg}\n`);
  }
  const identity = ["-c", "user.name=Label Test", "-c", "user.email=label@example.invalid"];
  const git = (...args: string[]) => {
    const ran = run(label, env, "git", ...identity, ...args);
    if (ran.status !== 0) throw new Error(`git ${args.join(" ")}: ${ran.status} ${ran.stderr}`);
    return ran.stdout.trim();
  };
  git("-c", "init.defaultBranch=main", "init", "-q");
  git("add", "-A");
  git("commit", "-qm", "label");
  const bin = path.join(label, ".rig-texts", git("rev-parse", "HEAD"));
  mkdirSync(bin, { recursive: true });
  const binary = path.join(bin, "workflow.test");
  writeFileSync(binary, "#!/bin/sh\necho 'testing: warning: no tests to run'\necho PASS\nexit 0\n");
  chmodSync(binary, 0o755);
  const work = directory("run");
  const request = path.join(work, "world-request.json");
  const answer = path.join(work, "world-answer.json");
  writeFileSync(request, "{}\n");

  const world = run(label, env, "bash", script, "world", request, answer);

  expect(world.stderr).toContain("testing: warning: no tests to run");
  expect(world.stderr).toContain("--- PASS: TestRigWorld");
  expect(world.status).toBe(1);
});
