import { beforeAll, describe, expect, setDefaultTimeout, test } from "bun:test";
import {
  appendFileSync,
  chmodSync,
  cpSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { skipWithout } from "@legion/test-fixtures/host-tools";
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";
import { scriptFunctions } from "../../../../scripts/e2e/lib/script-functions";

// Every test here runs rig.sh and its stand-ins as real processes. On a loaded machine one test
// takes longer than bun's 5 s default (the eight-turn conversation took 11 s), so each test gets
// one minute.
setDefaultTimeout(60_000);

// The judge runs in a Docker container; every other row here needs no container. Read once, so
// one warning names docker for the whole file.
const withoutDocker = skipWithout("docker");

// rig.sh's install_model_route, run with the checkout's real installers: the route a label's
// profile reaches Anthropic by, picked by LEGION_E2E_MODEL_ROUTE.
const root = path.resolve(import.meta.dir, "../../../..");
const fn = scriptFunctions(path.join(import.meta.dir, "rig.sh"));
const work = temporaryDirectory("skill-scenarios-rig");
const operatorHome = path.join(work, "operator-home");
// The home the probes must not reach, with the one thing they look for in it: a judge that could
// read it would list this directory. It is staged in a hook, below the one the reservation
// registered: a run that selects no row of this file makes neither. Each level is made on its
// own, never with `recursive`, so this hook makes nothing above the reserved root and a root the
// fixture did not make is an error here rather than a directory nothing removes.
beforeAll(() => {
  mkdirSync(operatorHome);
  mkdirSync(path.join(operatorHome, ".ssh"));
  writeFileSync(path.join(operatorHome, ".ssh", "id_ed25519"), "not a key\n", { mode: 0o600 });
});
/** The variables every child process of this file gets: the caller's PATH, a HOME of the test's
 * own, and the caller's mise data and config directories when the caller names them. A tool on
 * PATH can be a mise shim (jq and jj are, on a devbox). Under a fresh HOME such a shim finds
 * neither its installs nor its global config, so it fails. make_omp_home keeps the operator's two
 * directories for the processes it starts for the same reason, and base_env passes MISE_DATA_DIR
 * to every agent. A caller that names neither variable gets neither: the developer's own home
 * decides nothing here. PUBLIC_LEAK_CHECK and LEGION_LEAK_CHECK_TAG go through when the caller
 * has them: a row that builds a world through worker_fixture pushes into its bare remote, and on a
 * Legion pod that push goes through the pod's git wrapper, whose pre-push leak guard reads both. */
const miseData =
  process.env.MISE_DATA_DIR ??
  (process.env.XDG_DATA_HOME === undefined
    ? undefined
    : path.join(process.env.XDG_DATA_HOME, "mise"));
const childEnv = {
  PATH: process.env.PATH ?? "",
  HOME: operatorHome,
  ...(miseData === undefined ? {} : { MISE_DATA_DIR: miseData }),
  ...(process.env.XDG_CONFIG_HOME === undefined
    ? {}
    : { XDG_CONFIG_HOME: process.env.XDG_CONFIG_HOME }),
  ...(process.env.PUBLIC_LEAK_CHECK === undefined
    ? {}
    : { PUBLIC_LEAK_CHECK: process.env.PUBLIC_LEAK_CHECK }),
  ...(process.env.LEGION_LEAK_CHECK_TAG === undefined
    ? {}
    : { LEGION_LEAK_CHECK_TAG: process.env.LEGION_LEAK_CHECK_TAG }),
};

let profiles = 0;
/** A label's profile directory with an empty Oh My Pi home, as make_omp_home leaves it. */
function profileDir(): string {
  const dir = path.join(work, `profile-${++profiles}`);
  mkdirSync(path.join(dir, "home", ".omp"), { recursive: true });
  return dir;
}
function keyFile(mode: number): string {
  const file = path.join(work, `key-${profiles}-${mode.toString(8)}`);
  writeFileSync(file, "sk-ant-rig-test-0000000000000000\n", { mode });
  return file;
}
function route(dir: string, env: Record<string, string>, model = "") {
  const result = Bun.spawnSync(
    [
      "bash",
      "-c",
      `route_model_id=claude-opus-4-8\n${fn("install_model_route")}\n${fn("install_provider_route")}\ninstall_model_route "$1" skills-test "$2" "$3"`,
      "-",
      root,
      dir,
      model,
    ],
    { env: { ...childEnv, ...env } }
  );
  return { code: result.exitCode, stderr: result.stderr.toString() };
}

describe("install_model_route", () => {
  test("key-file writes the profile's route, its key command under model-key and its record under key", () => {
    const dir = profileDir();
    const { code, stderr } = route(dir, {
      LEGION_E2E_MODEL_ROUTE: "key-file",
      LEGION_E2E_ANTHROPIC_KEY_FILE: keyFile(0o600),
    });
    expect({ code, stderr: code === 0 ? "" : stderr }).toEqual({ code: 0, stderr: "" });
    const models = readFileSync(
      path.join(dir, "home/.omp/profiles/skills-test/agent/models.yml"),
      "utf8"
    );
    expect(models).toContain(`!exec ${path.join(dir, "model-key", "anthropic-key")}`);
    expect(models).toContain("https://api.anthropic.com");
    const served = Bun.spawnSync([path.join(dir, "model-key", "anthropic-key")], { env: childEnv });
    expect(served.stdout.toString().trim()).toBe("sk-ant-rig-test-0000000000000000");
    expect(existsSync(path.join(dir, "key", "anthropic-key.log"))).toBe(true);
  });

  test("key-file without LEGION_E2E_ANTHROPIC_KEY_FILE fails, naming the variable", () => {
    const dir = profileDir();
    const { code, stderr } = route(dir, { LEGION_E2E_MODEL_ROUTE: "key-file" });
    expect(code).toBe(1);
    expect(stderr).toContain("LEGION_E2E_ANTHROPIC_KEY_FILE is unset");
    expect(existsSync(path.join(dir, "home/.omp/profiles/skills-test/agent/models.yml"))).toBe(
      false
    );
  });

  test("a key file others can read is refused, and no key command is left behind", () => {
    const dir = profileDir();
    const { code, stderr } = route(dir, {
      LEGION_E2E_MODEL_ROUTE: "key-file",
      LEGION_E2E_ANTHROPIC_KEY_FILE: keyFile(0o644),
    });
    expect(code).not.toBe(0);
    expect(stderr).toContain("group or other can read it");
    expect(existsSync(path.join(dir, "model-key"))).toBe(false);
  });

  /** An operator's models.yml holding the gateway block the provider route names, and another. */
  function providerFile(apiKey: string): string {
    const file = path.join(work, `models-${profiles}.yml`);
    writeFileSync(
      file,
      [
        "providers:",
        "  openai:",
        '    apiKey: "!secrets get --value OPENAI_KEY"',
        "  box-gateway:",
        "    baseUrl: http://gateway.internal.example:4000",
        `    apiKey: "${apiKey}"`,
        "    api: anthropic-messages",
        "    authHeader: true",
        "    models:",
        "      - id: claude-opus-5-5",
        "        thinking:",
        "          mode: anthropic-adaptive",
        "          efforts: [low, high]",
        "",
      ].join("\n")
    );
    return file;
  }

  test("provider copies the named block, its !cat key command as written, with the rig's one model", () => {
    const dir = profileDir();
    const { code, stderr } = route(dir, {
      LEGION_E2E_MODEL_ROUTE: "provider",
      LEGION_E2E_MODEL_PROVIDER_FILE: providerFile("!cat /run/user/1000/box/gateway-key"),
      LEGION_E2E_MODEL_PROVIDER: "box-gateway",
    });
    expect({ code, stderr: code === 0 ? "" : stderr }).toEqual({ code: 0, stderr: "" });
    const agent = path.join(dir, "home/.omp/profiles/skills-test/agent");
    expect(JSON.parse(readFileSync(path.join(agent, "models.yml"), "utf8"))).toEqual({
      providers: {
        "box-gateway": {
          baseUrl: "http://gateway.internal.example:4000",
          apiKey: "!cat /run/user/1000/box/gateway-key",
          api: "anthropic-messages",
          authHeader: true,
          models: [{ id: "claude-opus-4-8" }],
        },
      },
    });
    const config = readFileSync(path.join(agent, "config.yml"), "utf8");
    expect(config).toContain("enabledModels:\n  - box-gateway/claude-opus-4-8\n");
    expect(config).toContain("  - anthropic\n");
    expect(config).toContain("  default: box-gateway/claude-opus-4-8\n");
    expect(config).toContain("  task: box-gateway/claude-opus-4-8\n");
    expect(existsSync(path.join(dir, "model-key"))).toBe(false);
  });

  test("provider with a model the block lists takes the block's own entry for it", () => {
    const dir = profileDir();
    const { code, stderr } = route(
      dir,
      {
        LEGION_E2E_MODEL_ROUTE: "provider",
        LEGION_E2E_MODEL_PROVIDER_FILE: providerFile("!cat /run/user/1000/box/gateway-key"),
        LEGION_E2E_MODEL_PROVIDER: "box-gateway",
      },
      "claude-opus-5-5"
    );
    expect({ code, stderr: code === 0 ? "" : stderr }).toEqual({ code: 0, stderr: "" });
    const agent = path.join(dir, "home/.omp/profiles/skills-test/agent");
    expect(
      JSON.parse(readFileSync(path.join(agent, "models.yml"), "utf8")).providers["box-gateway"]
        .models
    ).toEqual([
      { id: "claude-opus-5-5", thinking: { mode: "anthropic-adaptive", efforts: ["low", "high"] } },
    ]);
    expect(readFileSync(path.join(agent, "config.yml"), "utf8")).toContain(
      "  default: box-gateway/claude-opus-5-5\n"
    );
  });

  test("provider without LEGION_E2E_MODEL_PROVIDER_FILE fails, naming the variable", () => {
    const dir = profileDir();
    const { code, stderr } = route(dir, {
      LEGION_E2E_MODEL_ROUTE: "provider",
      LEGION_E2E_MODEL_PROVIDER: "box-gateway",
    });
    expect(code).toBe(1);
    expect(stderr).toContain("LEGION_E2E_MODEL_PROVIDER_FILE is unset");
  });

  test("provider refuses a block absent from the file, and one whose key is not read by !cat", () => {
    const missing = route(profileDir(), {
      LEGION_E2E_MODEL_ROUTE: "provider",
      LEGION_E2E_MODEL_PROVIDER_FILE: providerFile("!cat /run/user/1000/box/gateway-key"),
      LEGION_E2E_MODEL_PROVIDER: "elsewhere",
    });
    expect(missing.code).toBe(1);
    expect(missing.stderr).toContain("has no provider block elsewhere");
    const literal = profileDir();
    const inline = route(literal, {
      LEGION_E2E_MODEL_ROUTE: "provider",
      LEGION_E2E_MODEL_PROVIDER_FILE: providerFile("sk-literal-key"),
      LEGION_E2E_MODEL_PROVIDER: "box-gateway",
    });
    expect(inline.code).toBe(1);
    expect(inline.stderr).toContain('does not read its key as "!cat <absolute file>"');
    expect(existsSync(path.join(literal, "home/.omp/profiles/skills-test/agent/models.yml"))).toBe(
      false
    );
  });

  test("an unknown route fails, naming the three it takes", () => {
    const dir = profileDir();
    const { code, stderr } = route(dir, { LEGION_E2E_MODEL_ROUTE: "bedrock" });
    expect(code).toBe(1);
    expect(stderr).toContain("it is gateway, key-file or provider");
  });
});

/** A label's checkout whose plugin installer and home maker do nothing, with the real key-file
 * route. */
function labelCheckout(): string {
  const co = freshDir("route-checkout");
  mkdirSync(path.join(co, "scripts/e2e/lib"), { recursive: true });
  mkdirSync(path.join(co, "skills/asd-ste100"), { recursive: true });
  writeFileSync(path.join(co, "skills/asd-ste100/SKILL.md"), "# STE\n");
  writeFileSync(path.join(co, ".omp-pin"), "omp@pin\n");
  // The real key-file route, with the libs it sources: model-endpoint.sh for its base URL's
  // check, and omp-home.sh for the line that shuts the setup wizard, whose make_omp_home is then
  // replaced by one that only makes the home's .omp.
  for (const file of ["install-key-file-model-route.sh", "model-endpoint.sh", "omp-home.sh"]) {
    cpSync(path.join(root, "scripts/e2e/lib", file), path.join(co, "scripts/e2e/lib", file));
  }
  appendFileSync(
    path.join(co, "scripts/e2e/lib/omp-home.sh"),
    'make_omp_home() { mkdir -p "$1/.omp"; }\n'
  );
  writeFileSync(path.join(co, "scripts/e2e/lib/install-plugin-profile.sh"), "exit 0\n");
  return co;
}
const routeEnv = () => ({
  LEGION_E2E_MODEL_ROUTE: "key-file",
  LEGION_E2E_ANTHROPIC_KEY_FILE: keyFile(0o600),
});

// The key-file route's key command reads the operator's key for as long as it exists
// (install-key-file-model-route.sh: the caller removes --key-dir on every exit that keeps no
// profile). A setup that stops after the route is installed leaves no profile to use it.
describe("a setup that fails after install_model_route", () => {
  // Catches: a label profile that fails after its route is installed (here the superpowers probe)
  // and keeps a key command that still reads the operator's key.
  test("cmd_profile removes the label's model-key and marks nothing ready", () => {
    const work = freshDir("route-work");
    const script = [
      "set -euo pipefail",
      ...[
        "fail",
        "label_dir",
        "on_exit",
        "drop_unready_keys",
        "install_model_route",
        "given_omp",
        "cmd_profile",
      ].map(fn),
      "checkout_commit() { printf 'fixture parents=base\\n'; }",
      "install_superpowers() { fail 'the superpowers probe failed'; }",
      'work=$1; cmd_profile probe "$2"',
    ].join("\n");
    const failed = bash(script, [work, labelCheckout()], {
      ...routeEnv(),
      SKILL_SCENARIOS_SUPERPOWERS: "set",
    });
    expect(failed.code).toBe(1);
    expect(failed.stderr).toContain("the superpowers probe failed");
    const P = path.join(work, "profiles/probe");
    expect(existsSync(path.join(P, "home/.omp/profiles/skills-probe/agent/models.yml"))).toBe(true);
    expect(existsSync(path.join(P, "model-key"))).toBe(false);
    expect(existsSync(path.join(P, "ready"))).toBe(false);

    // The same setup that succeeds keeps its key command: a ready profile uses it.
    const kept = freshDir("route-work");
    const ok = bash(
      script.replace("fail 'the superpowers probe failed'", ":"),
      [kept, labelCheckout()],
      {
        ...routeEnv(),
        SKILL_SCENARIOS_SUPERPOWERS: "set",
      }
    );
    expect({ code: ok.code, stderr: ok.stderr }).toEqual({ code: 0, stderr: "" });
    expect(existsSync(path.join(kept, "profiles/probe/model-key/anthropic-key"))).toBe(true);
    expect(existsSync(path.join(kept, "profiles/probe/ready"))).toBe(true);
  });

  // Catches: a judge-profile build that fails at its second profile and keeps the first one's key
  // command.
  test("judge_profiles removes both profiles' model-key and marks nothing ready", () => {
    const work = freshDir("judge-route-work");
    const co = labelCheckout();
    const script = [
      "set -euo pipefail",
      ...["fail", "on_exit", "drop_unready_keys", "install_model_route", "judge_profiles"].map(fn),
      "checkout_commit() { printf 'fixture parents=base\\n'; }",
      "root=$1; work=$2; judge_model=fixture; judge_thinking=high; judge_profiles",
    ].join("\n");
    // The bare profile's home cannot be made, after the ste profile's route is installed.
    appendFileSync(
      path.join(co, "scripts/e2e/lib/omp-home.sh"),
      'make_omp_home() { [ "$(basename "$(dirname "$1")")" != bare ] || { echo "no home for bare" >&2; return 1; }; mkdir -p "$1/.omp"; }\n'
    );
    const failed = bash(script, [co, work], routeEnv());
    expect(failed.code).toBe(1);
    expect(failed.stderr).toContain("make_omp_home failed for the judge's bare profile");
    const dir = path.join(work, "judge-profiles");
    expect(existsSync(path.join(dir, "ste/home/.omp/profiles/judge-ste/agent/models.yml"))).toBe(
      true
    );
    expect(existsSync(path.join(dir, "ste/model-key"))).toBe(false);
    expect(existsSync(path.join(dir, "bare/model-key"))).toBe(false);
    expect(existsSync(path.join(dir, "ready"))).toBe(false);
  });
});

// cmd_profile hands both plugin installs the Oh My Pi the label runs: .omp-pin's, through mise, or
// the binary SKILL_SCENARIOS_OMP names, which install-plugin-profile.sh takes as --omp-binary alone
// (its --omp is a mise tool spec, whatever it looks like).
describe("cmd_profile's Oh My Pi", () => {
  /** cmd_profile of the label `probe` under `env`, with an installer that records its arguments. */
  function profile(env: Record<string, string>) {
    const co = labelCheckout();
    const installs = path.join(co, "installs");
    writeFileSync(
      path.join(co, "scripts/e2e/lib/install-plugin-profile.sh"),
      `printf '%s\\n' "$*" >>${JSON.stringify(installs)}\n`
    );
    const work = freshDir("omp-profile-work");
    const script = [
      "set -euo pipefail",
      ...[
        "fail",
        "label_dir",
        "on_exit",
        "drop_unready_keys",
        "install_model_route",
        "given_omp",
        "cmd_profile",
      ].map(fn),
      "checkout_commit() { printf 'fixture parents=base\\n'; }",
      'work=$1; cmd_profile probe "$2"',
    ].join("\n");
    const result = bash(script, [work, co], { ...routeEnv(), ...env });
    const P = path.join(work, "profiles/probe");
    return {
      code: result.code,
      stderr: result.stderr,
      installs: readFileSync(installs, "utf8").trimEnd().split("\n"),
      flags: ["pi-envoy", "pi-legion"].map(
        (pkg) => `--package ${pkg} --profile skills-probe --home ${P}/home --dest ${P}/${pkg}`
      ),
    };
  }

  // Catches: the binary handed on as --omp, which the installer reads as a mise tool spec, so a
  // pod with no mise fails at `mise install <path>`.
  test("a set SKILL_SCENARIOS_OMP goes to both installs as --omp-binary, and an unset one as nothing", () => {
    const omp = path.join(freshDir("omp-given"), "omp");
    writeFileSync(omp, "#!/bin/sh\n", { mode: 0o755 });
    const given = profile({ SKILL_SCENARIOS_OMP: omp });
    expect({ code: given.code, stderr: given.stderr }).toEqual({ code: 0, stderr: "" });
    expect(given.installs).toEqual(given.flags.map((flags) => `${flags} --omp-binary ${omp}`));
    const pinned = profile({});
    expect({ code: pinned.code, stderr: pinned.stderr }).toEqual({ code: 0, stderr: "" });
    expect(pinned.installs).toEqual(pinned.flags);
  });
});

/** Runs SCRIPT (rig.sh functions and a call of them) under bash with ARGS as $1..., in an
 * environment of PATH and the test's own HOME. */
function bash(script: string, args: string[] = [], env: Record<string, string> = {}) {
  const result = Bun.spawnSync(["bash", "-c", script, "-", ...args], {
    env: { ...childEnv, ...env },
  });
  return {
    code: result.exitCode,
    stdout: result.stdout.toString(),
    stderr: result.stderr.toString(),
  };
}
let dirs = 0;
/** A new directory under the test's work directory. */
function freshDir(name: string): string {
  const dir = path.join(work, `${name}-${++dirs}`);
  mkdirSync(dir, { recursive: true });
  return dir;
}

// cmd_profile records the label's commit in built-from with checkout_commit. A git worktree holds
// no .jj, and jj alone printed an empty commit line for it.
describe("checkout_commit", () => {
  const git = (dir: string, ...args: string[]) => {
    const result = Bun.spawnSync(
      [
        "git",
        "-C",
        dir,
        "-c",
        "user.name=Rig Test",
        "-c",
        "user.email=rig@example.invalid",
        "-c",
        "commit.gpgsign=false",
        ...args,
      ],
      { env: childEnv }
    );
    if (result.exitCode !== 0) throw new Error(`git ${args.join(" ")}: ${result.stderr}`);
    return result.stdout.toString().trim();
  };
  function gitCheckout() {
    const dir = freshDir("git-checkout");
    git(dir, "init", "-q", "-b", "main");
    writeFileSync(path.join(dir, "a.txt"), "a\n");
    git(dir, "add", "-A");
    git(dir, "commit", "-qm", "first");
    const first = git(dir, "rev-parse", "HEAD");
    writeFileSync(path.join(dir, "b.txt"), "b\n");
    git(dir, "add", "-A");
    git(dir, "commit", "-qm", "second");
    return { dir, first, head: git(dir, "rev-parse", "HEAD") };
  }
  const commit = (dir: string) => bash(`${fn("checkout_commit")}\ncheckout_commit "$1"`, [dir]);

  // Catches: a checkout_commit that reads jj alone (an empty line for git), or names no parents.
  test("a clean git checkout prints its HEAD and HEAD's parents", () => {
    const { dir, first, head } = gitCheckout();
    expect(commit(dir)).toEqual({ code: 0, stdout: `${head} parents=${first}\n`, stderr: "" });
  });

  // Catches: a built-from that names a commit the label's files do not match.
  test("a git checkout with a change git status lists is marked dirty", () => {
    const { dir, first, head } = gitCheckout();
    writeFileSync(path.join(dir, "a.txt"), "changed\n");
    expect(commit(dir).stdout).toBe(`${head} parents=${first} dirty\n`);
  });

  // Catches: a checkout_commit that hides a failed read, which cmd_profile would record as an
  // empty commit line.
  test("a directory that is no checkout fails", () => {
    expect(commit(freshDir("no-checkout")).code).not.toBe(0);
  });
});

// cmd_live_read reads every path skill_reads prints. asd-ste100 holds examples/ beside
// references/, and a read of SKILL.md and references/ alone left examples/ unread.
describe("skill_reads", () => {
  function checkout() {
    const co = freshDir("skills-checkout");
    const skill = path.join(co, "skills", "demo");
    for (const file of [
      "SKILL.md",
      "references/rules.md",
      "examples/before-after.md",
      "examples/data.json",
      "LICENSE",
    ]) {
      mkdirSync(path.dirname(path.join(skill, file)), { recursive: true });
      writeFileSync(path.join(skill, file), `${file}\n`);
    }
    return co;
  }
  const reads = (co: string, skill: string) =>
    bash(`${fn("fail")}\n${fn("skill_reads")}\nskill_reads "$1" "$2"`, [co, skill]);

  // Catches: a list that leaves examples/ out, or reads a file that is not markdown.
  test("SKILL.md first, then every other markdown file in byte order", () => {
    expect(reads(checkout(), "demo")).toEqual({
      code: 0,
      stdout: [
        "skill://demo",
        "skill://demo/examples/before-after.md",
        "skill://demo/references/rules.md",
        "",
      ].join("\n"),
      stderr: "",
    });
  });

  // Catches: a live read of a skill the checkout does not have, which reads nothing and passes.
  test("a skill the checkout lacks fails, naming it", () => {
    const co = checkout();
    const { code, stderr } = reads(co, "absent");
    expect(code).toBe(1);
    expect(stderr).toContain(`${co} has no skill absent`);
  });
});

// Oh My Pi walks a run's cwd's ancestors for a project `.omp`, past the run's own HOME. Under a
// work directory below the operator's HOME, a live read loaded the operator's plugins from
// $HOME/.omp in place of the label's packed ones, and nothing scored that as a failure.
describe("omp_ancestor", () => {
  const ancestor = (dir: string) => bash(`${fn("omp_ancestor")}\nomp_ancestor "$1"`, [dir]);

  // Catches: a check that misses a `.omp` above the work directory, the measured case.
  test("names the nearest ancestor that holds .omp", () => {
    const top = freshDir("omp-above");
    mkdirSync(path.join(top, ".omp"));
    const work = path.join(top, "a", "work");
    expect(ancestor(work)).toEqual({ code: 0, stdout: `${top}/.omp\n`, stderr: "" });
  });

  // Catches: a check that starts at the parent and so misses the work directory's own `.omp`.
  test("the work directory's own .omp counts", () => {
    const work = freshDir("omp-self");
    writeFileSync(path.join(work, ".omp"), "");
    expect(ancestor(work).stdout).toBe(`${work}/.omp\n`);
  });

  // Catches: a check that refuses every work directory.
  test("prints nothing and fails when no ancestor holds .omp", () => {
    expect(ancestor(path.join(freshDir("omp-none"), "work"))).toEqual({
      code: 1,
      stdout: "",
      stderr: "",
    });
  });

  // Catches: a rig.sh that defines the check and never runs it.
  test("rig.sh refuses such a work directory before it makes it, naming the .omp", () => {
    const top = freshDir("omp-rig");
    mkdirSync(path.join(top, ".omp"));
    const work = path.join(top, "work");
    const result = Bun.spawnSync(["bash", path.join(import.meta.dir, "rig.sh"), "score"], {
      env: { ...childEnv, SKILL_SCENARIOS_WORK: work },
    });
    expect(result.exitCode).toBe(1);
    expect(result.stderr.toString()).toContain(`${top}/.omp`);
    expect(existsSync(work)).toBe(false);
  });
});

// A spec-writing or spec-rewrite batch takes one run, numbered one above the highest run of its
// scenario and label, so a later batch never removes an earlier run (cmd_run starts from rm -rf).
describe("next_run", () => {
  const next = (work: string, scenario: string, label: string) =>
    bash(`${fn("next_run")}\nwork=$1\nnext_run "$2" "$3"`, [work, scenario, label]);

  // Catches: numbering that starts over at 1 and so overwrites run 1, or that counts directories
  // instead of reading their numbers.
  test("one above the highest number of the scenario and label", () => {
    const work = freshDir("numbering");
    for (const run of [
      "spec-writing-s1-1",
      "spec-writing-s1-10",
      "spec-writing-s1-x-4",
      "spec-writing-s1-2-7",
      "spec-rewrite-s1-30",
      "spec-writing-main-40",
    ])
      mkdirSync(path.join(work, "runs", run), { recursive: true });
    writeFileSync(path.join(work, "runs", "spec-writing-s1-99"), "");
    expect(next(work, "spec-writing", "s1")).toEqual({ code: 0, stdout: "11\n", stderr: "" });
  });

  // Catches: a first batch that fails because $work/runs does not exist yet.
  test("1 when the label has no run of the scenario", () => {
    expect(next(freshDir("numbering-empty"), "spec-rewrite", "s1").stdout).toBe("1\n");
  });
});

// cmd_batch and cmd_run read SKILL_SCENARIOS_SOURCE through source_dir before they start anything.
describe("source_dir", () => {
  function work(files: string[]) {
    const dir = freshDir("sources");
    mkdirSync(path.join(dir, "sources", "acme-1"), { recursive: true });
    for (const file of files) writeFileSync(path.join(dir, "sources", "acme-1", file), "x\n");
    return dir;
  }
  const sourceDir = (dir: string, scenario: string, name: string) =>
    bash(`${fn("fail")}\n${fn("source_dir")}\nwork=$1\nsource_dir "$2" "$3"`, [
      dir,
      scenario,
      name,
    ]);
  const full = ["title.txt", "messages.md", "asks.json"];

  // Catches: a spec-rewrite run seeded from a source with no old spec, which seed.ts would refuse
  // only after the batch started its services.
  test("spec-rewrite refuses a source without old-spec.md, naming it", () => {
    const dir = work(full);
    const { code, stderr } = sourceDir(dir, "spec-rewrite", "acme-1");
    expect(code).toBe(1);
    expect(stderr).toContain(`the source ${dir}/sources/acme-1 has no old-spec.md`);
  });

  // Catches: a spec-writing run refused for want of an old spec it never reads.
  test("spec-writing takes the same source, and prints its directory", () => {
    const dir = work(full);
    expect(sourceDir(dir, "spec-writing", "acme-1")).toEqual({
      code: 0,
      stdout: `${dir}/sources/acme-1\n`,
      stderr: "",
    });
    expect(sourceDir(work([...full, "old-spec.md"]), "spec-rewrite", "acme-1").code).toBe(0);
  });

  // Catches: a run that starts on a source missing a file seed.ts reads, or a name that leaves
  // $work/sources.
  test("refuses an unset name, a path, and a source missing a file", () => {
    const dir = work(["title.txt", "asks.json"]);
    expect(sourceDir(dir, "spec-writing", "").stderr).toContain("SKILL_SCENARIOS_SOURCE is unset");
    expect(sourceDir(dir, "spec-writing", "../sources/acme-1").stderr).toContain(
      "is not a directory name"
    );
    expect(sourceDir(dir, "spec-writing", "acme-1").stderr).toContain("has no messages.md");
  });
});

// The conversation of a spec run: rig.sh's spec_turns with stand-ins for omp (launch), seed.ts
// (seed_ts) and the judge's person stand-in (judge_ts). Each stand-in logs its call, and the
// seed.ts state stand-in hands back the state the test scripted for that turn.
describe("spec_turns", () => {
  interface Turn {
    asks?: number;
    approval?: boolean;
  }
  function conversation(
    turns: Turn[],
    options: { secondTranscript?: boolean; failAt?: number } = {}
  ) {
    const run = freshDir("spec-run");
    const states = freshDir("spec-states");
    mkdirSync(path.join(run, "sessions"));
    turns.forEach((turn, index) => {
      const t = index + 1;
      writeFileSync(
        path.join(states, `state-${t}.json`),
        JSON.stringify({
          turn: t,
          open_asks: Array.from({ length: turn.asks ?? 0 }, (_, k) => ({
            id: `ask-${t}-${k}`,
            question: "Q?",
            options: [],
            multiple: false,
          })),
          approval: turn.approval ? { ask_id: `approval-${t}`, version: t } : null,
        })
      );
    });
    const standIns = `
launch() {
  printf 'launch %s | %s\\n' "$*" "$(<"$R/prompt.txt")" >>"$R/calls"
  [ -n "\${SECOND:-}" ] && [ $# -gt 1 ] && : >"$R/sessions/fresh.jsonl"
  printf x >>"$R/launches"
  status=0
  [ "$(wc -c <"$R/launches")" != "\${FAIL_AT:-0}" ] || status=1
  printf 'output of %s\\nexit=%s\\n' "$*" "$status" >"$R/out.txt"
  printf '{}\\n' >>"$R/sessions/run.jsonl"
}
seed_ts() {
  printf 'seed %s\\n' "$*" >>"$R/calls"
  [ "$1" != state ] || cp "$STATES/state-$3.json" "$R/state-$3.json"
}
judge_ts() {
  printf 'judge %s\\n' "$*" >>"$R/calls"
  jq -c '.open_asks[] | {ask: .id, selected: [], text: "t"}' "$R/state-$3.json" >"$R/answers-$3.jsonl"
}`;
    const result = bash(
      `${fn("note")}\n${fn("fail")}\n${fn("spec_turns")}\n${standIns}\nR=$1\nspec_turns spec-writing-s1-1 LWEVAL-2 "Design it."`,
      [run],
      {
        STATES: states,
        ...(options.secondTranscript ? { SECOND: "1" } : {}),
        ...(options.failAt === undefined ? {} : { FAIL_AT: String(options.failAt) }),
      }
    );
    const read = (name: string) =>
      existsSync(path.join(run, name)) ? readFileSync(path.join(run, name), "utf8") : "";
    const calls = read("calls")
      .trimEnd()
      .split("\n")
      .map((line) => line.replaceAll(run, "<R>"));
    return { ...result, run, read, calls };
  }
  const answered = "The person answered your asks on LWEVAL-2.";
  const approved = "The person approved the spec of LWEVAL-2.";

  // Catches: a loop that answers before it reads the state, continues without --continue (a second
  // session), skips the stand-in, or sends another prompt than the three the rig.sh header gives.
  test("asks, then an approval: answer, continue, approve, continue once more, stop", () => {
    const { code, calls, read } = conversation([{ asks: 2 }, { approval: true }, {}]);
    expect(code).toBe(0);
    expect(calls).toEqual([
      "launch spec-writing-s1-1 | Design it.",
      "seed state <R>/fixture.json 1 <R>",
      "judge answer <R> 1",
      "seed answer <R>/fixture.json <R>/answers-1.jsonl",
      `launch spec-writing-s1-1 --continue | ${answered}`,
      "seed state <R>/fixture.json 2 <R>",
      "seed approve <R>/fixture.json",
      `launch spec-writing-s1-1 --continue | ${approved}`,
      "seed state <R>/fixture.json 3 <R>",
    ]);
    expect(read("turns.txt")).toBe("1 first exit=0\n2 answered exit=0\n3 approved exit=0\n");
    expect(read("answers.jsonl")).toBe(read("answers-1.jsonl"));
    expect(read("out-2.txt")).toBe("output of spec-writing-s1-1 --continue\nexit=0\n");
  });

  // Catches: a loop that answers the open asks when an approval request is open too (the rig.sh
  // header's order puts the approval first).
  test("an open approval request goes before open asks", () => {
    const { calls } = conversation([{ asks: 1, approval: true }, {}]);
    expect(calls.slice(1, 3)).toEqual([
      "seed state <R>/fixture.json 1 <R>",
      "seed approve <R>/fixture.json",
    ]);
    expect(calls.some((call) => call.startsWith("judge"))).toBe(false);
  });

  // Catches: a loop that continues a session with nothing for it to answer.
  test("no open ask and no approval request ends the run after its turn", () => {
    const { code, calls, read } = conversation([{}]);
    expect(code).toBe(0);
    expect(calls).toEqual([
      "launch spec-writing-s1-1 | Design it.",
      "seed state <R>/fixture.json 1 <R>",
    ]);
    expect(read("turns.txt")).toBe("1 first exit=0\n");
  });

  // Catches: a run past 8 turns, or an answer after the eighth turn that no turn reads.
  test("stops after 8 turns, answering nothing after the eighth", () => {
    const { calls, read } = conversation(Array.from({ length: 9 }, () => ({ asks: 1 })));
    expect(calls.filter((call) => call.startsWith("launch"))).toHaveLength(8);
    expect(calls.filter((call) => call.startsWith("judge"))).toHaveLength(7);
    expect(calls.at(-1)).toBe("seed state <R>/fixture.json 8 <R>");
    expect(read("turns.txt").trimEnd().split("\n")).toHaveLength(8);
  });

  // Catches: a --continue that starts a second transcript beside the first, which every reader of
  // the run would then read as one of two.
  test("a later turn that leaves a second transcript fails the run, naming its sessions", () => {
    const { code, stderr, run } = conversation([{ asks: 1 }, {}], { secondTranscript: true });
    expect(code).toBe(1);
    expect(stderr).toContain(`after turn 2, ${run}/sessions holds 2 transcripts`);
  });

  // Catches: a loop that answers and continues a session whose turn failed or timed out.
  test("a turn that does not exit 0 ends the run after its state", () => {
    const { code, calls, read } = conversation([{ asks: 1 }, { asks: 1 }, {}], { failAt: 2 });
    expect(code).toBe(0);
    expect(calls.at(-1)).toBe("seed state <R>/fixture.json 2 <R>");
    expect(calls.filter((call) => call.startsWith("judge"))).toHaveLength(1);
    expect(read("turns.txt")).toBe("1 first exit=0\n2 answered exit=1\n");
  });
});

// cmd_profile's superpowers step, with a stand-in mise: it records the environment omp got and
// writes a transcript whose one tool result is the text in result.txt beside it.
describe("install_superpowers", () => {
  function setup(result: string) {
    const bin = freshDir("superpowers-bin");
    const label = freshDir("superpowers-label");
    const src = freshDir("superpowers-src");
    for (const skill of ["writing-plans", "brainstorming"]) {
      mkdirSync(path.join(src, "skills", skill), { recursive: true });
      writeFileSync(
        path.join(src, "skills", skill, "SKILL.md"),
        `---\nname: ${skill}\n---\n\n# Skill ${skill}\n`
      );
    }
    writeFileSync(path.join(src, "package.json"), '{"version":"9.9.9"}\n');
    writeFileSync(path.join(label, "built-from"), "checkout /x\n");
    // The transcript line the stand-in omp leaves, made here: the stand-in runs under the probe's
    // own `env -i`, where a jq on PATH may be a mise shim that cannot run.
    writeFileSync(
      path.join(bin, "probe.jsonl"),
      `${JSON.stringify({ message: { role: "toolResult", content: [{ type: "text", text: result }] } })}\n`
    );
    // `mise where` names the pin's install, whose bin/omp the probe runs by path; any other call is
    // the probe's `mise x <pin> -- <omp> -p …`.
    mkdirSync(path.join(bin, "pin", "bin"), { recursive: true });
    writeFileSync(path.join(bin, "pin", "bin", "omp"), "#!/bin/sh\n", { mode: 0o755 });
    writeFileSync(
      path.join(bin, "mise"),
      `#!/usr/bin/env bash
if [ "$1" = where ]; then echo "$(dirname "$0")/pin"; exit 0; fi
printf '%s\\n' "$@" >"$(dirname "$0")/argv.txt"
env >"$(dirname "$0")/env.txt"
while [ $# -gt 0 ] && [ "$1" != --session-dir ]; do shift; done
mkdir -p "$2"
cp "$(dirname "$0")/probe.jsonl" "$2/probe.jsonl"
echo done
`,
      { mode: 0o755 }
    );
    const run = () =>
      bash(
        `${fn("fail")}\n${fn("given_omp")}\n${fn("pinned_command")}\n${fn("install_superpowers")}\nsuperpowers_skills=(writing-plans brainstorming)\ninstall_superpowers "$1" skills-s1 omp@pin`,
        [label],
        { PATH: `${bin}:${childEnv.PATH}`, SKILL_SCENARIOS_SUPERPOWERS: src, USER: "rig" }
      );
    return { bin, label, run };
  }

  // Catches: a profile step that copies the skills somewhere the pinned Oh My Pi does not read, or
  // gives the probe the operator's Envoy (the label's pi-envoy would register with it).
  test("copies both skills into the profile, probes with no Envoy, and marks the label", () => {
    const { bin, label, run } = setup("# Skill writing-plans\n\nbody");
    expect(run()).toMatchObject({ code: 0, stderr: "" });
    const skills = path.join(label, "home/.omp/profiles/skills-s1/agent/skills");
    expect(readFileSync(path.join(skills, "brainstorming/SKILL.md"), "utf8")).toContain(
      "# Skill brainstorming"
    );
    expect(existsSync(path.join(skills, "writing-plans/SKILL.md"))).toBe(true);
    expect(existsSync(path.join(label, "superpowers"))).toBe(true);
    // Each variable is read whole, by name: a substring would also match a longer name such as
    // X_OMP_PROFILE, and env can print any variable first.
    const env = new Map(
      readFileSync(path.join(bin, "env.txt"), "utf8")
        .split("\n")
        .flatMap((line): [string, string][] => {
          const eq = line.indexOf("=");
          return eq > 0 ? [[line.slice(0, eq), line.slice(eq + 1)]] : [];
        })
    );
    expect(env.get("ENVOY_URL")).toBe("http://127.0.0.1:1");
    expect(env.get("OMP_PROFILE")).toBe("skills-s1");
    expect(env.get("HOME")).toBe(`${label}/home`);
    expect(readFileSync(path.join(label, "built-from"), "utf8")).toContain(
      "superpowers-skill writing-plans "
    );
    // The probe names the pin's own binary, so no omp earlier on PATH stands in for it.
    expect(readFileSync(path.join(bin, "argv.txt"), "utf8")).toStartWith(
      `x\nomp@pin\n--\n${bin}/pin/bin/omp\n-p\n`
    );
  });

  // Catches: a probe that passes when Oh My Pi did not load the skill.
  test("fails when the read answers Unknown skill, and marks nothing", () => {
    const { label, run } = setup("Unknown skill: writing-plans\nAvailable: dispatch");
    const { code, stderr } = run();
    expect(code).toBe(1);
    expect(stderr).toContain("does not load skill://writing-plans (Unknown skill)");
    expect(existsSync(path.join(label, "superpowers"))).toBe(false);
  });

  // Catches: a probe that passes when the agent read nothing at all.
  test("fails when no tool result holds the skill's heading", () => {
    const { label, run } = setup("some other file");
    const { code, stderr } = run();
    expect(code).toBe(1);
    expect(stderr).toContain("read no skill://writing-plans");
    expect(existsSync(path.join(label, "superpowers"))).toBe(false);
  });
});

// rig.sh judge-omp, the whole script, in its Docker container (Docker is on the devbox and on CI's
// ubuntu-24.04 runners), with the real key-file route and a stand-in omp that records its argv and
// environment. SKILL_SCENARIOS_JUDGE_OMP puts the stand-in where the pinned omp's binary is
// mounted, so no test needs a model. The checkout's own skills/asd-ste100 is the ste skill.
describe("judge-omp", () => {
  // Every row that reaches the container needs Docker, so a machine without it (a Legion
  // worker's pod) skips those rows and the rest of the file still runs. GitHub Actions is the
  // exception, as in extensions/controller-start-omp.test.ts: its ubuntu-24.04 runners have
  // Docker, and a runner that lost it must fail these rows rather than quietly skip them.
  // The bad-argument row is not skipped: judge-omp refuses its arguments before it looks for
  // Docker, so it runs anywhere.
  const onActions = process.env.GITHUB_ACTIONS === "true";
  const skip = withoutDocker && !onActions;
  // Staged in a hook: Bun runs this body even where the rows do not run, and a write here would
  // make the suite's work directory and leave it behind.
  let work = "";
  let outside = "";
  let omp = "";
  beforeAll(() => {
    work = freshDir("judge-work");
    outside = path.join(work, "outside.txt");
    writeFileSync(outside, "a file beside the judge's directory\n");
    mkdirSync(path.join(work, "runs"), { recursive: true });
    omp = path.join(freshDir("judge-omp-bin"), "omp");
    // The stand-in writes everything through the one file the judge may write, answer.json: argv
    // (one word a line), then `env` lines, then one line per probe (`<name> allowed|denied`), then
    // one `mode <name> <bits>` line per key path, which only a judge holding the key can read.
    writeFileSync(
      omp,
      `#!/bin/bash
probe() { if "\${@:2}" >/dev/null 2>&1; then echo "probe $1 allowed"; else echo "probe $1 denied"; fi; }
# DROP: write a marker into every directory this judge can write outside its own directory.
# SEEK: report any marker another judge left anywhere this judge can read.
if [ -n "\${DROP:-}" ]; then
  find / \\( -path /proc -o -path /sys -o -path /dev -o -path "$PWD" \\) -prune -o -type d -writable -print 2>/dev/null |
    while IFS= read -r d; do printf 'private input\\n' >"$d/leak-$DROP" 2>/dev/null; done
fi
{
  printf 'argv %s\\n' "$0" "$@"
  printf 'env %s\\n' "PWD=$PWD" "HOME=$HOME" "OMP_PROFILE=$OMP_PROFILE"
  compgen -e | grep -q '^LEGION_E2E' && echo "env LEGION_E2E set"
  probe read-outside cat "$OUTSIDE"
  probe read-home-ssh ls "$OPERATOR_HOME/.ssh"
  probe list-work ls "$WORK/runs"
  probe read-own cat "$PWD/prompt.txt"
  probe write-own-dir touch "$PWD/new.txt"
  probe write-sessions touch "$PWD/sessions/s.jsonl"
  probe read-skill cat "$HOME/.omp/profiles/$OMP_PROFILE/agent/skills/asd-ste100/SKILL.md"
  probe read-models cat "$HOME/.omp/profiles/$OMP_PROFILE/agent/models.yml"
  key=$(sed -n 's/.*!exec \\(.*\\)"$/\\1/p' "$HOME/.omp/profiles/$OMP_PROFILE/agent/models.yml" | head -1)
  probe key-command test -x "$key"
  probe gateway-base-url grep -q 'baseUrl: https://gateway.internal.example/anthropic' "$HOME/.omp/profiles/$OMP_PROFILE/agent/models.yml"
  probe token-is-jwt bash -c '[[ "$("$1")" =~ ^eyJ[A-Za-z0-9_-]*\\.[A-Za-z0-9_-]+\\.[A-Za-z0-9_-]+$ ]]' - "$key"
  probe dbus-set test -n "\${DBUS_SESSION_BUS_ADDRESS:-}"
  printf 'mode %s\\n' "key-dir $(stat -c %a /opt/key 2>/dev/null || echo none)" \\
    "token $(stat -c %a /opt/key/token 2>/dev/null || echo none)"
  [ -z "\${SEEK:-}" ] || echo "seek $(find / \\( -path /proc -o -path /sys -o -path /dev \\) -prune -o -name 'leak-*' -print 2>/dev/null | head -1)"
} >"$PWD/answer.json"
echo judged
exit "$(cat "$PWD/exit-status" 2>/dev/null || echo 0)"
`,
      { mode: 0o755 }
    );
  });
  const judge = (
    kind: string,
    dir: string,
    output = "answer.json",
    env: Record<string, string> = {}
  ) =>
    Bun.spawnSync(["bash", path.join(import.meta.dir, "rig.sh"), "judge-omp", kind, dir, output], {
      env: {
        ...childEnv,
        USER: "rig",
        SKILL_SCENARIOS_WORK: work,
        SKILL_SCENARIOS_JUDGE_OMP: omp,
        SKILL_SCENARIOS_JUDGE_ENV: `OUTSIDE=${outside} OPERATOR_HOME=${operatorHome} WORK=${work}`,
        LEGION_E2E_MODEL_ROUTE: "key-file",
        LEGION_E2E_ANTHROPIC_KEY_FILE: keyFile(0o600),
        ...env,
      },
    });
  let jobs = 0;
  function judgeDir(): string {
    const dir = path.join(work, "judging", `job-${++jobs}`);
    mkdirSync(dir, { recursive: true });
    writeFileSync(path.join(dir, "prompt.txt"), "Write answer.json.\n");
    return dir;
  }
  /** The stand-in's record of one kind (`argv`, `env`, `probe`), from the judge's answer file. */
  const recorded = (dir: string, kind: string) =>
    readFileSync(path.join(dir, "answer.json"), "utf8")
      .split("\n")
      .filter((line) => line.startsWith(`${kind} `))
      .map((line) => line.slice(kind.length + 1));
  /**
   * A judge's exit code, and whether the rig refused: any line of its stderr starting
   * `skill-scenarios: `, the prefix note() and fail() give every word of the rig's own. Equality
   * against the whole stderr string read a byte from docker, getent, mise or flock as a failed
   * row; what a row is about is that the judge ran and the rig refused nothing. A result that is
   * not clean carries the stderr that was read, so a failing row still names what went wrong; a
   * clean result reports no stderr.
   */
  const judged = (result: { exitCode: number; stderr: Buffer }) => {
    const stderr = result.stderr.toString();
    const refused = stderr.split("\n").some((line) => line.startsWith("skill-scenarios: "));
    const clean = !refused && result.exitCode === 0;
    return { code: result.exitCode, refused, stderr: clean ? "" : stderr };
  };
  /** What `judged` reads back from a judge that ran and a rig that refused nothing. */
  const judgedCleanly = { code: 0, refused: false, stderr: "" };

  // Catches: a judge session with the label's plugins, the operator's skills or the default model,
  // run outside its own directory, or with no record of its model.
  test.skipIf(skip)(
    "ste runs omp in the directory on its prompt, no plugins, asd-ste100 alone, the pinned model",
    () => {
      const dir = judgeDir();
      const result = judge("ste", dir);
      expect(judged(result)).toEqual(judgedCleanly);
      expect(recorded(dir, "argv")).toEqual([
        "/opt/omp/omp",
        "-p",
        "Write answer.json.",
        "--no-extensions",
        "--skills",
        "asd-ste100",
        "--model",
        "anthropic/claude-opus-4-8",
        "--thinking",
        "high",
        "--session-dir",
        `${dir}/sessions`,
      ]);
      expect(recorded(dir, "env")).toEqual([
        `PWD=${dir}`,
        "HOME=/home/judge",
        "OMP_PROFILE=judge-ste",
      ]);
      expect(readFileSync(path.join(dir, "out.txt"), "utf8")).toBe("judged\nexit=0\n");
      expect(readFileSync(path.join(dir, "judge-model.txt"), "utf8")).toBe(
        "model anthropic/claude-opus-4-8\nthinking high\n"
      );
      const skill = path.join(work, "judge-profiles/ste/home/.omp/profiles/judge-ste/agent/skills");
      expect(readFileSync(path.join(skill, "asd-ste100/SKILL.md"), "utf8")).toBe(
        readFileSync(path.join(root, "skills/asd-ste100/SKILL.md"), "utf8")
      );
      expect(
        readFileSync(
          path.join(work, "judge-profiles/ste/home/.omp/profiles/judge-ste/agent/models.yml"),
          "utf8"
        )
      ).toContain("https://api.anthropic.com");
    }
  );

  // Catches: a judge that reads the run's transcript, other runs or the operator's files beside its
  // own directory (as a findings judge did, given no check), or that writes anything but its one
  // answer file and its sessions.
  test.skipIf(skip)(
    "a judge sees only its own directory, read-only, with its answer file and sessions writable",
    () => {
      const dir = judgeDir();
      expect(judge("ste", dir).exitCode).toBe(0);
      expect(Object.fromEntries(recorded(dir, "probe").map((line) => line.split(" ")))).toEqual({
        "read-outside": "denied",
        "read-home-ssh": "denied",
        "list-work": "denied",
        "read-own": "allowed",
        "write-own-dir": "denied",
        "write-sessions": "allowed",
        "read-skill": "allowed",
        "read-models": "allowed",
        "key-command": "allowed",
        "gateway-base-url": "denied",
        "token-is-jwt": "denied",
        "dbus-set": "denied",
      });
      expect(existsSync(path.join(dir, "new.txt"))).toBe(false);
    }
  );

  // Catches: a bare judge that still loads skills, or a judge-omp that hides omp's failure.
  test.skipIf(skip)("bare loads no skill, and judge-omp exits with omp's status", () => {
    const dir = judgeDir();
    writeFileSync(path.join(dir, "exit-status"), "3\n");
    const result = judge("bare", dir);
    expect(result.exitCode).toBe(3);
    expect(recorded(dir, "argv")).toContain("--no-skills");
    expect(recorded(dir, "argv")).not.toContain("--skills");
    expect(recorded(dir, "env")).toContain("OMP_PROFILE=judge-bare");
    expect(recorded(dir, "probe")).toContain("read-skill denied");
    expect(readFileSync(path.join(dir, "out.txt"), "utf8")).toBe("judged\nexit=3\n");
    expect(
      existsSync(path.join(work, "judge-profiles/bare/home/.omp/profiles/judge-bare/agent/skills"))
    ).toBe(false);
  });

  // Catches: a mount two judges share that one can write and the next can read, such as the key
  // command's call record, a path one judge could leave its private input in for another to read.
  // The first judge writes a marker into every directory it can write outside its own directory;
  // the second, of the same kind, looks for one anywhere it can read.
  test.skipIf(skip)("one judge cannot leave a file that a second judge can read", () => {
    const first = judgeDir();
    const env = (word: string) => ({
      SKILL_SCENARIOS_JUDGE_ENV: `OUTSIDE=${outside} OPERATOR_HOME=${operatorHome} WORK=${work} ${word}`,
    });
    expect(judge("ste", first, "answer.json", env("DROP=one")).exitCode).toBe(0);
    const second = judgeDir();
    expect(judge("ste", second, "answer.json", env("SEEK=1")).exitCode).toBe(0);
    expect(recorded(second, "seek")).toEqual([""]);
  });

  // Catches: a judge kind the rig has no profile for, a directory with no prompt, and an answer
  // file name that escapes the judge's directory or names another of its files.
  test("refuses another kind, a directory with no prompt.txt and a bad answer name", () => {
    expect(judge("plugins", judgeDir()).stderr.toString()).toContain("is ste or bare");
    expect(judge("ste", freshDir("judging-empty")).stderr.toString()).toContain(
      "has no prompt.txt"
    );
    for (const output of ["../answer.json", "sessions", "prompt.txt", "a/b.json", ""]) {
      const result = judge("ste", judgeDir(), output);
      expect(result.exitCode).not.toBe(0);
      expect(result.stderr.toString()).toContain("is not the name of an answer file");
    }
  });

  // The gateway route with the real installer and key command, and a stand-in hawk-token first on
  // PATH that mints a JWT and counts each mint. The key command needs the operator's home and
  // session bus, so it runs on the host; the container gets only the token it printed.
  describe("on the gateway route", () => {
    // Staged in a hook, for the reason the describe above gives.
    let gatewayWork = "";
    let fakeBin = "";
    let mints = "";
    beforeAll(() => {
      gatewayWork = freshDir("judge-gateway-work");
      mkdirSync(path.join(gatewayWork, "runs"), { recursive: true });
      fakeBin = freshDir("judge-gateway-bin");
      mints = path.join(gatewayWork, "mints.log");
      writeFileSync(
        path.join(fakeBin, "hawk-token"),
        `#!/usr/bin/env bash
printf '%s\\n' "$$" >>"$FAKE_MINT_LOG"
payload=$(printf '{"exp":%s}' "$(($(date +%s) + 3600))" | base64 -w0 | tr '+/' '-_' | tr -d '=')
printf 'eyJhbGciOiJub25lIn0.%s.c2ln\\n' "$payload"
`,
        { mode: 0o755 }
      );
    });
    const gatewayJudge = (kind: string, dir: string, env: Record<string, string> = {}) =>
      judge(kind, dir, "answer.json", {
        PATH: `${fakeBin}:${childEnv.PATH}`,
        SKILL_SCENARIOS_WORK: gatewayWork,
        SKILL_SCENARIOS_JUDGE_ENV: `OUTSIDE=${outside} OPERATOR_HOME=${operatorHome} WORK=${gatewayWork}`,
        LEGION_E2E_MODEL_ROUTE: "gateway",
        LEGION_E2E_MODEL_GATEWAY_URL: "https://gateway.internal.example/anthropic",
        DBUS_SESSION_BUS_ADDRESS: "unix:path=/nonexistent/bus",
        FAKE_MINT_LOG: mints,
        ...env,
      });
    const mintCount = () =>
      existsSync(mints) ? readFileSync(mints, "utf8").split("\n").filter(Boolean).length : 0;
    function gatewayDir(): string {
      const dir = path.join(gatewayWork, "judging", `job-${++jobs}`);
      mkdirSync(dir, { recursive: true });
      writeFileSync(path.join(dir, "prompt.txt"), "Write answer.json.\n");
      return dir;
    }

    // Catches: a gateway judge that cannot start in its container, one that gets the operator's
    // home or session bus, one whose models.yml still names the host key command (which the
    // container cannot run), one that mints more than once per judge run, a token the judge can
    // read only by a path outside its mounts, and a token left on disk once the judge ends.
    test.skipIf(skip)(
      "a gateway judge starts in the container on a token the host minted once, and the token file is gone after",
      () => {
        // The first judge-omp call builds both profiles, and each install's preflight mints once.
        // Count only a later call's mints: one judge run is one call of the key command.
        expect(gatewayJudge("bare", gatewayDir()).exitCode).toBe(0);
        const dir = gatewayDir();
        const before = mintCount();
        const result = gatewayJudge("ste", dir);
        expect(judged(result)).toEqual(judgedCleanly);
        expect(mintCount() - before).toBeLessThanOrEqual(1);
        expect(recorded(dir, "env")).toEqual([
          `PWD=${dir}`,
          "HOME=/home/judge",
          "OMP_PROFILE=judge-ste",
        ]);
        expect(Object.fromEntries(recorded(dir, "probe").map((line) => line.split(" ")))).toEqual({
          "read-outside": "denied",
          "read-home-ssh": "denied",
          "list-work": "denied",
          "read-own": "allowed",
          "write-own-dir": "denied",
          "write-sessions": "allowed",
          "read-skill": "allowed",
          "read-models": "allowed",
          "key-command": "allowed",
          "gateway-base-url": "allowed",
          "token-is-jwt": "allowed",
          "dbus-set": "denied",
        });
        expect(existsSync(`${dir}.key`)).toBe(false);
        expect(readFileSync(path.join(dir, "answer.json"), "utf8")).not.toMatch(/eyJ[\w-]*\./);
      }
    );

    // Catches: a token directory or file that others can read while the judge runs. The modes
    // are the judge's own reading of them from inside the container, recorded while it ran
    // (`recorded(dir, "mode")`), rather than a watcher's from outside: the stand-in omp sleeps
    // for nothing now, so there is no window for a watcher to look through, and a row that
    // waited for one would be waiting on a sleep.
    test.skipIf(skip)("the token file is the owner's alone while the judge runs", () => {
      const dir = gatewayDir();
      const result = gatewayJudge("bare", dir);
      expect(judged(result)).toEqual(judgedCleanly);
      expect(recorded(dir, "mode")).toEqual(["key-dir 700", "token 600"]);
      expect(existsSync(`${dir}.key`)).toBe(false);
    });

    // Catches: a token file left behind when the judge fails, and a failed mint that starts the
    // judge anyway.
    test.skipIf(skip)(
      "removes the token file when the judge fails, and starts no judge when the key command fails",
      () => {
        const dir = gatewayDir();
        writeFileSync(path.join(dir, "exit-status"), "3\n");
        expect(gatewayJudge("bare", dir).exitCode).toBe(3);
        expect(existsSync(`${dir}.key`)).toBe(false);
        const refused = gatewayDir();
        const broken = freshDir("judge-gateway-broken");
        writeFileSync(path.join(broken, "hawk-token"), "#!/usr/bin/env bash\nexit 1\n", {
          mode: 0o755,
        });
        rmSync(path.join(gatewayWork, "judge-profiles/cache"), { recursive: true, force: true });
        for (const kind of ["ste", "bare"])
          rmSync(path.join(gatewayWork, `judge-profiles/${kind}/cache`), {
            recursive: true,
            force: true,
          });
        const result = gatewayJudge("ste", refused, { PATH: `${broken}:${childEnv.PATH}` });
        expect(result.exitCode).toBe(1);
        expect(result.stderr.toString()).toContain(
          "judge-omp: the gateway's key command gave no key"
        );
        expect(existsSync(path.join(refused, "answer.json"))).toBe(false);
      }
    );
  });

  // Catches: a judge profile whose built-from records an empty commit line when the rig's root is
  // no checkout, so a judge's model run cannot be traced to the code that built it.
  test.skipIf(skip)("fails, naming the rig's root, when the root is no checkout", () => {
    const top = freshDir("judge-root");
    const scripts = path.join(top, "packages/pi-legion/scripts/skill-scenarios");
    mkdirSync(scripts, { recursive: true });
    writeFileSync(path.join(scripts, "rig.sh"), readFileSync(path.join(import.meta.dir, "rig.sh")));
    for (const file of [".omp-pin", "skills/asd-ste100/SKILL.md"]) {
      mkdirSync(path.dirname(path.join(top, file)), { recursive: true });
      writeFileSync(path.join(top, file), readFileSync(path.join(root, file)));
    }
    mkdirSync(path.join(top, "scripts/e2e"), { recursive: true });
    cpSync(path.join(root, "scripts/e2e/lib"), path.join(top, "scripts/e2e/lib"), {
      recursive: true,
    });
    const rootWork = freshDir("judge-root-work");
    const result = Bun.spawnSync(
      ["bash", path.join(scripts, "rig.sh"), "judge-omp", "ste", judgeDir(), "answer.json"],
      {
        env: {
          ...childEnv,
          USER: "rig",
          SKILL_SCENARIOS_WORK: rootWork,
          SKILL_SCENARIOS_JUDGE_OMP: omp,
          LEGION_E2E_MODEL_ROUTE: "key-file",
          LEGION_E2E_ANTHROPIC_KEY_FILE: keyFile(0o600),
          GIT_CEILING_DIRECTORIES: path.dirname(top),
        },
      }
    );
    expect(result.exitCode).toBe(1);
    expect(result.stderr.toString()).toContain(`cannot read the commit of ${top} with jj or git`);
    expect(existsSync(path.join(rootWork, "judge-profiles/ready"))).toBe(false);
  });
});

// A spec run's plan row reads skill://writing-plans, which only a label built with
// SKILL_SCENARIOS_SUPERPOWERS holds (cmd_profile marks it with a `superpowers` file).
describe("require_superpowers", () => {
  const check = (label: string) =>
    bash(`${fn("fail")}\n${fn("require_superpowers")}\nP=$1\nrequire_superpowers spec-writing`, [
      label,
    ]);

  // Catches: a spec run on a label with no writing-plans, whose plan row would then fail for the
  // rig's fault and not the skill's.
  test("refuses a label built without SKILL_SCENARIOS_SUPERPOWERS, naming the variable", () => {
    const label = freshDir("label");
    const { code, stderr } = check(label);
    expect(code).toBe(1);
    expect(stderr).toContain("SKILL_SCENARIOS_SUPERPOWERS");
    writeFileSync(path.join(label, "superpowers"), "");
    expect(check(label).code).toBe(0);
  });
});

// launch with a stand-in tmux that starts the pane's command a moment later, as a real tmux server
// does, a stand-in start_process that starts it at once in the background, both recording each
// call, and a stand-in mise that prints the omp command line it was given.
describe("launch", () => {
  const standIns = `
tmux() {
  printf 'tmux %s\\n' "$1" >>"$R/calls"
  case $1 in new-session) (command sleep 0.3 && bash -c "\${@: -1}") & ;; esac
}
start_process() {
  printf 'start_process %s\\n' "$1" >>"$R/calls"
  shift
  "$@" &
  agent_pid=$!
}
stop_tree() { :; }
sleep() { command sleep 0.05; }`;
  /** One later turn, `launch spec-writing-s1-1 --continue`, in a run directory of its own: the
   * pane's out.txt as its wait read it, and the tmux and start_process calls it made. `tmux` puts
   * a tmux binary on PATH, which is what launch looks for: the row's own tmux function takes
   * every call either way. With none, PATH holds only the tools launch and its pane run, so a tmux
   * elsewhere on the machine is not on it. `given` sets SKILL_SCENARIOS_OMP to an omp of the run's
   * own, which prints its path and its arguments. */
  function laterTurn({ tmux, given }: { tmux: boolean; given: boolean }) {
    const run = freshDir("launch-run");
    const bin = freshDir("launch-bin");
    let PATH = `${bin}:${childEnv.PATH}`;
    if (tmux) {
      writeFileSync(path.join(bin, "tmux"), '#!/bin/sh\necho "tmux ran: $*" >&2\nexit 1\n', {
        mode: 0o755,
      });
    } else {
      const tools = freshDir("launch-tools");
      for (const tool of ["bash", "cat", "chmod", "cp", "env", "grep", "sleep", "tail"]) {
        const found = Bun.which(tool);
        if (found === null) throw new Error(`launch's row needs ${tool} on PATH`);
        symlinkSync(found, path.join(tools, tool));
      }
      PATH = `${bin}:${tools}`;
    }
    for (const dir of ["cwd", "sessions"]) mkdirSync(path.join(run, dir));
    writeFileSync(path.join(run, "prompt.txt"), "The person answered your asks on LWEVAL-2.\n");
    writeFileSync(path.join(run, "pane.env"), `PATH=${PATH}\n`);
    writeFileSync(path.join(run, "out.txt"), "the first turn\nexit=0\n");
    mkdirSync(path.join(bin, "pin", "bin"), { recursive: true });
    writeFileSync(path.join(bin, "pin", "bin", "omp"), "#!/bin/sh\n", { mode: 0o755 });
    writeFileSync(
      path.join(bin, "mise"),
      `#!/bin/sh\nif [ "$1" = where ]; then echo "${bin}/pin"; exit 0; fi\nshift 3\necho "$@"\n`,
      { mode: 0o755 }
    );
    const omp = path.join(bin, "given", "omp");
    mkdirSync(path.dirname(omp));
    writeFileSync(omp, '#!/bin/sh\necho "$0 $*"\n', { mode: 0o755 });
    const result = bash(
      `${fn("fail")}\n${fn("given_omp")}\n${fn("pinned_command")}\n${fn("launch")}\n${standIns}\nR=$1 tag=t pin=omp@pin\nlaunch spec-writing-s1-1 --continue\ncp "$R/out.txt" "$R/seen"`,
      [run],
      given ? { PATH, SKILL_SCENARIOS_OMP: omp } : { PATH }
    );
    return {
      run,
      bin,
      omp,
      code: result.code,
      stderr: result.stderr,
      seen: existsSync(path.join(run, "seen")) ? readFileSync(path.join(run, "seen"), "utf8") : "",
      calls: readFileSync(path.join(run, "calls"), "utf8").trimEnd().split("\n"),
    };
  }
  const prompt = "-p The person answered your asks on LWEVAL-2. --continue --session-dir";
  const inTmux = ["tmux kill-session", "tmux new-session", "tmux kill-session"];

  // Catches: a later turn that reads the previous turn's `exit=` line as its own and returns at
  // once, and extra omp arguments placed anywhere but after the prompt.
  test("a later turn passes its arguments to omp and waits for its own exit line", () => {
    const { run, bin, code, seen } = laterTurn({ tmux: true, given: false });
    expect(code).toBe(0);
    expect(seen).toBe(
      `${bin}/pin/bin/omp -p The person answered your asks on LWEVAL-2. --continue --session-dir ${run}/sessions\nexit=0\n`
    );
  });

  // Catches: SKILL_SCENARIOS_OMP deciding whether tmux runs as well as which omp does, so a box
  // that has tmux and tries an Oh My Pi build of its own loses its panes.
  test("with tmux on PATH, a set SKILL_SCENARIOS_OMP still runs in a tmux session, as it is", () => {
    const turn = laterTurn({ tmux: true, given: true });
    expect({ code: turn.code, stderr: turn.stderr }).toEqual({ code: 0, stderr: "" });
    expect(turn.seen).toBe(`${turn.omp} ${prompt} ${turn.run}/sessions\nexit=0\n`);
    expect(turn.calls).toEqual(inTmux);
  });

  // Catches: a box with no tmux that sets no SKILL_SCENARIOS_OMP, where launch would start no
  // agent at all, and a pod with no tmux that sets it, where the agent would be the pin's omp
  // through a mise the pod does not carry.
  test.each([
    ["mise's install of the pin", false],
    ["the binary SKILL_SCENARIOS_OMP names", true],
  ] as const)("with no tmux on PATH, the agent is a process group of the run's own shell, whichever omp runs: %s", (_omp, given) => {
    const turn = laterTurn({ tmux: false, given });
    expect({ code: turn.code, stderr: turn.stderr }).toEqual({ code: 0, stderr: "" });
    const omp = given ? turn.omp : `${turn.bin}/pin/bin/omp`;
    expect(turn.seen).toBe(`${omp} ${prompt} ${turn.run}/sessions\nexit=0\n`);
    expect(turn.calls).toEqual(["start_process agent"]);
  });
});

// pinned_command: the command a label's agents run omp by, the binary SKILL_SCENARIOS_OMP names
// or mise's install of the pin, each refused in words of its own when it is not executable.
describe("pinned_command", () => {
  /** pinned_command of the pin omp@pin under `env`, and the command it set, one word a line. */
  const pinned = (env: Record<string, string>) =>
    bash(
      `${fn("fail")}\n${fn("given_omp")}\n${fn("pinned_command")}\npinned_command omp@pin\nprintf '%s\\n' "\${omp_command[@]}"`,
      [],
      env
    );

  // Catches: the two branches' failure texts run together, which read "the Oh My Pi binary
  // SKILL_SCENARIOS_OMP names/x/omp, is not executable".
  test("a SKILL_SCENARIOS_OMP that is not executable is refused by name, as is a pin's install", () => {
    const dir = freshDir("pinned");
    const given = path.join(dir, "omp");
    writeFileSync(given, "#!/bin/sh\n", { mode: 0o644 });
    expect(pinned({ SKILL_SCENARIOS_OMP: given })).toEqual({
      code: 1,
      stdout: "",
      stderr: `skill-scenarios: ${given}, the Oh My Pi binary SKILL_SCENARIOS_OMP names, is not executable\n`,
    });
    chmodSync(given, 0o755);
    expect(pinned({ SKILL_SCENARIOS_OMP: given })).toEqual({
      code: 0,
      stdout: `${given}\n`,
      stderr: "",
    });

    const bin = freshDir("pinned-bin");
    mkdirSync(path.join(bin, "pin", "bin"), { recursive: true });
    writeFileSync(path.join(bin, "pin", "bin", "omp"), "#!/bin/sh\n", { mode: 0o644 });
    writeFileSync(path.join(bin, "mise"), `#!/bin/sh\necho "${bin}/pin"\n`, { mode: 0o755 });
    expect(pinned({ PATH: `${bin}:${childEnv.PATH}` })).toEqual({
      code: 1,
      stdout: "",
      stderr: `skill-scenarios: ${bin}/pin/bin/omp, the Oh My Pi binary of the pin omp@pin, is not executable\n`,
    });
  });
});

// services_up and a run's start_dispatch, with every process they start stood in for: the Go
// toolchain (which records each build and writes the binary it names), the Postgres and NATS a
// batch was given, the listener, and run-server.sh, whose environment the row reads.
describe("the batch's Dispatch server build", () => {
  // Catches: a run whose Dispatch start compiles the server, as run-server.sh does when it is given
  // no binary: the first runs of a batch, compiling at once, overran the daemon stand-in's start
  // bound, then 30 s (trial-a-head-1 left an empty daemon.log).
  test("services_up builds the Dispatch server once, and each run's start_dispatch runs that build", () => {
    const work = freshDir("dispatch-build-work");
    const bin = freshDir("dispatch-build-bin");
    const builds = path.join(work, "builds");
    writeFileSync(
      path.join(bin, "go"),
      `#!/usr/bin/env bash
printf '%s %s\\n' "$PWD" "$*" >>${JSON.stringify(builds)}
while [ $# -gt 0 ]; do
  if [ "$1" = -o ]; then printf '#!/bin/sh\\n' >"$2" && chmod +x "$2"; fi
  shift
done
`,
      { mode: 0o755 }
    );
    const globals =
      "work=$1 root=$2 tag=t services_env=$1/services/env " +
      "services_postgres=$1/services/postgres-url services_dispatch=$1/services/dispatch-server";
    const up = bash(
      [
        ...["fail", "note", "safe_url", "given_services", "lock_services", "services_up"].map(fn),
        "until_true() { :; }",
        "pg_sql() { echo 100; }",
        "start_listener() { envoy_port=9020; }",
        globals,
        "lock_services",
        "services_up",
      ].join("\n"),
      [work, root],
      {
        PATH: `${bin}:${childEnv.PATH}`,
        SKILL_SCENARIOS_POSTGRES_URL: "postgres://rig@127.0.0.1:5432/postgres",
        SKILL_SCENARIOS_NATS_URL: "nats://127.0.0.1:4222",
        SKILL_SCENARIOS_PARALLEL: "1",
      }
    );
    expect({ code: up.code, stderr: up.stderr }).toEqual({ code: 0, stderr: "" });
    const envoy = path.join(root, "packages/envoy");
    expect(readFileSync(builds, "utf8")).toBe(
      `${envoy} build -o ${work}/services/envoy-listener ./cmd/listener\n` +
        `${envoy} build -o ${work}/services/dispatch-server ./cmd/dispatch\n`
    );

    // A run's start, in a process of its own as `rig.sh run` is: run-server.sh is given the build.
    const run = freshDir("dispatch-build-run");
    mkdirSync(path.join(run, "logs"));
    const start = bash(
      [
        fn("start_dispatch"),
        'start_process() { env >"$evidence/run-server.env"; dispatch_pid=1; }',
        "await_start() { :; }",
        "report_port() { echo 5555; }",
        globals,
        "evidence=$3",
        "start_dispatch postgres://rig@127.0.0.1:5432/run_x",
      ].join("\n"),
      [work, root, run]
    );
    expect({ code: start.code, stderr: start.stderr }).toEqual({ code: 0, stderr: "" });
    expect(readFileSync(path.join(run, "run-server.env"), "utf8").split("\n")).toContain(
      `DISPATCH_E2E_SERVER_BIN=${work}/services/dispatch-server`
    );
  });
});

// own_dispatch on a Postgres the batch was given (SKILL_SCENARIOS_POSTGRES_URL): the run's own
// database there is scripts/e2e/lib/loopback-dsn.sh's run_database_url, which reads the URL with
// libpq. The server's CREATE DATABASE, the Dispatch server and the seed are stood in for, so a row
// reads what the server was asked and the connection string the run's Dispatch was given.
const withoutPython = skipWithout("python3");
const withoutLibpq =
  !withoutPython &&
  Bun.spawnSync([
    "python3",
    "-c",
    'import ctypes.util, sys; sys.exit(0 if ctypes.util.find_library("pq") else 1)',
  ]).exitCode !== 0;
if (withoutLibpq) {
  console.warn(
    `Skipped the own_dispatch tests in ${import.meta.path}. run_database_url reads each URL with ` +
      `libpq, and ctypes.util.find_library("pq") finds none on this host; install libpq5.`
  );
}
describe.skipIf(withoutPython || withoutLibpq)(
  "own_dispatch on a Postgres the batch was given",
  () => {
    /** One run's own_dispatch on the server `url` names: the run's database, its exit, what the
     * server was asked (`sql`) and the connection string the run's Dispatch was given. */
    function ownDispatch(url: string) {
      const run = freshDir("own-dispatch");
      const result = bash(
        [
          fn("fail"),
          fn("safe_url"),
          fn("own_dispatch"),
          '. "$2/scripts/e2e/lib/loopback-dsn.sh"',
          'pg_sql() { printf \'%s\\n\' "$2" >>"$R/sql"; }',
          'start_dispatch() { printf \'%s\\n\' "$1" >"$R/database-url"; dispatch_port=5555; }',
          "bun() { :; }",
          "R=$1 root=$2 here=/rig services_postgres=$1/postgres-url",
          'printf \'%s\\n\' "$3" >"$services_postgres"',
          "own_dispatch project",
        ].join("\n"),
        [run, root, url]
      );
      const [sql, databaseUrl] = ["sql", "database-url"].map((file) =>
        existsSync(path.join(run, file)) ? readFileSync(path.join(run, file), "utf8") : ""
      );
      return {
        database: `run_${path.basename(run).replaceAll("-", "_")}`,
        code: result.code,
        stderr: result.stderr,
        sql,
        databaseUrl,
      };
    }

    // The URLs scripts/e2e/lib/loopback-dsn.test.ts pins run_database_url with (its first row is
    // the one a shell rewrite of the URL got wrong: a `?` inside the user name, which libpq reads
    // as part of it), each with the connection string the helper rebuilds for the run's database.
    // Catches: a run's Dispatch given a string a text join of the URL's parts would make, which
    // names another database, another user or another host.
    test.each([
      [
        "postgres://u?x:p@localhost:5433/db",
        "host='localhost' password='p' port='5433' user='u?x'",
      ],
      ["postgres://evil.example?x=@localhost", "host='localhost' user='evil.example?x='"],
      ["postgres://localhost/db?", "host='localhost'"],
      [
        "postgres://legion_test@127.0.0.1:5433/postgres?sslmode=disable",
        "host='127.0.0.1' port='5433' sslmode='disable' user='legion_test'",
      ],
      ["postgres://u@localhost:5432@10.1.2.3/db", "host='localhost' port='5432@10.1.2.3' user='u'"],
      ["postgres://evil.example?@localhost/db", "host='localhost' user='evil.example?'"],
      [
        "postgres://u@127.0.0.1:5433/postgres?dbname=other&sslmode=disable",
        "host='127.0.0.1' port='5433' sslmode='disable' user='u'",
      ],
    ])("%s gives the run's Dispatch its own database there", (url, rest) => {
      const { database, ...made } = ownDispatch(url);
      expect(made).toEqual({
        code: 0,
        stderr: "",
        sql: `CREATE DATABASE ${database}\n`,
        databaseUrl: `dbname='${database}' ${rest}\n`,
      });
    });

    // Catches: a run that makes its database on a server that is not this machine's, which the
    // run's Dispatch would refuse only after the database was made.
    test("a server that is not this machine's is refused by name, before any database is made", () => {
      expect(ownDispatch("postgres://legion_test:secret@db.prod.example/db")).toEqual({
        database: expect.any(String),
        code: 1,
        stderr:
          "skill-scenarios: SKILL_SCENARIOS_POSTGRES_URL must name 127.0.0.1, localhost or ::1: " +
          "libpq reads the host db.prod.example, and each run makes a database of its own there\n",
        sql: "",
        databaseUrl: "",
      });
    });
  }
);

// red-at-the-head (rig.sh's header, red_fixture, run_red_at_the_head, cmd_run and cmd_batch): the
// world's red at the head of worker_fixture's pull request, the one role booted on it, and the
// world a run's name and world.json carry. The scorer's half of the scenario is score.test.ts's,
// and the stand-in's sequencing is gh-standin.test.ts's.

/** The twelve worlds, as rig.sh's header lists them and its red_world knows them. */
const RED_WORLDS = ["a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l"] as const;
type RedWorld = (typeof RED_WORLDS)[number];
/** Each world's red check, the job that carries it, and what the head settles at once the re-run
 * is pressed (red_fixture). The reviewer holds the phase in i and j, the tester in the rest. In i
 * and j the head also carries `aggregate`, the aggregate check the base requires, red over the
 * failing lane and settled with it; world.json's `check` stays the lane. In g the red check is
 * that aggregate, and its failing lane is a red check run of its own beside it (`aggregate` names
 * the lane there). World c's check is named as its job is, as GitHub names a job's check run. */
const RED_WORLD: Record<
  RedWorld,
  { check: string; job: string; after: "green" | "red"; aggregate?: string }
> = {
  a: { check: "test (notify)", job: "test (notify)", after: "green" },
  b: { check: "test (notify)", job: "test (notify)", after: "red" },
  c: { check: "build (linux-x64)", job: "build (linux-x64)", after: "green" },
  d: { check: "test (widgets)", job: "test (widgets)", after: "red" },
  e: { check: "test (notify)", job: "test (notify)", after: "red" },
  f: { check: "title", job: "title", after: "red" },
  g: { check: "ci", job: "test (notify)", after: "green", aggregate: "test (notify)" },
  h: { check: "test (notify)", job: "test (notify)", after: "green" },
  i: { check: "test (notify)", job: "test (notify)", after: "green", aggregate: "ci" },
  j: { check: "test (notify)", job: "test (notify)", after: "red", aggregate: "ci" },
  k: { check: "test (notify)", job: "test (notify)", after: "green" },
  l: { check: "test (notify)", job: "test (notify)", after: "green" },
};

const RED_KEY = "LWEVAL-9";
const RED_REPO = "example/widgets";
const RED_PR = 1003;
const RED_TITLE = `feat: greet() (${RED_KEY})`;
/** red_fixture's ids for this pull request: the run that failed, its job, and the check runs —
 * lint, typecheck, the world's red, and the attempt the re-run makes. */
const RED_RUN_ID = 7_000_000 + RED_PR;
const RED_JOB_ID = 8_000_000 + RED_PR;
const RED_ID = { lint: 9_000_000 + RED_PR - 2, typecheck: 9_000_000 + RED_PR - 1 };
const RED_RED_ID = 9_000_000 + RED_PR;
/** The aggregate check of worlds i and j; the attempt the re-run makes of it is the next id. */
const RED_CI_ID = RED_RED_ID + 3;
const RED_RUN_URL = `https://github.com/${RED_REPO}/actions/runs/${RED_RUN_ID}`;
const ghStandin = path.join(import.meta.dir, "gh-standin.ts");

/** A run directory whose world is built as a run builds it: worker_fixture's green pull request
 * RED_PR, its repository and its routes, and, where `world` names one, red_fixture's red over it,
 * with the rig's own functions and jj stood in for; in `run` where the caller gives one. */
function builtWorld(world: string, run = freshDir(`built-${world}`)): string {
  const rig = readFileSync(path.join(import.meta.dir, "rig.sh"), "utf8");
  const workerFixture = rig.slice(
    rig.indexOf("\nworker_fixture() {\n") + 1,
    rig.indexOf("\n# worker_world INDEX START")
  );
  // The functions the two fixtures call: the shared route definitions, the routes of other
  // commits, the repository's snapshot, the worlds' facts, the route programs and gh's texts, the
  // model of the red and the routes of its runs, and the required set.
  const names = [
    "fail",
    "route_defs",
    "other_commit_routes",
    "repo_snapshot",
    "red_world_table",
    "red_world",
    "program_defs",
    "pr_list_program",
    "pr_view_program",
    "pulls_query_program",
    "run_list_program",
    "runs_query_program",
    "check_runs_program",
    "jobs_program",
    "run_view_program",
    "rerun_flags",
    "rerun_help",
    "rerun_usage",
    "view_usage",
    "red_defs",
    "red_model",
    "red_model_defs",
    "run_routes",
    "red_fixture",
    "required_set_fixture",
  ];
  const result = bash(
    [
      ...names.map((name) => fn(name)),
      workerFixture,
      "jj() { :; }",
      "R=$1 worker_key=$2 worker_repo=$3",
      'worker_fixture 3 1740000000 && { [ "$4" = green ] || red_fixture "$4"; }',
    ].join("\n"),
    [run, RED_KEY, RED_REPO, world]
  );
  if (result.code !== 0) throw new Error(`the world ${world} failed: ${result.stderr}`);
  return run;
}
/** A run directory whose fixtures.json and world.json red_fixture has turned red over `world`,
 * over the world worker_fixture really writes. */
const redFixture = (world: string) => builtWorld(world);
/** The routes of a run's fixtures.json, each by its match and its marks. */
type Route = { match: string; head_read?: string; repo_refusal?: boolean; publish?: unknown };
const routesOf = (run: string): Route[] =>
  JSON.parse(readFileSync(path.join(run, "fixtures.json"), "utf8")).routes;
/** The match of the one route of a run that `pick` takes. */
function routeOf(run: string, pick: (route: Route) => boolean): string {
  const found = routesOf(run).filter(pick);
  if (found.length !== 1) throw new Error(`${found.length} routes of ${run} answer the pick`);
  return (found[0] as Route).match;
}
/** The head and the base worker_fixture builds for pull request RED_PR, read from its world.json:
 * its commits are dated by the run's index and the batch's start, so every build makes these two.
 * Read at load, before the suite makes its temporary directory, so from a directory of its own. */
const { head: RED_HEAD, base_sha: RED_BASE }: { head: string; base_sha: string } = (() => {
  const heads = mkdtempSync(path.join(tmpdir(), "skill-scenarios-heads-"));
  try {
    return JSON.parse(readFileSync(path.join(builtWorld("green", heads), "world.json"), "utf8"));
  } finally {
    rmSync(heads, { recursive: true, force: true });
  }
})();

/** One `gh` call of the role's, through the stand-in rig.sh puts on its PATH. The environment is
 * the test's own, which names no Envoy, so a route that publishes records that it could not and
 * nothing leaves this machine; the row that reads a settlement names a listener of its own. Its
 * output is read byte for byte, a BOM at its start kept (a job log starts with one), where
 * Response's text() would drop it. */
async function gh(run: string, args: string[], env: Record<string, string> = {}) {
  const child = Bun.spawn(["bun", ghStandin, "gh", ...args], {
    env: { ...childEnv, SKILL_SCENARIO_RUN: run, ...env },
    stdout: "pipe",
    stderr: "pipe",
  });
  const read = async (stream: ReadableStream<Uint8Array>) =>
    new TextDecoder("utf-8", { ignoreBOM: true }).decode(await new Response(stream).arrayBuffer());
  const [stdout, stderr, exit] = await Promise.all([
    read(child.stdout),
    read(child.stderr),
    child.exited,
  ]);
  return { stdout, stderr, exit };
}
const outputLines = (stdout: string) => stdout.split("\n").filter((line) => line !== "");
/** What the stand-in recorded of each call of a run, in the order it answered them. */
function ghCalls(run: string): { argv: string[]; route: string | null; exit: number }[] {
  return readFileSync(path.join(run, "calls.jsonl"), "utf8")
    .split("\n")
    .filter((line) => line !== "")
    .map((line) => JSON.parse(line) as { argv: string[]; route: string | null; exit: number });
}

/** The query the page gives for the counted set at a named head (red-at-the-head.md), read from
 * the page as a role copies it, with the world's owner and repository, so an edit of the page
 * reaches every row that sends it. */
const RED_ROLLUP_QUERY = (() => {
  const page = readFileSync(
    path.join(root, "skills/legion-worker/references/red-at-the-head.md"),
    "utf8"
  );
  const call = /^gh api graphql --paginate -F oid=<head> -f query='([^']*)'$/m.exec(page);
  if (call === null) throw new Error("red-at-the-head.md gives no query for the counted set");
  return (call[1] as string).replace('"<owner>"', '"example"').replace('"<repo>"', '"widgets"');
})();
/** One GraphQL read of the counted set at the head, as the page sends it, with `jq` over the
 * answer. */
const rollupRead = (run: string, filter: string) =>
  gh(run, [
    "api",
    "graphql",
    "--paginate",
    "-F",
    `oid=${RED_HEAD}`,
    "-f",
    `query=${RED_ROLLUP_QUERY}`,
    "--jq",
    filter,
  ]);

/** The four reads of the head the page has a role make, each as a verdict per check: the three
 * reads of the counted set — the rollup of `gh pr view`, the rows of `gh pr checks` and the
 * GraphQL rollup of the head — and the check runs of the commit, which answer every run at it,
 * the latest id of a name being the one the counted set shows. */
async function readHead(run: string) {
  const rollup = await gh(run, [
    "pr",
    "view",
    String(RED_PR),
    "--json",
    "statusCheckRollup",
    "--jq",
    ".statusCheckRollup[] | [.name,.conclusion] | @tsv",
  ]);
  const graphql = await rollupRead(
    run,
    ".data.repository.object.statusCheckRollup.contexts.nodes[] | [.name,.conclusion] | @tsv"
  );
  const checks = await gh(run, ["pr", "checks", String(RED_PR)]);
  const runs = await gh(run, [
    "api",
    "--paginate",
    `repos/${RED_REPO}/commits/${RED_HEAD}/check-runs?filter=all&per_page=100`,
    "--jq",
    ".check_runs[] | [.id,.name,.conclusion] | @tsv",
  ]);
  const latest: Record<string, { id: number; verdict: string }> = {};
  for (const line of outputLines(runs.stdout)) {
    const [id, name, conclusion] = line.split("\t");
    const seen = latest[name];
    const entry = { id: Number(id), verdict: conclusion === "success" ? "pass" : "fail" };
    if (seen === undefined || seen.id < entry.id) latest[name] = entry;
  }
  const verdict = (stdout: string, pass: string) =>
    Object.fromEntries(
      outputLines(stdout).map((line) => {
        const [name, answer] = line.split("\t");
        return [name, answer === pass ? "pass" : "fail"];
      })
    );
  return {
    rollup: verdict(rollup.stdout, "SUCCESS"),
    graphql: verdict(graphql.stdout, "SUCCESS"),
    checks: verdict(checks.stdout, "pass"),
    checksExit: checks.exit,
    checkRuns: Object.fromEntries(
      Object.entries(latest).map(([name, { verdict: answer }]) => [name, answer])
    ),
    checkRunRows: outputLines(runs.stdout),
  };
}
/** The verdict the head carries under each check, before the re-run and after it. The aggregate
 * of worlds i and j carries the verdict of the lane under it, since the re-run settles both. */
function headIs(world: RedWorld, when: "before" | "after"): Record<string, string> {
  const { check, after, aggregate } = RED_WORLD[world];
  const other: Record<string, string> = { lint: "pass", typecheck: "pass" };
  // The spent head's e2e check: an earlier run of it failed, and the one that replaced it passed.
  if (world === "e") other.e2e = "pass";
  const verdict = when === "after" && after === "green" ? "pass" : "fail";
  return {
    ...other,
    [check]: verdict,
    ...(aggregate === undefined ? {} : { [aggregate]: verdict }),
  };
}
/** What the commit's check runs answer, which is headIs but for world `k`: the by-hand run's
 * failure of the same check name is the latest run of it there, before the re-run and after it,
 * and the counted set holds neither. */
function runsAre(world: RedWorld, when: "before" | "after"): Record<string, string> {
  if (world !== "k") return headIs(world, when);
  return { ...headIs(world, when), [RED_WORLD[world].check]: "fail" };
}
/** The settlement the Envoy listener renders for a state of the head, as red_fixture writes it
 * for the re-run to publish and for the rig's own report of the check: `notify` is the id of the
 * run of `test (notify)` that state holds, the red's own or the attempt the re-run made, and `ci`
 * the id of the run of the aggregate that state holds, in worlds i and j alone. */
const settlement = (notify: number, passed: string[], failed: string[], ci?: number) => ({
  kind: "checks",
  repo: RED_REPO,
  number: String(RED_PR),
  sha: RED_HEAD,
  check_runs: [
    ...(ci === undefined ? [] : [{ name: "ci", id: ci }]),
    { name: "lint", id: RED_ID.lint },
    { name: "test (notify)", id: notify },
    { name: "typecheck", id: RED_ID.typecheck },
  ],
  generation: 2,
  snapshot: RED_HEAD.slice(0, 8),
  failed: { count: failed.length, checks: failed },
  running: { count: 0, checks: [] },
  passed: { count: passed.length, checks: passed },
  queued: { count: 0, checks: [] },
  cancelled: { count: 0, checks: [] },
  skipped: { count: 0, checks: [] },
  failing_checks: failed.map((name) => ({ name, url: RED_RUN_URL })),
});
/** That settlement as the envelope a listener receives it in. */
const envelope = (notify: number, passed: string[], failed: string[], ci?: number) => ({
  authorization: "Bearer rig-envoy-token",
  url: "/v1/messages/publish",
  body: {
    topic: `notifications.github.example.widgets.pr.${RED_PR}.checks`,
    source: "github",
    message:
      `checks settled on ${RED_REPO}#${RED_PR} @ ${RED_HEAD.slice(0, 7)}: ` +
      `${passed.length} passed, ${failed.length} failed, 0 cancelled, 0 skipped`,
    payload: JSON.stringify(settlement(notify, passed, failed, ci)),
  },
});

/** Names `listener` as the run's own Envoy, with a bearer file, in <run>/pane.env: the file
 * boot_worker writes the agent's environment to, from the batch's services file, and the one the
 * gh stand-in and red_report read the listener from. */
function paneListener(run: string, listener: string) {
  const token = path.join(run, "envoy-token");
  writeFileSync(token, "rig-envoy-token\n");
  writeFileSync(path.join(run, "pane.env"), `ENVOY_URL=${listener}\nENVOY_TOKEN_FILE=${token}\n`);
}

/** The rig reporting world `l`'s check between the two turns (red_report), with `listener` as
 * the Envoy the run's pane.env names, and what it printed. The shell runs as a child this test
 * awaits, never a synchronous one, so the listener of the row answers the publish while it runs. */
async function redReport(run: string, listener: string) {
  paneListener(run, listener);
  const child = Bun.spawn(
    [
      "bash",
      "-c",
      `${fn("fail")}\n${fn("note")}\n${fn("red_report")}\nR=$1 here=$2\nred_report`,
      "-",
      run,
      import.meta.dir,
    ],
    { env: childEnv, stdout: "pipe", stderr: "pipe" }
  );
  const [stdout, stderr, exit] = await Promise.all([
    new Response(child.stdout).text(),
    new Response(child.stderr).text(),
    child.exited,
  ]);
  if (exit !== 0) throw new Error(`red_report: ${stderr}`);
  return stdout;
}

describe("red_fixture", () => {
  // One row per world, because each world is a seed of its own and a disagreement must name the
  // world it is in. Catches: a world whose reads of the head disagree (a rollup that stays green
  // under a red `gh pr checks`, or a GraphQL rollup that answers the review threads of
  // worker_fixture), a read that answers the same state twice, a settlement that never lands, and
  // a `gh pr checks` whose status does not follow its rows. The counted set and the commit
  // endpoint answer the same checks in every world but `k`, whose commit endpoint holds the
  // by-hand run as well (runsAre). World `l` reports no red at all until the rig reports it, so
  // it has rows of its own below rather than a place in this one.
  const settling = RED_WORLDS.filter((world) => world !== "l");
  // Each world's head is read twice before the re-run, which is both that every read answers the
  // world's own red and the defect the first live run of world a measured: the head's second read
  // in a turn answered the settlement although the role had pressed nothing, so the role read the
  // head as already re-run and pressed no re-run of its own. The head turns on the act, never on
  // the read (gh-standin.ts, `follows`), and the re-run the role presses after its reads still
  // turns it.
  test.each([
    ...settling,
  ])("world %s: reading the head again, with no re-run between, answers red again", async (world) => {
    const run = redFixture(world);

    const first = await readHead(run);
    const again = await readHead(run);
    for (const read of [first, again]) {
      expect({
        rollup: read.rollup,
        graphql: read.graphql,
        checks: read.checks,
        runs: read.checkRuns,
      }).toEqual({
        rollup: headIs(world, "before"),
        graphql: headIs(world, "before"),
        checks: headIs(world, "before"),
        runs: runsAre(world, "before"),
      });
      expect(read.checksExit).toBe(1);
    }

    // gh 2.98.0 prints nothing for a re-run it requested where no terminal is there (measured).
    const rerun = await gh(run, ["run", "rerun", String(RED_RUN_ID), "--failed"]);
    expect([rerun.stdout, rerun.exit]).toEqual(["", 0]);
    const after = await readHead(run);
    expect({
      rollup: after.rollup,
      graphql: after.graphql,
      checks: after.checks,
      runs: after.checkRuns,
    }).toEqual({
      rollup: headIs(world, "after"),
      graphql: headIs(world, "after"),
      checks: headIs(world, "after"),
      runs: runsAre(world, "after"),
    });
    expect(after.checksExit).toBe(RED_WORLD[world].after === "green" ? 0 : 1);
  });

  // Catches: a world `e` whose head's runs carry no second attempt, which is where the page
  // reads the spend, and a second attempt in every world, which would spend the re-run of the
  // eleven others.
  test("the head's runs answer attempt 1 in every world, and the spent head's attempt 2", async () => {
    const seen: Record<string, string[]> = {};
    for (const world of RED_WORLDS) {
      const runs = await gh(redFixture(world), [
        "api",
        `repos/${RED_REPO}/actions/runs?head_sha=${RED_HEAD}&event=pull_request&per_page=100`,
        "--jq",
        ".workflow_runs[] | [.id,.name,.run_attempt,.conclusion] | @tsv",
      ]);
      seen[world] = outputLines(runs.stdout);
    }
    const red = `${RED_RUN_ID}\tci\t1\tfailure`;
    expect(seen).toEqual({
      ...Object.fromEntries(RED_WORLDS.map((world) => [world, [red]])),
      e: [red, `${RED_RUN_ID - 1}\te2e\t2\tsuccess`],
      // The title check runs in a workflow of its own; lint and typecheck in the `ci` run beside.
      f: [`${RED_RUN_ID}\ttitle\t1\tfailure`, `${RED_RUN_ID + 2}\tci\t1\tsuccess`],
    });
  });

  // The page reads the spend on each earlier attempt of a run whose `run_attempt` is above 1.
  // Catches: a world `e` whose attempt read answers no jobs (the catch-all's runs list), which
  // leaves a role following the page unable to judge the spend, and an attempt answer in every
  // world.
  test("the spent head's attempt 1 answers the superseded e2e failure, in world e alone", async () => {
    const seen: Record<string, string[]> = {};
    for (const world of RED_WORLDS) {
      const jobs = await gh(redFixture(world), [
        "api",
        "--paginate",
        `repos/${RED_REPO}/actions/runs/${RED_RUN_ID - 1}/attempts/1/jobs?per_page=100`,
        "--jq",
        ".total_count, (.jobs[] | [.name,.conclusion] | @tsv)",
      ]);
      seen[world] = outputLines(jobs.stdout);
    }
    const attempt = ["1", "e2e\tfailure"];
    const answered = RED_WORLDS.filter((world) => seen[world].join("\n") === attempt.join("\n"));
    expect(seen.e).toEqual(attempt);
    expect(answered).toEqual(["e"]);
  });

  // The commit endpoint of world `e` keeps the superseded e2e runs, which the page lets no role
  // judge the spend on. Catches: a seed that moved them out with the spend, which would leave the
  // world unable to tell a role judging there from one judging on the head's runs, and one that
  // carries them in every world.
  test("the spent head alone carries the superseded runs the commit endpoint answers", async () => {
    const spent = await readHead(redFixture("e"));
    const fresh = await readHead(redFixture("a"));
    expect(spent.checkRunRows).toEqual([
      `${RED_ID.lint}\tlint\tsuccess`,
      `${RED_ID.typecheck}\ttypecheck\tsuccess`,
      `${RED_RED_ID - 4}\te2e\tfailure`,
      `${RED_RED_ID - 3}\te2e\tsuccess`,
      `${RED_RED_ID}\ttest (notify)\tfailure`,
    ]);
    expect(fresh.checkRunRows).toEqual([
      `${RED_ID.lint}\tlint\tsuccess`,
      `${RED_ID.typecheck}\ttypecheck\tsuccess`,
      `${RED_RED_ID}\ttest (notify)\tfailure`,
    ]);
  });

  // Catches: a read of another commit's check runs, the base's among them, answered with the
  // head's own red, so the base reads as failing the same test and a role takes the world's
  // flake for a defect on the base that no re-run clears (red-at-the-head.md, "When the cause is
  // another tree's"). The tester's round-9 batch at c0ace596 met it: trial-j-nos1-4 read main's
  // check runs at 77671688…, was answered `test (notify)` failure, and pressed no re-run, giving
  // that as its reason. Seven more runs of that batch and of round 6's read `commits/null/…` or
  // `commits/trunk/…` and were answered the same red.
  // On GitHub a ref no commit has is a 422, whose body gh prints on stdout (measured on gh 2.98.0, 2026-11-18).
  test("a read of a commit other than the head answers none of the head's red", async () => {
    for (const world of ["a", "i", "j"]) {
      const run = redFixture(world);
      // The base, by its sha and by its branch: a commit nothing ran on, which GitHub answers
      // with no check run, exit 0.
      for (const commit of [RED_BASE, "main"]) {
        const read = await gh(run, [
          "api",
          `repos/${RED_REPO}/commits/${commit}/check-runs`,
          "--jq",
          '.check_runs[] | select(.conclusion == "failure") | .name',
        ]);
        expect({ world, commit, exit: read.exit, failing: outputLines(read.stdout) }).toEqual({
          world,
          commit,
          exit: 0,
          failing: [],
        });
      }
      // A ref no commit has: GitHub's 422, whose body gh prints on stdout, skipping --jq, and
      // which names no check of the head's.
      const unknown = await gh(run, [
        "api",
        `repos/${RED_REPO}/commits/null/check-runs`,
        "--jq",
        '.check_runs[] | select(.conclusion == "failure") | .name',
      ]);
      expect({
        world,
        exit: unknown.exit,
        stderr: unknown.stderr,
        namesHeadRed: /test \(notify\)|"ci"/.test(unknown.stdout),
      }).toEqual({
        world,
        exit: 1,
        stderr: "gh: No commit found for SHA: null (HTTP 422)\n",
        namesHeadRed: false,
      });
    }
  });

  // Catches: a `--json` read that names a field the world's record lacks, or a field gh does not
  // know, answered exit 0 with the field left out, where GitHub answers every field the command
  // lists and gh 2.98.0 refuses an unknown one. The stand-in's picker (gh-standin.ts, `picked`)
  // kept only the fields the record held. The tester's round-10 batch at 4728c2f7 met it in 11
  // of its first 46 runs, every one a head run: `pr view --json mergeStateStatus` (a-head-2,
  // d-head-1, d-head-5, e-head-4) read the field as missing, `pr view --json files` (d-head-2)
  // exited 1 in the one world where the files decide attribution, and `run view --json
  // runAttempt` (a-head-1, a-head-4, b-head-3, b-head-4, c-head-1, c-head-2), a field gh refuses,
  // exited 0. Each such read must give the field a value, or be refused. The commits read names a
  // field the world does not hold, so it is the one that reaches the picker's missing-field case
  // on a rig that holds the rest: with only the other three, a picker set back to drop a missing
  // field passed this row (the tester's mutation at 76cc2379).
  test("a --json read answers every field it names, or is refused", async () => {
    const run = redFixture("a");
    const reads = [
      ["pr", "view", String(RED_PR), "--repo", RED_REPO, "--json", "mergeStateStatus"],
      ["pr", "view", String(RED_PR), "--repo", RED_REPO, "--json", "files"],
      ["pr", "view", String(RED_PR), "--repo", RED_REPO, "--json", "commits"],
      ["run", "view", String(RED_RUN_ID), "--repo", RED_REPO, "--json", "status,runAttempt"],
    ];
    for (const args of reads) {
      const read = await gh(run, args);
      const fields = args[args.indexOf("--json") + 1].split(",");
      const answered = read.exit === 0 ? (JSON.parse(read.stdout) as Record<string, unknown>) : {};
      const silent = fields.filter((field) => read.exit === 0 && answered[field] === undefined);
      expect({ call: args.join(" "), silent }).toEqual({ call: args.join(" "), silent: [] });
    }
  });

  // Catches: a route for other commits that takes a read of the head by another of its names, its
  // short sha or its branch, or a `status` read of it, each of which answered the head's red
  // before those routes and must answer it still; a base whose status or statuses read as
  // anything but GitHub's for a commit nothing ran on; and a commit the rig does not know that is
  // answered rather than refused, or refused under another name than the one the call gave it.
  test("the head keeps its red under every name, the base reads as no check, another commit is refused", async () => {
    const run = redFixture("a");
    const failing = async (ref: string, endpoint: string) =>
      outputLines(
        (
          await gh(run, [
            "api",
            `repos/${RED_REPO}/commits/${ref}/${endpoint}`,
            "--jq",
            '.check_runs[] | select(.conclusion == "failure") | .name',
          ])
        ).stdout
      );
    // A `status` read of the head answers its combined status, GitHub's for a commit no status was
    // posted to: no check run is in it, so it names no failure.
    expect({
      short: await failing(RED_HEAD.slice(0, 7), "check-runs"),
      branch: await failing(`legion/${RED_KEY}`, "check-runs"),
      ref: await failing(`refs/heads/legion/${RED_KEY}`, "check-runs?filter=all"),
      status: await failing(RED_HEAD, "status"),
    }).toEqual({
      short: ["test (notify)"],
      branch: ["test (notify)"],
      ref: ["test (notify)"],
      status: [],
    });
    const status = await gh(run, ["api", `repos/${RED_REPO}/commits/main/status`]);
    const statuses = await gh(run, [
      "api",
      `repos/${RED_REPO}/commits/${RED_BASE.slice(0, 7)}/statuses`,
    ]);
    // The combined status's other fields (its repository and URLs) are the measured rows' below.
    expect([JSON.parse(status.stdout), JSON.parse(statuses.stdout)]).toMatchObject([
      { state: "pending", statuses: [], total_count: 0, sha: RED_BASE },
      [],
    ]);
    const trunk = await gh(run, ["api", `repos/${RED_REPO}/commits/trunk/check-runs`]);
    expect([trunk.stdout, trunk.stderr, trunk.exit]).toEqual(
      githubAnswer("checkruns-422", "trunk")
    );
  });

  // Catches: a GraphQL rollup read of another commit answered with the head's counted set, the
  // defect of the check-runs row above through the read the page gives for the counted set at a
  // named head (RED_ROLLUP_QUERY), whether the call names the commit in its `oid` field or inline.
  // The base's rollup reads as GitHub's for a commit with no check, a commit the rig does not know
  // as no object at all, and the head's own read, by oid or by its branch, keeps the head's red.
  test("a GraphQL rollup read of a commit other than the head answers none of the head's red", async () => {
    const unknown = "0123456789abcdef0123456789abcdef01234567";
    const inline =
      'query{repository(owner:"example",name:"widgets"){object(expression:"<commit>"){' +
      "... on Commit{statusCheckRollup{state}}}}}";
    // Each read as a role sends it: the page's query with the commit in its `oid` field, or a
    // query that names the commit inline.
    const reads: Record<string, string[]> = {
      base: ["-F", `oid=${RED_BASE}`, "-f", `query=${RED_ROLLUP_QUERY}`],
      main: ["-f", `query=${inline.replace("<commit>", "main")}`],
      unknown: ["-F", `oid=${unknown}`, "-f", `query=${RED_ROLLUP_QUERY}`],
      head: ["-F", `oid=${RED_HEAD}`, "-f", `query=${RED_ROLLUP_QUERY}`],
      branch: ["-f", `query=${inline.replace("<commit>", `legion/${RED_KEY}`)}`],
    };
    for (const world of ["a", "i", "j"]) {
      const run = redFixture(world);
      const objects: Record<string, { statusCheckRollup: { state: string } | null } | null> = {};
      for (const [read, args] of Object.entries(reads)) {
        const answer = await gh(run, [
          "api",
          "graphql",
          ...args,
          "--jq",
          ".data.repository.object",
        ]);
        objects[read] = JSON.parse(answer.stdout);
      }
      expect({
        world,
        base: objects.base,
        main: objects.main,
        unknown: objects.unknown,
        head: objects.head?.statusCheckRollup?.state,
        branch: objects.branch?.statusCheckRollup?.state,
      }).toEqual({
        world,
        base: { statusCheckRollup: null },
        main: { statusCheckRollup: null },
        unknown: null,
        head: "FAILURE",
        branch: "FAILURE",
      });
    }
  });

  // Catches: a read of the runs of another commit or branch, the base's among them, answered with
  // the runs of the head, through the REST read of the runs or `gh run list`, so the base reads as
  // failing the red of the head; and a route for those reads that takes the head's own read, by
  // its sha or by its branch.
  test("a read of the runs of another commit or branch answers no run, and the head's keeps its run", async () => {
    const run = redFixture("a");
    const runs = `repos/${RED_REPO}/actions/runs`;
    const conclusions = async (args: string[]) => outputLines((await gh(run, args)).stdout);
    expect({
      baseSha: await conclusions([
        "api",
        `${runs}?head_sha=${RED_BASE}&per_page=100`,
        "--jq",
        ".workflow_runs[] | .conclusion",
      ]),
      main: await conclusions([
        "api",
        `${runs}?branch=main`,
        "--jq",
        ".workflow_runs[] | .conclusion",
      ]),
      listMain: await conclusions([
        "run",
        "list",
        "--branch",
        "main",
        "--json",
        "conclusion",
        "--jq",
        ".[] | .conclusion",
      ]),
      listBase: await gh(run, ["run", "list", "--commit", RED_BASE]),
      head: await conclusions([
        "api",
        `${runs}?head_sha=${RED_HEAD}&event=pull_request&per_page=100`,
        "--jq",
        ".workflow_runs[] | .conclusion",
      ]),
      headBranch: await conclusions([
        "api",
        `${runs}?branch=legion/${RED_KEY}`,
        "--jq",
        ".workflow_runs[] | .conclusion",
      ]),
      listHead: await conclusions([
        "run",
        "list",
        "--commit",
        RED_HEAD,
        "--json",
        "conclusion",
        "--jq",
        ".[] | .conclusion",
      ]),
    }).toEqual({
      baseSha: [],
      main: [],
      listMain: [],
      listBase: { stdout: "", stderr: "", exit: 0 },
      head: ["failure"],
      headBranch: ["failure"],
      listHead: ["failure"],
    });
  });

  // Catches: a `gh run rerun` that names no run of the head pressing the head's re-run. A re-run
  // of the red's check run id, which trial-d-head-5, trial-e-head-2 and trial-g-head-2 made,
  // settled the head and published its settlement, where GitHub answers the 404 of a run it has
  // not got; `gh run rerun --help`, which trial-i-nos1-1 and trial-j-nos1-1 ran, did the same,
  // where gh prints its help. Neither may move the head, and the re-run of the head's own run
  // still settles it.
  test("a re-run of a check run id is refused and leaves the head red; the head's own run settles it", async () => {
    const run = redFixture("a");
    const refused = await gh(run, ["run", "rerun", String(RED_RED_ID), "--failed"]);
    const help = await gh(run, ["run", "rerun", "--help"]);
    const before = await readHead(run);
    const pressed = await gh(run, ["run", "rerun", String(RED_RUN_ID), "--failed"]);
    const after = await readHead(run);
    // Each of the four reads of the head as a verdict per check: the rollup of `gh pr view`, the
    // GraphQL rollup, `gh pr checks` and the check runs of the commit.
    expect({
      refused,
      help: [help.stdout.split("\n")[0], help.stderr, help.exit],
      before: [before.rollup, before.graphql, before.checks, before.checkRuns],
      pressed: [pressed.stdout, pressed.exit],
      after: [after.rollup, after.graphql, after.checks, after.checkRuns],
    }).toEqual({
      refused: {
        stdout: "",
        stderr:
          `failed to get run: HTTP 404: Not Found (https://api.github.com/repos/${RED_REPO}` +
          `/actions/runs/${RED_RED_ID}?exclude_pull_requests=true)\n`,
        exit: 1,
      },
      help: ["Rerun an entire run, only failed jobs, or a specific job from a run.", "", 0],
      before: Array(4).fill(headIs("a", "before")),
      pressed: ["", 0],
      after: Array(4).fill(headIs("a", "after")),
    });
  });

  // Catches: a title check whose output names another title than the pull request's own, which
  // the role reads as a red of some other change.
  test("the title check's output names the pull request's own title", async () => {
    const run = redFixture("f");
    const title = await gh(run, [
      "pr",
      "view",
      String(RED_PR),
      "--json",
      "title",
      "--jq",
      ".title",
    ]);
    const summary = await gh(run, [
      "api",
      `repos/${RED_REPO}/commits/${RED_HEAD}/check-runs?filter=all`,
      "--jq",
      '.check_runs[] | select(.name == "title") | .output.summary',
    ]);
    const log = await gh(run, ["run", "view", String(RED_RUN_ID), "--log-failed"]);
    const said = `The title "${RED_TITLE}" has no scope: the pattern is <type>(<scope>): <subject>.`;
    expect(title.stdout).toBe(`${RED_TITLE}\n`);
    expect(summary.stdout).toBe(`${said}\n`);
    // gh prints a log line as `<job>\t<step>\t<time> <line>`, with GitHub's BOM before the time
    // on the first line of a step (measured, runview-job-log).
    expect(log.stdout).toBe(
      `title\tCheck the pull request title\t\uFEFF2026-03-04T10:00:00.0000000Z ${said}\n`
    );
  });

  // Catches: a world h whose log read answers a line like every other world's, which the page's
  // "a read that fails is recorded as failed" has nothing to record; an error in other words than
  // gh 2.98.0 prints for GitHub's 410 on a log past retention (measured by the lead on 2026-11-18,
  // run 30492596243 of cybermonklabs/legion): the reason `Server Error` and the URL of the log gh
  // asked for, the run's or, where the call names one, its attempt's; a REST read of the job's
  // log refused where GitHub answers its 410; and a world whose live job log answers nothing.
  test("world h's log reads fail as gh prints GitHub's 410, where world a's answer the failing line", async () => {
    const answer = async (run: string, args: string[]) => {
      const read = await gh(run, args);
      return [read.stdout, read.stderr, read.exit];
    };
    const red = ["run", "view", String(RED_RUN_ID)];
    const restLog = ["api", `repos/${RED_REPO}/actions/jobs/${RED_JOB_ID}/logs`];
    const runUrl = `https://api.github.com/repos/${RED_REPO}/actions/runs/${RED_RUN_ID}`;
    const gone = (logs: string) => [
      "",
      `failed to get run log: HTTP 410: Server Error (${runUrl}/${logs})\n`,
      1,
    ];
    const h = redFixture("h");
    expect({
      failed: await answer(h, [...red, "--log-failed"]),
      whole: await answer(h, [...red, "--log"]),
      attempt: await answer(h, [...red, "--attempt", "1", "--log-failed"]),
      job: await answer(h, ["run", "view", "--job", String(RED_JOB_ID), "--log"]),
      rest: await answer(h, restLog),
    }).toEqual({
      failed: gone("logs"),
      whole: gone("logs"),
      attempt: gone("attempts/1/logs"),
      job: gone("logs"),
      rest: [
        JSON.stringify({
          message: "Server Error",
          documentation_url:
            "https://docs.github.com/rest/actions/workflow-jobs#download-job-logs-for-a-workflow-run",
          status: "410",
        }),
        "gh: Server Error (HTTP 410)\n",
        1,
      ],
    });
    const a = redFixture("a");
    const at = "2026-03-04T10:00:00.0000000Z";
    const line = "(fail) notify > retries a refused send [3.00ms]";
    expect({
      failed: await answer(a, [...red, "--log-failed"]),
      rest: await answer(a, restLog),
    }).toEqual({
      failed: [`test (notify)\tRun bun test packages/notify\t\uFEFF${at} ${line}\n`, "", 0],
      rest: [
        `\uFEFF${at} ##[group]Run bun test packages/notify\n${at} ${line}\n` +
          `${at} ##[error]Process completed with exit code 1.\n${at} ##[endgroup]\n`,
        "",
        0,
      ],
    });
  });

  // Catches: a job log in other bytes than GitHub sends it and gh 2.98.0 prints it (measured on
  // job 135117329305 of cybermonklabs/legion, 2026-11-18: joblog-live and runview-job-log of the
  // measurements): the REST log without the one BOM at its start, a gh log without the BOM at the
  // first line of each step, a line without GitHub's timestamp, a log that does not end in a
  // newline, and an ESC byte, which gh refuses to print without --allow-escape-sequences.
  test("a job log reads as GitHub sends it and gh prints it: its BOMs, timestamps and last newline", async () => {
    const BOM = "\uFEFF";
    const stamped = (line: string) => /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{7}Z /.test(line);
    const seen: { world: string; read: string; bom: boolean; stamps: boolean; end: boolean }[] = [];
    const shape = (world: string, read: string, text: string, bom: boolean, lines: string[]) =>
      seen.push({
        world,
        read,
        bom: bom && !text.includes("\u001b"),
        stamps: lines.length > 1 && lines.slice(0, -1).every(stamped),
        end: lines.at(-1) === "",
      });
    for (const world of ["a", "i"]) {
      const run = redFixture(world);
      for (const id of [RED_JOB_ID, RED_JOB_ID + 1]) {
        const read = `api .../actions/jobs/${id}/logs`;
        const text = (await gh(run, ["api", `repos/${RED_REPO}/actions/jobs/${id}/logs`])).stdout;
        const body = text.slice(BOM.length);
        shape(world, read, text, text.startsWith(BOM) && !body.includes(BOM), body.split("\n"));
      }
      for (const args of [
        ["run", "view", String(RED_RUN_ID), "--log-failed"],
        ["run", "view", String(RED_RUN_ID), "--log"],
        ["run", "view", "--job", String(RED_JOB_ID), "--log"],
      ]) {
        const text = (await gh(run, args)).stdout;
        const steps = new Set<string>();
        let boms = true;
        const logs = text.split("\n").map((line, index, all) => {
          if (index === all.length - 1) return line;
          const [job, step, log = ""] = line.split("\t");
          const first = !steps.has(`${job}\t${step}`);
          steps.add(`${job}\t${step}`);
          if (log.startsWith(BOM) !== first || log.slice(1).includes(BOM)) boms = false;
          return first ? log.slice(BOM.length) : log;
        });
        shape(world, args.join(" "), text, boms, logs);
      }
    }
    expect(seen).toEqual(seen.map((read) => ({ ...read, bom: true, stamps: true, end: true })));
  });

  // Catches: a counted set in a shape of the rig's own, which a role that reads it as GitHub
  // answers one — the state, the one page of contexts with the cursor of its last node, and a
  // CheckRun node per check — would read as nothing at all; a node that names no run, which
  // leaves the role nothing to re-run; and fields the page's query does not ask (each node's
  // detailsUrl) answered all the same.
  test("the GraphQL rollup answers the counted set in the shape GitHub answers", async () => {
    const run = redFixture("a");
    const answer = await rollupRead(run, ".data.repository.object.statusCheckRollup");
    // GitHub Actions is the app of every check run of the world (its databaseId on GitHub).
    const checkSuite = {
      app: { databaseId: 15368 },
      workflowRun: { event: "pull_request", databaseId: RED_RUN_ID },
    };
    expect(JSON.parse(answer.stdout)).toEqual({
      state: "FAILURE",
      contexts: {
        totalCount: 3,
        // GitHub's cursor of the third and last node (gh-graphql.ts), base64 of "3".
        pageInfo: { hasNextPage: false, endCursor: "Mw" },
        nodes: [
          {
            __typename: "CheckRun",
            name: "lint",
            databaseId: RED_ID.lint,
            status: "COMPLETED",
            conclusion: "SUCCESS",
            checkSuite,
          },
          {
            __typename: "CheckRun",
            name: "test (notify)",
            databaseId: RED_RED_ID,
            status: "COMPLETED",
            conclusion: "FAILURE",
            checkSuite,
          },
          {
            __typename: "CheckRun",
            name: "typecheck",
            databaseId: RED_ID.typecheck,
            status: "COMPLETED",
            conclusion: "SUCCESS",
            checkSuite,
          },
        ],
      },
    });
  });

  // Catches: a world k whose by-hand failure is of another check name, which no role would read
  // as the head's one re-run already spent, and one the counted set holds too, which would make
  // the two reads agree and leave the world measuring nothing.
  test("world k: the commit endpoint answers a by-hand failure the counted set does not hold", async () => {
    const run = redFixture("k");
    const before = await readHead(run);
    // Two failing runs of the one check name at the commit, the second a by-hand run's, against
    // one counted run of it.
    expect(before.checkRunRows).toEqual([
      `${RED_ID.lint}\tlint\tsuccess`,
      `${RED_ID.typecheck}\ttypecheck\tsuccess`,
      `${RED_RED_ID}\ttest (notify)\tfailure`,
      `${RED_RED_ID + 2}\ttest (notify)\tfailure`,
    ]);
    expect(before.graphql).toEqual(headIs("k", "before"));
    expect(before.checks).toEqual(headIs("k", "before"));
    const counted = await rollupRead(
      run,
      ".data.repository.object.statusCheckRollup.contexts.nodes " +
        '| map(select(.name == "test (notify)")) | [length, .[0].databaseId]'
    );
    expect(JSON.parse(counted.stdout)).toEqual([1, RED_RED_ID]);
    // The by-hand run is a run of its own at that head, which is what puts it outside the set
    // GitHub counts for the pull request: its check run links to the job of that run.
    const byHand = await gh(run, [
      "api",
      `repos/${RED_REPO}/commits/${RED_HEAD}/check-runs?filter=all`,
      "--jq",
      `.check_runs[] | select(.id == ${RED_RED_ID + 2}) | [.details_url, .output.summary] | @tsv`,
    ]);
    expect(byHand.stdout).toBe(
      `https://github.com/${RED_REPO}/actions/runs/${RED_RUN_ID + 1}/job/${RED_JOB_ID + 6}\t` +
        "A by-hand workflow_dispatch run of test (notify) failed.\n"
    );
    // The re-run clears the counted run and leaves the by-hand one, which is why a role that
    // reads the commit endpoint for the head's state reads it red after a green re-run.
    await gh(run, ["run", "rerun", String(RED_RUN_ID), "--failed"]);
    const after = await readHead(run);
    expect({ graphql: after.graphql, runs: after.checkRuns }).toEqual({
      graphql: headIs("k", "after"),
      runs: runsAre("k", "after"),
    });
  });

  // Catches: a world l whose counted set reports the red from the start, which the role could
  // decide on in its first turn, and one whose commit endpoint holds no failing run, which would
  // leave the role nothing to wait on. red_report is the rig reporting the check between the
  // turns: the counted set reports it, the run learns when, and the settlement wakes the role.
  test("world l: the counted set reports the red only once the rig reports it", async () => {
    const run = redFixture("l");
    const unreported = await readHead(run);
    expect({
      rollup: unreported.rollup,
      graphql: unreported.graphql,
      checks: unreported.checks,
    }).toEqual({
      rollup: { lint: "pass", typecheck: "pass" },
      graphql: { lint: "pass", typecheck: "pass" },
      checks: { lint: "pass", typecheck: "pass" },
    });
    expect(unreported.checksExit).toBe(0);
    // The commit endpoint holds the failing run all along, which is the read the page forbids
    // deciding on.
    expect(unreported.checkRunRows).toEqual([
      `${RED_ID.lint}\tlint\tsuccess`,
      `${RED_ID.typecheck}\ttypecheck\tsuccess`,
      `${RED_RED_ID}\ttest (notify)\tfailure`,
    ]);

    const received: { authorization: string | null; url: string; body: unknown }[] = [];
    const server = Bun.serve({
      port: 0,
      hostname: "127.0.0.1",
      fetch: async (request) => {
        received.push({
          authorization: request.headers.get("authorization"),
          url: new URL(request.url).pathname,
          body: await request.json(),
        });
        return Response.json({ ok: true });
      },
    });
    const at = new Date();
    try {
      await redReport(run, `http://127.0.0.1:${server.port}`);
    } finally {
      server.stop(true);
    }
    expect(received).toEqual([envelope(RED_RED_ID, ["lint", "typecheck"], ["test (notify)"])]);
    const reportedAt = readFileSync(path.join(run, "reported-at"), "utf8");
    expect(reportedAt).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z\n$/);
    expect(new Date(reportedAt.trim()).getTime()).toBeGreaterThanOrEqual(
      Math.floor(at.getTime() / 1000) * 1000
    );

    const reported = await readHead(run);
    expect({ rollup: reported.rollup, graphql: reported.graphql, checks: reported.checks }).toEqual(
      {
        rollup: headIs("l", "before"),
        graphql: headIs("l", "before"),
        checks: headIs("l", "before"),
      }
    );
    expect(reported.checksExit).toBe(1);

    const rerun = await gh(run, ["run", "rerun", String(RED_RUN_ID), "--failed"]);
    expect(rerun.exit).toBe(0);
    const after = await readHead(run);
    expect({
      rollup: after.rollup,
      graphql: after.graphql,
      checks: after.checks,
      runs: after.checkRuns,
    }).toEqual({
      rollup: headIs("l", "after"),
      graphql: headIs("l", "after"),
      checks: headIs("l", "after"),
      runs: runsAre("l", "after"),
    });
    expect(after.checksExit).toBe(0);
  });

  // The routes are the world's in their content alone, so one row runs the page's six calls in
  // every world. Catches: a call of the page that falls through to the stand-in's 404, a read of
  // the counted set answered by worker_fixture's generic graphql route, which holds a review
  // thread list, a check-runs read whose `filter=all` query misses the head's own route and takes
  // the generic one, a jobs read answered by the route for the head's runs, and an aggregate
  // world whose jobs name no failed lane.
  // The row builds the twelve worlds and makes six calls in each, so it has a bound of its own:
  // measured alone, 70.74 s with the pod's own git first on PATH and 47.08 s with plain git first
  // (the tester, 2026-11-18, at a201b17c), and 60.82 s with the pod's own git on the fold of the
  // review of 76cc2379 (2026-11-18), over the suite's 60 s default.
  test("every call the page names is answered by its own route, in every world", async () => {
    const seen: unknown[] = [];
    for (const world of RED_WORLDS) {
      const run = redFixture(world);
      await gh(run, [
        "api",
        `repos/${RED_REPO}/actions/runs?head_sha=${RED_HEAD}&event=pull_request`,
        "--jq",
        ".workflow_runs[] | [.id,.event,.conclusion] | @tsv",
      ]);
      await rollupRead(run, ".data.repository.object.statusCheckRollup.state");
      const jobs = await gh(run, [
        "api",
        `repos/${RED_REPO}/actions/runs/${RED_RUN_ID}/jobs`,
        "--jq",
        ".jobs[] | [.id,.name,.conclusion] | @tsv",
      ]);
      await gh(run, ["run", "view", String(RED_RUN_ID), "--log-failed"]);
      await gh(run, ["run", "rerun", String(RED_RUN_ID), "--failed"]);
      await gh(run, [
        "api",
        "--paginate",
        `repos/${RED_REPO}/commits/${RED_HEAD}/check-runs?filter=all&per_page=100`,
        "--jq",
        ".check_runs[] | [.id,.name,.conclusion] | @tsv",
      ]);
      // Each route by the read it answers: the REST reads by the endpoint their match is anchored
      // on, the rollup, the log and the press by the route of the run that answers each.
      const rollup = routeOf(run, (route) => route.head_read === "graphql");
      // The log of the run, read with no attempt named (world h has a route for attempt 1 too).
      const log = routeOf(
        run,
        (route) =>
          route.match.startsWith(`^gh run view ${RED_RUN_ID}(`) &&
          route.match.includes("(?=.*\\s--log") &&
          !route.match.includes("--attempt")
      );
      const press = routeOf(
        run,
        (route) => route.publish !== undefined && route.match.startsWith("^gh run rerun")
      );
      const label = (route: string | null) =>
        route === null
          ? "unmodeled"
          : route === rollup
            ? "rollup"
            : route === log
              ? "log"
              : route === press
                ? "press"
                : route.includes(`actions/runs/${RED_RUN_ID}/jobs`)
                  ? "jobs"
                  : route.includes("actions/runs")
                    ? "runs"
                    : route.includes("check-runs")
                      ? "check-runs"
                      : route;
      seen.push({
        world,
        routes: ghCalls(run).map((call) => label(call.route)),
        jobs: jobs.stdout,
      });
    }
    expect(seen).toEqual(
      RED_WORLDS.map((world) => ({
        world,
        routes: ["runs", "rollup", "jobs", "log", "press", "check-runs"],
        // The jobs of the run of the red: the failing lane, and beside it lint and typecheck (in
        // `f` the title check runs alone in a run of its own), and in `g`, `i` and `j` the
        // aggregate `ci` over the lane.
        jobs:
          `${RED_JOB_ID}\t${RED_WORLD[world].job}\tfailure\n` +
          (world === "f"
            ? ""
            : `${RED_JOB_ID + 1}\tlint\tsuccess\n${RED_JOB_ID + 2}\ttypecheck\tsuccess\n`) +
          (["g", "i", "j"].includes(world) ? `${RED_JOB_ID + 3}\tci\tfailure\n` : ""),
      }))
    );
  }, 240_000);

  // Catches: a `--required` read answered by the route for every check, which hands a role every
  // lane where GitHub hands it the required set alone (the review of red-at-the-head.md:86), a
  // required read that stays red after a green re-run, and a read without the flag that lost a
  // check to the new route. The base requires the one aggregate check `ci` (red_fixture), which
  // the heads of worlds `g`, `i` and `j` report; every other head answers gh's own `no required
  // checks reported` and exits 1.
  test("gh pr checks --required answers the required check alone, and a read without it every check", async () => {
    const none = {
      stdout: "",
      stderr: `no required checks reported on the 'legion/${RED_KEY}' branch\n`,
      exit: 1,
    };
    /** The job of the check run of `ci` a head first reports, in the worlds whose head reports
     * one: world g's red, and the aggregate over the red lane of i and j, the aggregate job of the
     * run of the red in each; its attempt 2 is the job the re-run makes. */
    const ciJob: Partial<Record<RedWorld, number>> = {
      g: RED_JOB_ID + 3,
      i: RED_JOB_ID + 3,
      j: RED_JOB_ID + 3,
    };
    const seen: unknown[] = [];
    for (const world of RED_WORLDS) {
      const run = redFixture(world);
      const required = ["pr", "checks", String(RED_PR), "--required"];

      const before = await gh(run, required);
      const every = await gh(run, ["pr", "checks", String(RED_PR)]);
      await gh(run, ["run", "rerun", String(RED_RUN_ID), "--failed"]);
      const after = await gh(run, required);

      // Each route by the read it answers: the required set, the head's checks, the press.
      const names = new Map([
        [routeOf(run, (route) => route.match.includes("--required")), "required"],
        [
          routeOf(
            run,
            (route) => route.head_read === "checks" && !route.match.includes("--required")
          ),
          "checks",
        ],
        [
          routeOf(
            run,
            (route) => route.publish !== undefined && route.match.startsWith("^gh run rerun")
          ),
          "press",
        ],
      ]);
      seen.push({
        world,
        before,
        every: [
          Object.fromEntries(outputLines(every.stdout).map((line) => line.split("\t").slice(0, 2))),
          every.exit,
        ],
        after,
        routes: ghCalls(run).map((call) => names.get(call.route ?? "") ?? call.route),
      });
    }
    expect(seen).toEqual(
      RED_WORLDS.map((world) => {
        const ci = ciJob[world];
        const url = `${RED_RUN_URL}/job`;
        // The attempt the re-run made of `ci`, which settles as the world settles.
        const settled = headIs(world, "after").ci;
        return {
          world,
          before:
            ci === undefined
              ? none
              : { stdout: `ci\tfail\t20s\t${url}/${ci}\n`, stderr: "", exit: 1 },
          // World `l`'s counted set has not reported its red at the first read.
          every:
            world === "l" ? [{ lint: "pass", typecheck: "pass" }, 0] : [headIs(world, "before"), 1],
          after:
            ci === undefined
              ? none
              : {
                  stdout: `ci\t${settled}\t20s\t${url}/${ci + 2}\n`,
                  stderr: "",
                  exit: settled === "fail" ? 1 : 0,
                },
          routes: ["required", "checks", "press", "required"],
        };
      })
    );
  });

  /** The route of one read of the base's required set in `run`: the one route that takes `gh api
   * GET` of `endpoint` under the world's repository. */
  const requiredSetRoute = (run: string, endpoint: string) =>
    routeOf(run, (route) =>
      new RegExp(route.match).test(`gh api GET repos/${RED_REPO}/${endpoint}`)
    );

  // The five reads of the base's required set read nothing of the world (required_set_fixture),
  // so this row runs that function once, over worker_fixture's green fixture, and the row after
  // it checks that every world's fixture carries them. Catches: a read of the base's required
  // set that another route takes, or that falls to the 404: the two the reviewer's prompt gives
  // (packages/daemon/internal/prompts/go/reviewer.md, "CI is yours to read"), and the protection
  // endpoint, the rulesets list and the one ruleset. In the batch of 2026-11-17 the prompt's two
  // reads, with their `--jq`, took worker_fixture's generic check-runs route, whose match their
  // `required_status_checks` text holds, and exited 1, and the rules read without its `--jq`
  // (trial-i-head-3), the protection read (trial-i-head-1) and the rulesets list (trial-i-head-5)
  // answered the 404, so every reviewer read the base as requiring nothing while `gh pr checks
  // --required` named `ci`. Also a route that takes another's path (the branch route the
  // `/protection` below it, the rulesets list the one ruleset, the branch route the rules path or
  // the reverse), and a required check at another path than the one GitHub answers it at for its
  // endpoint, which the prompt's `--jq` reads as nothing.
  test("the base's rules, rulesets, branch and protection each answer the required ci", async () => {
    const run = builtWorld("green");
    expect(
      bash(
        `${fn("route_defs")}\n${fn("required_set_fixture")}\nR=$1 worker_repo=$2\nrequired_set_fixture ci main $3`,
        [run, RED_REPO, RED_BASE]
      )
    ).toEqual({ code: 0, stdout: "", stderr: "" });
    const rules = `repos/${RED_REPO}/rules/branches/main`;
    const branch = `repos/${RED_REPO}/branches/main`;
    // The prompt's two reads, word for word but for `<o>/<r>` and `<base>`.
    const prompt = [
      await gh(run, [
        "api",
        "--paginate",
        rules,
        "--jq",
        '.[] | select(.type=="required_status_checks") | .parameters.required_status_checks[].context',
      ]),
      await gh(run, ["api", branch, "--jq", ".protection.required_status_checks.checks[].context"]),
    ];
    /** One read of `endpoint` with no `--jq`, which answers JSON on stdout alone and exits 0. */
    const raw = async (endpoint: string) => {
      const { stdout, stderr, exit } = await gh(run, ["api", endpoint]);
      expect({ endpoint, stderr, exit }).toEqual({ endpoint, stderr: "", exit: 0 });
      return JSON.parse(stdout);
    };
    type Rule = { parameters: { required_status_checks: { context: string }[] } };
    /** `.[].parameters.required_status_checks[].context`, the required contexts of a rule list. */
    const contextsOf = (list: Rule[]) =>
      list.flatMap((rule) => rule.parameters.required_status_checks.map((check) => check.context));
    // Then each raw, then the protection endpoint, the rulesets list and the one ruleset the list
    // names by its id, each read for the required contexts at the path GitHub answers them at.
    const rulesOfBranch: Rule[] = await raw(rules);
    const branchAnswer: { protection: { required_status_checks: { contexts: string[] } } } =
      await raw(branch);
    const protectionAnswer: { required_status_checks: { checks: { context: string }[] } } =
      await raw(`${branch}/protection`);
    const rulesets: { id: number }[] = await raw(`repos/${RED_REPO}/rulesets`);
    const ids = rulesets.map((listed) => listed.id);
    expect(ids).toHaveLength(1);
    const ruleset: { rules: Rule[] } = await raw(`repos/${RED_REPO}/rulesets/${ids[0]}`);
    const ci = { stdout: "ci\n", stderr: "", exit: 0 };
    expect({
      prompt,
      contexts: {
        rules: contextsOf(rulesOfBranch),
        ruleset: contextsOf(ruleset.rules),
        branch: branchAnswer.protection.required_status_checks.contexts,
        protection: protectionAnswer.required_status_checks.checks.map((check) => check.context),
      },
      routes: ghCalls(run).map((call) => call.route),
    }).toEqual({
      prompt: [ci, ci],
      contexts: { rules: ["ci"], ruleset: ["ci"], branch: ["ci"], protection: ["ci"] },
      routes: [
        "rules/branches/main",
        "branches/main",
        "rules/branches/main",
        "branches/main",
        "branches/main/protection",
        "rulesets",
        "rulesets/1",
      ].map((endpoint) => requiredSetRoute(run, endpoint)),
    });
  });

  // Catches: a world whose fixture lacks one of the five reads of the base's required set, which
  // would answer the 404 there, and a fixture that carries them behind another route, where the
  // generic check-runs route takes the prompt's two reads again: red_fixture writes them in every
  // world (required_set_fixture), ahead of every other route.
  test("every world's fixture carries the five reads of the base's required set, ahead of every other route", () => {
    // Which of the five comes first is incidental, since each read lands on a route of its own
    // (the row above), so the first five routes of each world are compared with them sorted.
    const seen = RED_WORLDS.map((world) => {
      const run = redFixture(world);
      const firstFive = routesOf(run)
        .slice(0, 5)
        .map((route) => route.match);
      const required = [
        "rules/branches/main",
        "rulesets",
        "rulesets/1",
        "branches/main",
        "branches/main/protection",
      ].map((endpoint) => requiredSetRoute(run, endpoint));
      return { world, routes: firstFive.sort(), required: required.sort() };
    });
    expect(
      seen.filter(({ routes, required }) => routes.join("\n") !== required.join("\n"))
    ).toEqual([]);
  });

  // Catches: a world i or j whose counted set loses the aggregate the base requires, in any read
  // of it or in the settlement the re-run publishes; an aggregate in another run than its lane's,
  // which the re-run of the lane's run would not reach; and an aggregate that does not settle
  // with its lane, left red by i's green re-run or cleared by j's red one. world.json keeps the
  // lane as the world's check and job, which is what the scorer judges a role's record against.
  test("worlds i and j: the required aggregate ci stands over the lane in every read, and settles with it", async () => {
    const received: { authorization: string | null; url: string; body: unknown }[] = [];
    const server = Bun.serve({
      port: 0,
      hostname: "127.0.0.1",
      fetch: async (request) => {
        received.push({
          authorization: request.headers.get("authorization"),
          url: new URL(request.url).pathname,
          body: await request.json(),
        });
        return Response.json({ ok: true });
      },
    });
    const seen: unknown[] = [];
    try {
      for (const world of ["i", "j"] as const) {
        const run = redFixture(world);
        paneListener(run, `http://127.0.0.1:${server.port}`);
        /** `ci` beside the lane under it, as each read of the head answers the two, with the run
         * each counted check names and every run of `ci` at the commit. */
        const reads = async () => {
          const head = await readHead(run);
          const pair = (verdicts: Record<string, string>) => [
            verdicts.ci,
            verdicts["test (notify)"],
          ];
          const required = await gh(run, ["pr", "checks", String(RED_PR), "--required"]);
          const runOf = await rollupRead(
            run,
            ".data.repository.object.statusCheckRollup.contexts.nodes[] " +
              "| [.name, .checkSuite.workflowRun.databaseId] | @tsv"
          );
          const ci = await gh(run, [
            "api",
            `repos/${RED_REPO}/commits/${RED_HEAD}/check-runs?filter=all`,
            "--jq",
            '.check_runs[] | select(.name == "ci") | [.id, .conclusion, .output.summary] | @tsv',
          ]);
          return {
            rollup: pair(head.rollup),
            graphql: pair(head.graphql),
            checks: pair(head.checks),
            runs: pair(head.checkRuns),
            required: outputLines(required.stdout).map((line) => line.split("\t").slice(0, 2)),
            runOf: outputLines(runOf.stdout),
            ci: outputLines(ci.stdout),
          };
        };
        const before = await reads();
        await gh(run, ["run", "rerun", String(RED_RUN_ID), "--failed"]);
        const after = await reads();
        const { check, job } = JSON.parse(readFileSync(path.join(run, "world.json"), "utf8"));
        seen.push({ world, before, after, check, job });
      }
    } finally {
      server.stop(true);
    }
    const lanes = "1 of 6 lanes failed: test (notify).";
    const runOf = ["ci", "lint", "test (notify)", "typecheck"].map(
      (name) => `${name}\t${RED_RUN_ID}`
    );
    const both = (verdict: string) => ({
      rollup: [verdict, verdict],
      graphql: [verdict, verdict],
      checks: [verdict, verdict],
      runs: [verdict, verdict],
      required: [["ci", verdict]],
      runOf,
    });
    const red = `${RED_CI_ID}\tfailure\t${lanes}`;
    const lane = { check: "test (notify)", job: "test (notify)" };
    expect(seen).toEqual([
      {
        world: "i",
        before: { ...both("fail"), ci: [red] },
        after: {
          ...both("pass"),
          ci: [red, `${RED_CI_ID + 1}\tsuccess\tThe re-run of ci passed.`],
        },
        ...lane,
      },
      {
        world: "j",
        before: { ...both("fail"), ci: [red] },
        after: { ...both("fail"), ci: [red, `${RED_CI_ID + 1}\tfailure\t${lanes}`] },
        ...lane,
      },
    ]);
    expect(received).toEqual([
      envelope(RED_RED_ID + 1, ["ci", "lint", "test (notify)", "typecheck"], [], RED_CI_ID + 1),
      envelope(RED_RED_ID + 1, ["lint", "typecheck"], ["ci", "test (notify)"], RED_CI_ID + 1),
    ]);
  });

  // Catches: a re-run that publishes nothing, which leaves a waiting role asleep, and a
  // settlement whose payload names another head, another pull request or another verdict than
  // the one the role pressed the re-run at.
  test("the re-run publishes the settlement of the head, as the fixture wrote it", async () => {
    const received: { authorization: string | null; url: string; body: unknown }[] = [];
    const server = Bun.serve({
      port: 0,
      hostname: "127.0.0.1",
      fetch: async (request) => {
        received.push({
          authorization: request.headers.get("authorization"),
          url: new URL(request.url).pathname,
          body: await request.json(),
        });
        return Response.json({ ok: true });
      },
    });
    try {
      for (const world of ["a", "b"] as const) {
        const run = redFixture(world);
        paneListener(run, `http://127.0.0.1:${server.port}`);
        const rerun = await gh(run, ["run", "rerun", String(RED_RUN_ID), "--failed"]);
        expect(rerun.exit).toBe(0);
      }
    } finally {
      server.stop(true);
    }
    expect(received).toEqual([
      envelope(RED_RED_ID + 1, ["lint", "test (notify)", "typecheck"], []),
      envelope(RED_RED_ID + 1, ["lint", "typecheck"], ["test (notify)"]),
    ]);
  });

  // Catches: a world.json that loses the world, its check, its settlement, the failing line, the
  // failing test's name, the check paired with the red or a fact of the world, each of which the
  // scorer reads the run's answers against (score.ts, redAtTheHead: the record is judged against
  // `log`, its check against `check` and `partner`, and the role's `dispatch search` against
  // `test`) or run_red_at_the_head runs the world by; and one that loses the base the pull
  // request merges into, its commit, or the pull request's title, which red_fixture reads there.
  test("world.json carries the world, its check, its job, its settlement and its failing line", () => {
    const run = redFixture("g");
    expect(JSON.parse(readFileSync(path.join(run, "world.json"), "utf8"))).toEqual({
      key: RED_KEY,
      repo: RED_REPO,
      pr: RED_PR,
      branch: `legion/${RED_KEY}`,
      head: RED_HEAD,
      base: "main",
      base_sha: RED_BASE,
      title: RED_TITLE,
      world: "g",
      check: "ci",
      job: "test (notify)",
      after: "green",
      run_id: RED_RUN_ID,
      job_id: RED_JOB_ID,
      log: "(fail) notify > retries a refused send [3.00ms]",
      test: "notify > retries a refused send",
      role: "tester",
      phase: "testing",
      handoff: "test",
      turns: 2,
      rerun: true,
      round: "goes-on",
      disposal: true,
      report: false,
      decline_fails: false,
      aggregate: false,
      spent: false,
      by_hand: false,
      over_lane: true,
      partner: "test (notify)",
    });
  });

  // red_world is the one place a world's facts are written: red_fixture copies them into
  // world.json, run_red_at_the_head runs the world by them and score.ts's redAtTheHead scores it
  // by them. Catches: a fact that moves in one world, which every reader would then take, and a
  // world outside the twelve that prints facts at all.
  test("red_world prints each world's facts, and nothing for a world outside the twelve", () => {
    const tester = { role: "tester", phase: "testing", handoff: "test" };
    const reviewer = { role: "reviewer", phase: "reviewing", handoff: "review" };
    const reruns = { rerun: true, turns: 2 };
    const none = { rerun: false, turns: 1, disposal: false };
    const plain = {
      report: false,
      decline_fails: false,
      aggregate: false,
      spent: false,
      by_hand: false,
      over_lane: false,
    };
    const facts: Record<RedWorld, object> = {
      a: { after: "green", ...tester, ...reruns, round: "goes-on", disposal: true, ...plain },
      b: {
        after: "red",
        ...tester,
        ...reruns,
        round: "fails-naming-check",
        disposal: false,
        ...plain,
      },
      c: { after: "green", ...tester, ...reruns, round: "goes-on", disposal: false, ...plain },
      d: { after: "red", ...tester, ...none, round: "fails", ...plain },
      e: { after: "red", ...tester, ...none, round: "fails-spent", ...plain, spent: true },
      f: { after: "red", ...tester, ...none, round: "fails", ...plain },
      g: {
        after: "green",
        ...tester,
        ...reruns,
        round: "goes-on",
        disposal: true,
        ...plain,
        over_lane: true,
      },
      h: { after: "green", ...tester, ...reruns, round: "goes-on", disposal: false, ...plain },
      i: {
        after: "green",
        ...reviewer,
        ...reruns,
        round: "goes-on",
        disposal: true,
        ...plain,
        aggregate: true,
      },
      j: {
        after: "red",
        ...reviewer,
        ...reruns,
        round: "fails-naming-check",
        disposal: false,
        ...plain,
        aggregate: true,
      },
      k: {
        after: "green",
        ...tester,
        ...reruns,
        round: "goes-on",
        disposal: true,
        ...plain,
        decline_fails: true,
        by_hand: true,
      },
      l: {
        after: "green",
        ...tester,
        ...reruns,
        round: "goes-on",
        disposal: true,
        ...plain,
        report: true,
      },
    };
    expect(
      RED_WORLDS.map((world) =>
        JSON.parse(
          bash(`${fn("red_world_table")}\n${fn("red_world")}\nred_world "$1"`, [world]).stdout
        )
      )
    ).toEqual(RED_WORLDS.map((world) => facts[world]));
    expect(bash(`${fn("red_world_table")}\n${fn("red_world")}\nred_world "$1"`, ["z"])).toEqual({
      code: 4,
      stdout: "",
      stderr: "",
    });
  });

  /** Each route of the run's fixtures.json that carries the mark, with its count of replies. */
  const marked = (run: string) => {
    const fixtures: { routes: { match: string; replies?: unknown[]; unreported?: boolean }[] } =
      JSON.parse(readFileSync(path.join(run, "fixtures.json"), "utf8"));
    return fixtures.routes
      .filter((route) => route.unreported === true)
      .map((route) => [route.match, route.replies?.length]);
  };
  /** The routes of the run's fixtures.json that answer the counted set, in the order they stand:
   * every read of the head red_fixture marks `head_read` but the check runs and the check suites
   * of its commit, which hold every run at the commit rather than the counted set, and the
   * `--required` read. */
  const countedOf = (run: string) =>
    routesOf(run)
      .filter(
        (route) =>
          (route.head_read !== undefined &&
            route.head_read !== "check-runs" &&
            route.head_read !== "check-suites") ||
          route.match.includes("--required")
      )
      .map((route) => route.match);

  // Catches: a red_report that trims routes by their shape, which takes any other route of three
  // replies with them, a mark the gh stand-in drops when it writes fixtures.json back (a re-run
  // pressed before the report writes it, as a pull request body edit does), which would leave
  // world l's counted set unreported for good, and a mark in a world the rig reports nothing in.
  test("world l marks the routes red_report trims, and the mark outlives the stand-in's writes", async () => {
    // In the order the routes stand in fixtures.json: worker_fixture's own, each turned red where
    // it stands, and the `--required` read red_fixture adds.
    const run = redFixture("l");
    const counted = countedOf(run);
    expect(RED_WORLDS.filter((world) => marked(redFixture(world)).length > 0)).toEqual(["l"]);

    expect(marked(run)).toEqual(counted.map((match) => [match, 3]));
    await gh(run, ["run", "rerun", String(RED_RUN_ID), "--failed"]);
    expect(marked(run)).toEqual(counted.map((match) => [match, 3]));
    // A listener that is gone: the report is noted and red_report goes on.
    expect(await redReport(run, "http://127.0.0.1:1")).toContain(
      "skill-scenarios: the report of the check reached no listener at http://127.0.0.1:1\n"
    );
    expect(marked(run)).toEqual([]);
    const fixtures: { routes: { match: string; replies?: unknown[] }[] } = JSON.parse(
      readFileSync(path.join(run, "fixtures.json"), "utf8")
    );
    expect(
      counted.map(
        (match) => fixtures.routes.find((route) => route.match === match)?.replies?.length
      )
    ).toEqual(counted.map(() => 2));
  });

  // Catches: a red_report that rewrites fixtures.json without the lock every gh stand-in call
  // holds from its read of the file to its write back: a call between its read and its write would
  // put back the answers the report dropped, or the report would drop the call's count.
  test("red_report rewrites fixtures.json only once it holds the lock a gh call holds", async () => {
    const run = redFixture("l");
    // The lock as a gh call holds it, taken before the report starts: the holder prints once it
    // has the lock, and lets it go when its stdin closes.
    const holder = Bun.spawn(
      ["flock", "--exclusive", path.join(run, "fixtures.lock"), "bash", "-c", "echo held; read -r"],
      { env: childEnv, stdin: "pipe", stdout: "pipe" }
    );
    await holder.stdout.getReader().read();
    const report = redReport(run, "http://127.0.0.1:1");
    // A real-time bound, because what the row shows is a thing that does not happen: the report
    // runs as a process of its own, which no fake clock reaches, and no signal marks a write it
    // never made. Unlocked, its jq and mv take milliseconds, far inside the bound; locked, it
    // writes nothing however long the bound is, so the bound decides no passing run.
    await Bun.sleep(500);
    const whileHeld = {
      marked: marked(run).length,
      reported: existsSync(path.join(run, "reported-at")),
    };
    holder.stdin.end();
    await holder.exited;
    await report;
    expect(whileHeld).toEqual({ marked: countedOf(run).length, reported: false });
    expect({
      marked: marked(run).length,
      reported: existsSync(path.join(run, "reported-at")),
    }).toEqual({ marked: 0, reported: true });
  });
});

// worker_fixture's reads of the repository and the pull request, which the rig answers from what
// the world holds: the remote it pushes, the pull request object, and the jobs red_fixture writes.
// A world here is built as a run builds it, worker_fixture then red_fixture, with jj stood in for.
describe("the reads of the repository and the pull request", () => {
  // The commits the world holds, read from the checkout worker_fixture made them in (its push to
  // the bare remote passes through whatever git is first on PATH).
  const git = (run: string, ...args: string[]) =>
    Bun.spawnSync(["git", "-C", path.join(run, "src"), ...args]).stdout.toString();

  // Catches: a `gh pr list` that answers nothing, or every pull request whatever its filters ask,
  // or in a format gh does not print, which a role reading the head's pull request by its branch
  // takes for none.
  test("gh pr list answers the one open pull request its filters admit, as gh prints it", async () => {
    const run = builtWorld("a");
    const json = await gh(run, ["pr", "list", "--head", `legion/${RED_KEY}`, "--json", "number"]);
    const closed = await gh(run, ["pr", "list", "--state", "closed", "--json", "number"]);
    const text = await gh(run, ["pr", "list"]);
    expect([json.stdout, closed.stdout, text.stdout.split("\t").slice(0, 4)]).toEqual([
      `[{"number":${RED_PR}}]\n`,
      "[]\n",
      [String(RED_PR), RED_TITLE, `legion/${RED_KEY}`, "OPEN"],
    ]);
  });

  // Catches: a diff or a list of files the remote does not hold between the base and the head.
  test("gh pr diff, its names and the files read answer the base..head diff of the remote", async () => {
    const run = builtWorld("a");
    const head = JSON.parse(readFileSync(path.join(run, "world.json"), "utf8")).head;
    const diff = await gh(run, ["pr", "diff", String(RED_PR)]);
    const names = await gh(run, ["pr", "diff", String(RED_PR), "--name-only"]);
    const files = await gh(run, [
      "api",
      `repos/${RED_REPO}/pulls/${RED_PR}/files`,
      "--jq",
      ".[].filename",
    ]);
    expect({ diff: diff.stdout, names: names.stdout, files: files.stdout }).toEqual({
      diff: git(run, "diff", "main", head),
      names: git(run, "diff", "--name-only", "main", head),
      files: git(run, "diff", "--name-only", "main", head),
    });
  });

  // Catches: a job log through the API that names another failure than the world's, a log for a
  // job the world has none of, and a passing job whose log claims a failure.
  test("the job log through the API holds the failing line of a failed job, and 404s another id", async () => {
    const run = builtWorld("a");
    const failing = await gh(run, ["api", `repos/${RED_REPO}/actions/jobs/${RED_JOB_ID}/logs`]);
    const passing = await gh(run, ["api", `repos/${RED_REPO}/actions/jobs/${RED_JOB_ID + 1}/logs`]);
    const other = await gh(run, ["api", `repos/${RED_REPO}/actions/jobs/${RED_RED_ID}/logs`]);
    expect({
      failing: failing.stdout.includes("(fail) notify > retries a refused send"),
      passing: passing.stdout.includes("(fail)") || passing.stdout.includes("##[error]"),
      other: [other.stderr, other.exit],
    }).toEqual({ failing: true, passing: false, other: ["gh: Not Found (HTTP 404)\n", 1] });
  });

  // Catches: a list of comments that answers empty after the role posted one, which the stand-in
  // does not keep, through the REST list or `gh pr view --json comments`.
  test("comments answer none until the role posts one, and are unmodeled after", async () => {
    const run = builtWorld("a");
    const list = ["api", `repos/${RED_REPO}/issues/${RED_PR}/comments`];
    const view = ["pr", "view", String(RED_PR), "--json", "comments"];
    const before = [await gh(run, list), await gh(run, view)];
    await gh(run, ["pr", "comment", String(RED_PR), "--body", "Re-ran the red check."]);
    const after = [await gh(run, list), await gh(run, view)];
    expect({
      before: before.map((read) => [read.stdout, read.exit]),
      after: after.map((read) => [read.stderr, read.exit]),
    }).toEqual({
      before: [
        ["[]", 0],
        ['{"comments":[]}\n', 0],
      ],
      after: [
        [`rig: request not modeled: gh api repos/${RED_REPO}/issues/${RED_PR}/comments\n`, 1],
        [`rig: request not modeled: gh pr view ${RED_PR} --json comments\n`, 1],
      ],
    });
  });

  /** The review a role sends through `gh api`, its body handed over in an --input file. */
  async function postReview(run: string, review: object) {
    const file = path.join(run, `review-${Date.now()}-${Math.random()}.json`);
    writeFileSync(file, JSON.stringify(review));
    return gh(run, [
      "api",
      "--method",
      "POST",
      `repos/${RED_REPO}/pulls/${RED_PR}/reviews`,
      "--input",
      file,
    ]);
  }
  const reviewsOf = async (run: string) =>
    JSON.parse((await gh(run, ["api", `repos/${RED_REPO}/pulls/${RED_PR}/reviews`])).stdout);

  // Catches: a review the reviewer submitted that reads back as none, as another state, on
  // another commit than the head, or under an id the list and the by-id read do not agree on.
  // Before the keep, every reviewer of the round-6, 8 and 9 batches that read back its own review
  // met a list the rig could not answer (72 calls in 29 runs).
  test("a posted APPROVE reads back APPROVED at the head, by the list, by its id and in gh pr view", async () => {
    const run = builtWorld("i");
    const head = JSON.parse(readFileSync(path.join(run, "world.json"), "utf8")).head;
    const posted = await postReview(run, {
      event: "APPROVE",
      body: "Looks right.",
      commit_id: head,
    });
    const sent = JSON.parse(posted.stdout);
    const byId = await gh(run, ["api", `repos/${RED_REPO}/pulls/${RED_PR}/reviews/${sent.id}`]);
    const view = await gh(run, ["pr", "view", String(RED_PR), "--json", "reviews,latestReviews"]);
    const shown = { state: "APPROVED", body: "Looks right.", commit_id: head };
    expect({
      posted: { state: sent.state, body: sent.body, commit_id: sent.commit_id, user: sent.user },
      list: (await reviewsOf(run)).map((review: { id: number }) => review.id),
      byId: JSON.parse(byId.stdout),
      view: JSON.parse(view.stdout),
    }).toEqual({
      posted: { ...shown, user: { login: "rig[bot]" } },
      list: [sent.id],
      byId: sent,
      // gh reads the reviews through GraphQL, which names a bot by its bare login; REST's
      // review above names it rig[bot].
      view: {
        reviews: [
          {
            id: sent.node_id,
            author: { login: "rig" },
            authorAssociation: "NONE",
            body: "Looks right.",
            submittedAt: sent.submitted_at,
            includesCreatedEdit: false,
            reactionGroups: [],
            state: "APPROVED",
            commit: { oid: head },
          },
        ],
        latestReviews: [
          {
            id: "",
            author: { login: "rig" },
            authorAssociation: "NONE",
            body: "Looks right.",
            submittedAt: sent.submitted_at,
            includesCreatedEdit: false,
            reactionGroups: [],
            state: "APPROVED",
            commit: { oid: "" },
          },
        ],
      },
    });
  });

  // Catches: a changes request that reads back as an approval, or as no review, through `gh api`
  // fields or `gh pr review`.
  test("a REQUEST_CHANGES reads back CHANGES_REQUESTED, through gh api fields and gh pr review", async () => {
    const run = builtWorld("j");
    await gh(run, [
      "api",
      "-X",
      "POST",
      `repos/${RED_REPO}/pulls/${RED_PR}/reviews`,
      "-f",
      "event=REQUEST_CHANGES",
      "-f",
      "body=test (notify) is red at the head.",
    ]);
    await gh(run, ["pr", "review", String(RED_PR), "--request-changes", "-b", "Still red."]);
    expect(
      (await reviewsOf(run)).map((review: { state: string; body: string }) => [
        review.state,
        review.body,
      ])
    ).toEqual([
      ["CHANGES_REQUESTED", "test (notify) is red at the head."],
      ["CHANGES_REQUESTED", "Still red."],
    ]);
  });

  // Catches: a second review that replaces the first, or reads back before it.
  test("two posted reviews read back as two, in the order sent, each under its own id", async () => {
    const run = builtWorld("i");
    const first = JSON.parse(
      (await postReview(run, { event: "REQUEST_CHANGES", body: "Red." })).stdout
    );
    const second = JSON.parse((await postReview(run, { event: "APPROVE", body: "Green." })).stdout);
    expect(
      (await reviewsOf(run)).map((review: { id: number; state: string }) => [
        review.id,
        review.state,
      ])
    ).toEqual([
      [first.id, "CHANGES_REQUESTED"],
      [second.id, "APPROVED"],
    ]);
    expect(second.id).not.toBe(first.id);
  });

  // Catches: a review by an id the role never sent answered with one it did send, or with any
  // review at all, before or after the role sends one.
  test("a review by an id no review the role sent has answers GitHub's 404", async () => {
    const run = builtWorld("i");
    const unknown = () => gh(run, ["api", `repos/${RED_REPO}/pulls/${RED_PR}/reviews/6999`]);
    const before = await unknown();
    await postReview(run, { event: "APPROVE", body: "Looks right." });
    const after = await unknown();
    expect([before, after].map((read) => [read.stdout, read.stderr, read.exit])).toEqual([
      githubAnswer("review-404"),
      githubAnswer("review-404"),
    ]);
  });

  /** The inline comments of the pull request, or of one review, as `path line body` rows. */
  const commentRows = async (run: string, endpoint: string) => {
    const read = await gh(run, [
      "api",
      `repos/${RED_REPO}/pulls/${RED_PR}/${endpoint}`,
      "--jq",
      ".[] | [.path, .line, .body, .commit_id, .user.login, .pull_request_review_id] | @tsv",
    ]);
    return [read.stdout, read.exit];
  };

  // Catches: a reviewer's inline comments that read back as none, as fewer, out of order, on
  // another path, line or commit, or under no review, from the pull request's review comments, from
  // the review's own, or as the review threads the reviewer's prompt reads. Three reviewers of the
  // 158 archived runs read their inline comments back (trial-i-ablate-1, trial-j-head-4,
  // trial-j-nos1-3) and one the threads after them (round 8's trial-i-head-5).
  test("a review posted with two inline comments reads back both, from the pull request and the review", async () => {
    const run = builtWorld("j");
    const head = JSON.parse(readFileSync(path.join(run, "world.json"), "utf8")).head;
    const review = JSON.parse(
      (
        await postReview(run, {
          event: "REQUEST_CHANGES",
          body: "test (notify) is red at the head.",
          commit_id: head,
          comments: [
            { path: "greet.ts", line: 2, side: "RIGHT", body: "Trim before the greeting." },
            { path: "greet.test.ts", line: 10, side: "RIGHT", body: "Cover an empty name too." },
          ],
        })
      ).stdout
    );
    const rows =
      `greet.ts\t2\tTrim before the greeting.\t${head}\trig[bot]\t${review.id}\n` +
      `greet.test.ts\t10\tCover an empty name too.\t${head}\trig[bot]\t${review.id}\n`;
    expect([
      await commentRows(run, "comments"),
      await commentRows(run, `reviews/${review.id}/comments`),
    ]).toEqual([
      [rows, 0],
      [rows, 0],
    ]);
    const threads = await gh(run, [
      "api",
      "graphql",
      "-f",
      'query=query($n:Int!){repository(owner:"example",name:"widgets"){pullRequest(number:$n){reviewThreads(first:100){pageInfo{hasNextPage endCursor} nodes{id isResolved comments(last:1){nodes{author{login} body}}}}}}}',
      "-F",
      `n=${RED_PR}`,
      "--jq",
      ".data.repository.pullRequest.reviewThreads.nodes[] | [.isResolved, .comments.nodes[0].author.login, .comments.nodes[0].body] | @tsv",
    ]);
    // GraphQL names the comments' bot author by its bare login, rig (REST names it rig[bot]).
    expect([threads.stdout, threads.exit]).toEqual([
      "false\trig\tTrim before the greeting.\nfalse\trig\tCover an empty name too.\n",
      0,
    ]);
    // A query that asks less is answered no more than it asks, whatever its --jq names.
    const counted = await gh(run, [
      "api",
      "graphql",
      "-f",
      'query=query($n:Int!){repository(owner:"example",name:"widgets"){pullRequest(number:$n){reviewThreads(first:100){totalCount nodes{id isResolved}}}}}',
      "-F",
      `n=${RED_PR}`,
      "--jq",
      '.data.repository.pullRequest.reviewThreads | [.totalCount, (.nodes[0] | keys), has("pageInfo")]',
    ]);
    expect(JSON.parse(counted.stdout)).toEqual([2, ["id", "isResolved"], false]);
  });

  // Catches: a review with no inline comment whose comments read back another review's, or are
  // refused.
  test("a review posted with no inline comment reads back none", async () => {
    const run = builtWorld("i");
    const review = JSON.parse(
      (await postReview(run, { event: "APPROVE", body: "Looks right." })).stdout
    );
    expect([
      await commentRows(run, "comments"),
      await commentRows(run, `reviews/${review.id}/comments`),
    ]).toEqual([
      ["", 0],
      ["", 0],
    ]);
  });

  // Catches: the comments of a review the role never sent answered with another review's, or
  // with none, where GitHub answers its 404; trial-j-ablate-4 of round 9 read review 6001's.
  test("the comments of a review id no review the role sent has answer GitHub's 404", async () => {
    const run = builtWorld("j");
    await postReview(run, {
      event: "COMMENT",
      body: "One line.",
      comments: [{ path: "greet.ts", line: 1, body: "Here." }],
    });
    const unknown = await gh(run, [
      "api",
      `repos/${RED_REPO}/pulls/${RED_PR}/reviews/6001/comments`,
    ]);
    expect([unknown.stdout, unknown.stderr, unknown.exit]).toEqual(
      githubAnswer("review-comments-404")
    );
  });

  /** A GraphQL read of the review threads of the world's pull request whose threads ask `nodes`. */
  const threadsRead = (run: string, nodes: string) =>
    gh(run, [
      "api",
      "graphql",
      "-f",
      `query=query($o:String!,$r:String!,$n:Int!){repository(owner:$o,name:$r){pullRequest(number:$n){reviewThreads(first:100){pageInfo{hasNextPage endCursor} nodes{${nodes}}}}}}`,
      "-F",
      "o=example",
      "-F",
      "r=widgets",
      "-F",
      `n=${RED_PR}`,
    ]);
  const PROMPT_NODES = "id isResolved comments(last:1){nodes{author{login} body}}";

  // Catches: a thread read back where the reviewer opened none, which a reviewer whose prompt
  // reads the threads before it approves would take for a conversation left open.
  test("a review with no inline comment reads back no review thread", async () => {
    const run = builtWorld("i");
    await postReview(run, { event: "APPROVE", body: "Looks right." });
    const read = await threadsRead(run, PROMPT_NODES);
    expect([JSON.parse(read.stdout), read.exit]).toEqual([
      {
        data: {
          repository: {
            pullRequest: {
              reviewThreads: { pageInfo: { hasNextPage: false, endCursor: null }, nodes: [] },
            },
          },
        },
      },
      0,
    ]);
  });

  // Catches: inline comments that open no thread, one thread for both, a thread read back as
  // resolved, or a thread whose last comment is another than the one the reviewer wrote. Round
  // 8's trial-i-head-5 read the threads after its inline review.
  test("a review with two inline comments reads back two unresolved threads, each with its comment", async () => {
    const run = builtWorld("j");
    await postReview(run, {
      event: "REQUEST_CHANGES",
      body: "test (notify) is red at the head.",
      comments: [
        { path: "greet.ts", line: 2, body: "Trim before the greeting." },
        { path: "greet.test.ts", line: 10, body: "Cover an empty name too." },
      ],
    });
    const read = await threadsRead(run, PROMPT_NODES);
    const threads = JSON.parse(read.stdout).data.repository.pullRequest.reviewThreads;
    expect([
      read.exit,
      threads.pageInfo.hasNextPage,
      threads.nodes.map((thread: { isResolved: boolean; comments: unknown }) => [
        thread.isResolved,
        thread.comments,
      ]),
      new Set(threads.nodes.map((thread: { id: string }) => thread.id)).size,
    ]).toEqual([
      0,
      false,
      [
        [false, { nodes: [{ author: { login: "rig" }, body: "Trim before the greeting." }] }],
        [false, { nodes: [{ author: { login: "rig" }, body: "Cover an empty name too." }] }],
      ],
      2,
    ]);
  });

  // Catches: a threads query asking fields the world does not hold of a thread (its path and
  // line) answered with the fields it does hold, a trimmed guess, where the rig has no answer;
  // GraphQL answers the selection the world holds, and refuses the rest (gh-graphql.ts).
  test("a threads query asking a field the world does not hold of a thread is refused as unmodeled", async () => {
    const run = builtWorld("j");
    await postReview(run, {
      event: "REQUEST_CHANGES",
      body: "test (notify) is red at the head.",
      comments: [{ path: "greet.ts", line: 2, body: "Trim before the greeting." }],
    });
    const read = await threadsRead(run, "id isResolved path line");
    expect([
      read.stdout,
      read.stderr.startsWith("rig: request not modeled: gh api graphql "),
      read.exit,
    ]).toEqual(["", true, 1]);
  });

  // Catches: a reviewer that reads its own login through GraphQL and is answered another than
  // the one `gh api user` answers, the login every kept review carries, or is refused.
  test("the GraphQL viewer's login is the login gh api user answers", async () => {
    const run = builtWorld("i");
    const viewer = await gh(run, [
      "api",
      "graphql",
      "-f",
      "query={viewer{login}}",
      "--jq",
      ".data.viewer.login",
    ]);
    const user = await gh(run, ["api", "user", "--jq", ".login"]);
    expect([viewer.stdout, viewer.exit]).toEqual([user.stdout, 0]);
    expect(user.stdout).toBe("rig[bot]\n");
  });

  // Catches: `gh --version` refused, and a version other than the one the rig measured gh by.
  test("gh --version answers gh 2.98.0", async () => {
    expect((await gh(builtWorld("a"), ["--version"])).stdout).toBe(
      "gh version 2.98.0 (2026-08-20)\nhttps://github.com/cli/cli/releases/tag/v2.98.0\n"
    );
  });

  // Catches: a GraphQL read of the pull request answered from fields the object does not hold,
  // and one that asks a field the rig cannot state (its reviews) answered at all.
  test("a GraphQL read of the pull request answers the fields its object holds, and no other", async () => {
    const run = builtWorld("a");
    const query = (fields: string) =>
      `query=query{repository(owner:"example",name:"widgets"){pullRequest(number:${RED_PR}){${fields}}}}`;
    const held = await gh(run, [
      "api",
      "graphql",
      "-f",
      query("headRefName reviewDecision"),
      "--jq",
      ".data.repository.pullRequest | [.headRefName, .reviewDecision]",
    ]);
    const reviews = await gh(run, [
      "api",
      "graphql",
      "-f",
      query("reviews(last:10){nodes{state}}"),
    ]);
    expect([
      JSON.parse(held.stdout),
      reviews.exit,
      reviews.stderr.startsWith("rig: request not modeled"),
    ]).toEqual([[`legion/${RED_KEY}`, null], 1, true]);
  });

  // Catches: a read of the base, the repository, a tree, a ref or the root contents answered from
  // anything but the remote the fixture pushed, and a branch the remote has not got answered.
  test("the base, the repository, its trees, refs and contents answer the remote", async () => {
    const run = builtWorld("a");
    /** One read of `endpoint` under the repository, or of the repository itself where it is "". */
    const read = async (endpoint: string, filter: string) =>
      (
        await gh(run, [
          "api",
          endpoint === "" ? `repos/${RED_REPO}` : `repos/${RED_REPO}/${endpoint}`,
          "--jq",
          filter,
        ])
      ).stdout.trim();
    const trunk = await gh(run, ["api", `repos/${RED_REPO}/branches/trunk`]);
    expect({
      base: await read("commits/main", ".sha"),
      branch: await read("", ".default_branch"),
      tree: await read("git/trees/main?recursive=1", ".tree[].path"),
      ref: await read("git/ref/heads/main", ".object.sha"),
      contents: await read("contents/?ref=main", ".[].name"),
      rules: await read("rules/branches/trunk", "length"),
      trunk: [trunk.stderr, trunk.exit],
    }).toEqual({
      base: git(run, "rev-parse", "main").trim(),
      branch: "main",
      tree: git(run, "ls-tree", "-r", "--name-only", "main").trim(),
      ref: git(run, "rev-parse", "main").trim(),
      contents: git(run, "ls-tree", "--name-only", "main").trim(),
      rules: "0",
      trunk: ["gh: Branch not found (HTTP 404)\n", 1],
    });
  });

  // Catches: a read of another repository answered with this world's, which a role that named the
  // wrong repository takes for its own, and that refusal counted as unmodeled.
  test("a repository other than the world's is refused as GitHub and gh refuse it", async () => {
    const run = builtWorld("a");
    const rest = await gh(run, ["api", "repos/skills-rig/project/branches/main"]);
    const list = await gh(run, ["pr", "list", "--repo", "skills-rig/project"]);
    const view = await gh(run, [
      "run",
      "view",
      String(RED_RUN_ID),
      "--repo",
      "skills-rig/project",
      "--log-failed",
    ]);
    expect([rest, list, view].map((read) => [read.stderr, read.exit])).toEqual([
      ["gh: Not Found (HTTP 404)\n", 1],
      [
        "GraphQL: Could not resolve to a Repository with the name 'skills-rig/project'. (repository)\n",
        1,
      ],
      [
        `failed to get run: HTTP 404: Not Found (https://api.github.com/repos/skills-rig/project/actions/runs/${RED_RUN_ID}?exclude_pull_requests=true)\n`,
        1,
      ],
    ]);
    expect(ghCalls(run).some((call) => "unmodeled" in call)).toBe(false);
  });
});

// Catches: a wake the rig gives for a re-run nobody pressed. `gh run rerun --help`, with or
// without the run's id, and a re-run gh refuses with its usage press nothing (red_fixture answers
// each with gh's own text and publishes nothing), so a rerun_pressed that took one for the press
// would start the role's second turn on a settlement that never came. rerun_pressed waits for the
// record of a call whose route published the head's settlement and whose listener took it
// (gh-standin.ts, `published` with a 2xx status). Catches too (F1's nuance, the review of
// 76cc2379): a press whose publish no listener took (`published: {error}`) or the listener
// refused, which woke the second turn on a settlement that never came.
test("rerun_pressed takes the call that published the head's settlement, and no other re-run", async () => {
  let status = 503;
  const server = Bun.serve({
    port: 0,
    hostname: "127.0.0.1",
    fetch: () => Response.json({ ok: status === 200 }, { status }),
  });
  try {
    const run = redFixture("a");
    const pressed = () => bash(`${fn("rerun_pressed")}\nR=$1\nrerun_pressed 0`, [run]).code;
    const press = () => gh(run, ["run", "rerun", String(RED_RUN_ID), "--failed"]);
    await gh(run, ["run", "rerun", "--help"]);
    await gh(run, ["run", "rerun", String(RED_RUN_ID), "--help"]);
    await gh(run, ["run", "rerun", "--failed"]);
    const before = pressed();
    await press();
    const unheard = pressed();
    paneListener(run, `http://127.0.0.1:${server.port}`);
    await press();
    const refused = pressed();
    status = 200;
    await press();
    expect([before, unheard, refused, pressed()]).toEqual([1, 1, 1, 0]);
  } finally {
    server.stop(true);
  }
});

// run_red_at_the_head with every process it starts stood in for: the run's Dispatch, the seeds,
// both fixtures, the rig's own report of world `l`'s check (red_report), the daemon stand-in
// (start_process), the worker pane (`bun`) and the turns (launch). Each stand-in records its
// call, so a row reads what the run passed on. The red_fixture stand-in writes what the real one
// writes to world.json that the run reads: the world's facts, from the rig's own red_world, and
// its check.
describe("run_red_at_the_head", () => {
  const standIns = `
own_dispatch() { dispatch_port=5555; }
seed_ts() {
  printf 'seed %s\\n' "$*" >>"$R/calls"
  [ "$1" != tester-proof ] || printf '{"issue":"${RED_KEY}","implement":{"version":4}}\\n' >"$2"
}
worker_fixture() {
  printf 'worker_fixture %s\\n' "$*" >>"$R/calls"
  jq -n '{pr:${RED_PR}, head:"${RED_HEAD}", branch:"legion/${RED_KEY}"}' >"$R/world.json"
}
write_facts() {
  jq --argjson facts "$(red_world "$1")" --arg world "$1" \\
    '. + $facts + {check:(${JSON.stringify(Object.fromEntries(RED_WORLDS.map((world) => [world, RED_WORLD[world].check])))})[$world]}' \\
    "$R/world.json" >"$R/world.red.json"
  mv "$R/world.red.json" "$R/world.json"
}
red_fixture() { printf 'red_fixture %s\\n' "$*" >>"$R/calls"; write_facts "$1"; }
start_process() {
  printf 'start_process %s\\n' "$*" >>"$R/calls"
  daemon_pid=4242
  printf 'listening on http://127.0.0.1:4321\\n' >"$R/logs/daemon.log"
}
await_start() { printf 'await_start %s\\n' "$1" >>"$R/calls"; }
base_env() { : >"$R/worker.env"; }
standins() { :; }
bun() { printf 'bun %s\\n' "$*" >>"$R/calls"; }
launch() { printf 'launch %s\\n' "$*" >>"$R/calls"; printf 'exit=0\\n' >"$R/out.txt"; }
rerun_pressed() { printf 'rerun_pressed %s\\n' "$*" >>"$R/calls"; }
red_report() { printf 'red_report\\n' >>"$R/calls"; }`;

  /** The script of one run of world $2 in the run directory $1: run_red_at_the_head over the
   * stand-ins above and then `extra`, where a row stands in for something more, with `here` as the
   * rig's own directory. */
  const redRunScript = (here: string, extra: string[] = []) =>
    [
      fn("fail"),
      fn("note"),
      // The helpers run_red_at_the_head shares with run_tester_proof: the seeds and the world's
      // read (worker_world), the architect's assignment (architect_assignment), the pane and the
      // daemon stand-in (boot_worker), and each turn's line of turns.txt (record_turn); and the
      // world's facts (red_world), which worker_world seeds the issue by.
      fn("worker_world"),
      fn("red_world_table"),
      fn("red_world"),
      fn("architect_assignment"),
      fn("boot_worker"),
      fn("record_turn"),
      fn("run_red_at_the_head"),
      standIns,
      ...extra,
      "R=$1",
      "work=$R/work",
      "P=$work/profiles/s1",
      'mkdir -p "$P"',
      "profile=$P/home",
      "co=/checkout",
      `here=${here}`,
      `worker_repo=${RED_REPO}`,
      "services_env=$R/services.env",
      ': >"$services_env"',
      'run_red_at_the_head "trial-$2-s1-2" 2 3 1740000000 "$2"',
    ].join("\n");

  /** One run of `world`, and what it took: the world it wrote down, the role each worker it
   * started was given, the turns it ran and what woke each, and whether the rig reported the
   * check between them. */
  function redRun(world: RedWorld) {
    const run = freshDir(`red-run-${world}`);
    mkdirSync(path.join(run, "logs"));
    const result = bash(redRunScript("/rig"), [run, world]);
    if (result.code !== 0) throw new Error(`run_red_at_the_head ${world}: ${result.stderr}`);
    const calls = readFileSync(path.join(run, "calls"), "utf8").trimEnd().split("\n");
    return {
      // The world the run hands red_fixture, which writes it to world.json as `world`.
      world: calls.find((call) => call.startsWith("red_fixture "))?.split(" ")[1],
      // The daemon stand-in takes the role last, the worker pane before its checkout.
      daemons: calls
        .filter((call) => call.includes("daemon-standin.ts"))
        .map((call) => call.split(" ").at(-1)),
      panes: calls
        .filter((call) => call.includes("worker-pane.ts"))
        .map((call) => call.split(" ").at(-2)),
      turns: readFileSync(path.join(run, "turns.txt"), "utf8").trimEnd().split("\n"),
      outs: ["out-1.txt", "out-2.txt"].filter((out) => existsSync(path.join(run, out))),
      reported: calls.includes("red_report"),
    };
  }

  /** One line of turns.txt: the turn, what woke it, the moment it ended and its exit line. */
  const turnLine = (turn: number, wake: string) =>
    expect.stringMatching(
      new RegExp(`^${turn} ${wake} \\d{4}-\\d{2}-\\d{2}T\\d{2}:\\d{2}:\\d{2}Z exit=0$`)
    );

  // Catches: a run that boots the tester in a reviewer's world, one that boots both roles, a
  // world.json the scorer reads as another world than the one the run ran, a world that takes a
  // turn it owes no wake for, and a world `l` whose second turn is a re-run's settlement rather
  // than the rig's own report of the check.
  test("boots one worker, takes each world's turns, and reports world l's check between them", () => {
    expect(RED_WORLDS.map(redRun)).toEqual(
      RED_WORLDS.map((world) => {
        const role = world === "i" || world === "j" ? "reviewer" : "tester";
        // `d`, `e` and `f` expect no re-run and take one turn; `l` takes its second on the rig's
        // report of the check, and every other world on the settlement of the re-run it pressed.
        const one = world === "d" || world === "e" || world === "f";
        return {
          world,
          daemons: [role],
          panes: [role],
          turns: one
            ? [turnLine(1, "assignment")]
            : [turnLine(1, "assignment"), turnLine(2, world === "l" ? "report" : "settlement")],
          outs: one ? ["out-1.txt"] : ["out-1.txt", "out-2.txt"],
          reported: world === "l",
        };
      })
    );
  });

  // The text each turn of a world gives its agent, which no fact of the world may change. Catches:
  // an assignment of another role's phase, a told line in a world whose counted set reports its
  // red, and a resume task of another phase.
  test("each world's prompts are the architect's assignment and the resume task, word for word", () => {
    const prompts = RED_WORLDS.map((world) => {
      const run = freshDir(`red-prompts-${world}`);
      mkdirSync(path.join(run, "logs"));
      const launch = `launch() { cat "$R/prompt.txt" >>"$R/prompts"; printf 'exit=0\\n' >"$R/out.txt"; }`;
      const result = bash(redRunScript("/rig", [launch]), [run, world]);
      if (result.code !== 0) throw new Error(`run_red_at_the_head ${world}: ${result.stderr}`);
      return readFileSync(path.join(run, "prompts"), "utf8");
    });
    const pr = `PR #${RED_PR} (https://github.com/${RED_REPO}/pull/${RED_PR}, repository ${RED_REPO}) is at ${RED_HEAD}.`;
    const handoff = `Handoff: implement version 4 (dispatch handoff-read --issue ${RED_KEY} --phase implement --version 4).`;
    expect(prompts).toEqual(
      RED_WORLDS.map((world) => {
        const reviewer = world === "i" || world === "j";
        const told = world === "l" ? " The daemon reports test (notify) red at that head." : "";
        const assignment = reviewer
          ? `Architect → reviewer, ${RED_KEY}. The tester has finished: ${pr}${told} ${handoff} Tester's verdict: pass. Review it and finish your phase.\n`
          : `Architect → tester, ${RED_KEY}. The implementer has finished: ${pr}${told} ${handoff} Test it and finish your phase.\n`;
        const resume = `Continue greet(): a greeting helper for the widgets CLI. Issue: ${RED_KEY}. Phase: ${reviewer ? "reviewing" : "testing"}. Resume the existing phase work.\n`;
        return world === "d" || world === "e" || world === "f" ? assignment : assignment + resume;
      })
    );
  });

  // Catches: a run whose fixtures.json the gh stand-in refuses, which would boot its worker and
  // answer each of the role's `gh` calls with the refusal, a head that never turns scored as the
  // role's failure. worker_world runs the stand-in's own parse once the fixtures are written, so
  // that run stops before it boots anything and leaves no exit= line in out.txt, which the scorer
  // reports as a rig error. The same run with its `follows` spelled as the route's match boots.
  test("a fixture the gh stand-in refuses stops the run before it boots a worker", () => {
    // red_fixture as a typo in its jq would leave the routes, and the real `bun` for the
    // stand-in's own parse; every other `bun` call is recorded, as above.
    const extra = [
      `red_fixture() { printf 'red_fixture %s\\n' "$*" >>"$R/calls"; cp "$R/red.json" "$R/fixtures.json"; write_facts "$1"; }`,
      `bun() { if [ "$2" = --check ]; then command bun "$@"; else printf 'bun %s\\n' "$*" >>"$R/calls"; fi; }`,
    ];
    const seen = ["^gh run rerunn", "^gh run rerun"].map((follows) => {
      const run = freshDir("red-run-checked");
      mkdirSync(path.join(run, "logs"));
      writeFileSync(
        path.join(run, "red.json"),
        JSON.stringify({
          routes: [
            { match: "^gh pr checks", replies: [{ stdout: "test\tfail", exit: 1 }, {}], follows },
            { match: "^gh run rerun", stdout: "✓ Requested rerun of failed jobs" },
          ],
        })
      );
      const result = bash(redRunScript(import.meta.dir, extra), [run, "a"]);
      const calls = readFileSync(path.join(run, "calls"), "utf8");
      return {
        code: result.code,
        stderr: result.stderr,
        log: readFileSync(path.join(run, "fixture.log"), "utf8"),
        fixtured: calls.includes("red_fixture a\n"),
        booted: calls.includes("start_process daemon") || calls.includes("launch "),
        finished: existsSync(path.join(run, "out.txt")),
        run,
      };
    });
    const [refused, spelled] = seen;
    expect(refused).toEqual({
      code: 1,
      stderr:
        `skill-scenarios: the gh stand-in refuses ${refused?.run}/fixtures.json; ` +
        `see ${refused?.run}/fixture.log\n`,
      log:
        `gh-standin: ${refused?.run}/fixtures.json is refused:\n` +
        `✖ route "^gh pr checks" follows "^gh run rerunn", which is no route's match\n` +
        "  → at routes[0].follows\n",
      fixtured: true,
      booted: false,
      finished: false,
      run: expect.any(String),
    });
    expect(spelled).toEqual({
      code: 0,
      stderr: "",
      log: "",
      fixtured: true,
      booted: true,
      finished: true,
      run: expect.any(String),
    });
  });
});

// cmd_run and cmd_batch read SKILL_SCENARIOS_RED_WORLD and take it only where red_world prints its
// facts: the run function is stood in for, so a row reads the name the command made and what it
// refused, and a row may stand in for red_world too, with `worlds` in place of rig.sh's own.
describe("a red-at-the-head run's world", () => {
  const runStandIns = `
load_label() { P=$work/profiles/$1; mkdir -p "$P"; printf 'commit\\n' >"$P/built-from"; }
on_exit() { :; }
run_red_at_the_head() {
  printf 'run_red_at_the_head %s\\n' "$*" >>"$work/calls"
  printf 'exit=0\\n' >"$R/out.txt"
}`;
  const run = (
    work: string,
    world: string | undefined,
    n: string,
    worlds = `${fn("red_world_table")}\n${fn("red_world")}\n${fn("red_worlds")}`
  ) =>
    bash(
      [
        fn("fail"),
        fn("cmd_run"),
        fn("run_prefix"),
        worlds,
        runStandIns,
        "work=$1",
        'mkdir -p "$work"',
        "services_env=$work/services.env",
        ': >"$services_env"',
        "shift",
        'cmd_run "$@"',
      ].join("\n"),
      [work, "red-at-the-head", "s1", n, "3", "1740000000"],
      world === undefined ? {} : { SKILL_SCENARIOS_RED_WORLD: world }
    );

  // Catches: a run named after its scenario and label alone, whose directory a second world's
  // batch would remove (cmd_run starts from rm -rf), and a world the run passes on by another
  // name than the one it was named for.
  test("the run's name and directory carry its world, so two worlds keep their own runs", () => {
    const work = freshDir("red-run-work");
    expect(run(work, "a", "1").stdout).toBe("trial-a-s1-1: exit=0\n");
    expect(run(work, "b", "1").stdout).toBe("trial-b-s1-1: exit=0\n");
    expect(readFileSync(path.join(work, "calls"), "utf8")).toBe(
      "run_red_at_the_head trial-a-s1-1 1 3 1740000000 a\n" +
        "run_red_at_the_head trial-b-s1-1 1 3 1740000000 b\n"
    );
    expect(existsSync(path.join(work, "runs/trial-a-s1-1"))).toBe(true);
    expect(existsSync(path.join(work, "runs/trial-b-s1-1"))).toBe(true);
  });

  // A red-at-the-head run's pane.env (base_env) holds every agent-visible path — HOME, TMPDIR,
  // PATH and SKILL_SCENARIO_RUN — and each lives under the run directory, named by run_prefix.
  // The prefix is `trial`, off the page's own name, so no such path names the page under test.
  // Catches: a run directory, or a pane.env value, that names skills/legion-worker/references/
  // red-at-the-head.md — which, in an agent-visible path, cues the change arm (119 of 120
  // record-batch runs opened the page before any CI read).
  test("no agent-visible path of a red-at-the-head run names the page it tests", () => {
    const work = freshDir("red-pane-work");
    const result = bash(
      [
        fn("fail"),
        fn("base_env"),
        fn("cmd_run"),
        fn("run_prefix"),
        fn("red_world_table"),
        fn("red_world"),
        'load_label() { P=$work/profiles/$1; mkdir -p "$P/home"; printf \'commit\\n\' >"$P/built-from"; }',
        "on_exit() { :; }",
        "run_red_at_the_head() { base_env; printf 'exit=0\\n' >\"$R/out.txt\"; }",
        "work=$1",
        'mkdir -p "$work"',
        "services_env=$work/services.env",
        ': >"$services_env"',
        "shift",
        'cmd_run "$@"',
      ].join("\n"),
      [work, "red-at-the-head", "s1", "1", "3", "1740000000"],
      { SKILL_SCENARIOS_RED_WORLD: "a" }
    );
    expect(result.code).toBe(0);
    const made = readdirSync(path.join(work, "runs"));
    expect(made).toHaveLength(1);
    const runName = made[0] ?? "";
    expect(runName).not.toContain("red-at-the-head");
    const env = readFileSync(path.join(work, "runs", runName, "pane.env"), "utf8").trimEnd();
    for (const line of env.split("\n")) {
      expect(line.slice(line.indexOf("=") + 1)).not.toContain("red-at-the-head");
    }
  });

  // Catches: a run of a world red_fixture has no red for, which would fail inside the run with
  // its services up, and a run named `trial--s1-1` when the variable is unset.
  test("cmd_run refuses a world that is not one of the twelve, naming them, and makes no run", () => {
    const work = freshDir("red-run-refused");
    const named = "not a, b, c, d, e, f, g, h, i, j, k or l";
    const unknown = run(work, "z", "1");
    expect(unknown.code).toBe(1);
    expect(unknown.stderr).toBe(`skill-scenarios: SKILL_SCENARIOS_RED_WORLD is 'z', ${named}\n`);
    const unset = run(work, undefined, "1");
    expect(unset.code).toBe(1);
    expect(unset.stderr).toBe(`skill-scenarios: SKILL_SCENARIOS_RED_WORLD is '', ${named}\n`);
    expect(existsSync(path.join(work, "runs"))).toBe(false);
  });

  const batchStandIns = `
load_label() { printf 'load_label %s\\n' "$1" >>"$work/calls"; }
on_exit() { :; }
lock_services() { printf 'lock_services\\n' >>"$work/calls"; }
services_up() { printf 'services_up\\n' >>"$work/calls"; }
warm_daemon_panes() { printf 'warm_daemon_panes\\n' >>"$work/calls"; }`;
  /** cmd_batch of two runs of label s1 in `scenario` over the stand-ins above, with
   * SKILL_SCENARIOS_RED_WORLD set to `world`, and the calls it made. */
  function batch(
    world: string | undefined,
    worlds = `${fn("red_world_table")}\n${fn("red_world")}\n${fn("red_worlds")}`,
    scenario = "red-at-the-head"
  ) {
    const work = freshDir("red-batch-work");
    const here = freshDir("red-batch-here");
    writeFileSync(
      path.join(here, "rig.sh"),
      `#!/usr/bin/env bash\nprintf 'run %s\\n' "$*" >>"${path.join(work, "calls")}"\n`
    );
    const result = bash(
      [
        fn("fail"),
        fn("cmd_batch"),
        worlds,
        batchStandIns,
        "work=$1",
        "here=$2",
        "shift 2",
        'cmd_batch "$@"',
      ].join("\n"),
      [work, here, scenario, "2", "s1"],
      world === undefined ? {} : { SKILL_SCENARIOS_RED_WORLD: world }
    );
    const calls = existsSync(path.join(work, "calls"))
      ? readFileSync(path.join(work, "calls"), "utf8").trimEnd().split("\n")
      : [];
    // The batch's start time is the clock's, so each run line is read without it. cmd_batch hands
    // the runs to `xargs -P`, which starts them at once, so the order of their lines is the
    // scheduler's: they are sorted, and the calls before them keep the order the batch made them in.
    const lines = calls.map((line) => line.split(" ").slice(0, 5).join(" "));
    const runs = lines.filter((line) => line.startsWith("run run "));
    return {
      ...result,
      calls: [...lines.filter((line) => !line.startsWith("run run ")), ...runs.sort()],
    };
  }

  // Catches: a batch that starts its services and only then finds it has no world, and a gate
  // that refuses the twelve worlds too.
  test("cmd_batch refuses such a world before it locks or starts a service", () => {
    const refused = batch("world-a");
    expect(refused.code).toBe(1);
    expect(refused.stderr).toBe(
      "skill-scenarios: a red-at-the-head batch needs SKILL_SCENARIOS_RED_WORLD set to " +
        "a, b, c, d, e, f, g, h, i, j, k or l (got 'world-a')\n"
    );
    expect(refused.calls).toEqual([]);
    const ran = batch("l");
    expect(ran.code).toBe(0);
    expect(ran.calls).toEqual([
      "load_label s1",
      "lock_services",
      "services_up",
      "warm_daemon_panes",
      "run run red-at-the-head s1 1",
      "run run red-at-the-head s1 2",
    ]);
  });

  // Catches: a batch that compiles daemon-pane.go in every daemon module for a scenario whose
  // runs boot no worker (boot_worker), and a tester-proof batch that skips the build its runs'
  // daemon stand-ins need. Askgate and the two spec scenarios need settings of their own before a
  // batch of them starts, so the row takes the scenarios a batch runs with nothing more.
  test("cmd_batch warms the daemon panes for tester-proof and red-at-the-head alone", () => {
    const scenarios = [
      "ask-on-message",
      "measure-before-ask",
      "tester-proof",
      "brainstorm",
      "red-at-the-head",
    ];
    const boots = ["tester-proof", "red-at-the-head"];
    expect(
      scenarios.map((scenario) => {
        const { code, calls } = batch("a", undefined, scenario);
        return { scenario, code, calls };
      })
    ).toEqual(
      scenarios.map((scenario) => ({
        scenario,
        code: 0,
        calls: [
          "load_label s1",
          "lock_services",
          "services_up",
          ...(boots.includes(scenario) ? ["warm_daemon_panes"] : []),
          `run run ${scenario} s1 1`,
          `run run ${scenario} s1 2`,
        ],
      }))
    );
  });

  // Catches: a list of worlds in cmd_run or cmd_batch beside red_world's table, which a world added
  // to the table would be missing from: here red_world knows `m` alone.
  test("cmd_run and cmd_batch take the worlds red_world knows, and keep no list of their own", () => {
    const worlds = `red_world() { [ "$1" = m ] && echo '{"role":"tester"}'; }\nred_worlds() { echo m; }`;
    const work = freshDir("red-run-catalogue");
    expect(run(work, "m", "1", worlds)).toEqual({
      code: 0,
      stdout: "trial-m-s1-1: exit=0\n",
      stderr: "",
    });
    const refused = run(work, "a", "1", worlds);
    expect(refused.code).toBe(1);
    // The refusal names the worlds red_worlds reads from the table, not a list of its own.
    expect(refused.stderr).toBe("skill-scenarios: SKILL_SCENARIOS_RED_WORLD is 'a', not m\n");
    expect(readFileSync(path.join(work, "calls"), "utf8")).toBe(
      "run_red_at_the_head trial-m-s1-1 1 3 1740000000 m\n"
    );
    const ran = batch("m", worlds);
    expect({ code: ran.code, calls: ran.calls }).toEqual({
      code: 0,
      calls: [
        "load_label s1",
        "lock_services",
        "services_up",
        "warm_daemon_panes",
        "run run red-at-the-head s1 1",
        "run run red-at-the-head s1 2",
      ],
    });
    expect(batch("a", worlds)).toMatchObject({ code: 1, calls: [] });
  });
});

// The variables a Legion pane exports for the live services, each set to a value of the caller's
// own, which nothing the rig starts may be handed: the rig's own Envoy is the one it writes into a
// run's pane.env, and its Dispatch the run's own server (rig.sh's start, base_env, boot_worker).
describe("the caller's Envoy, Dispatch and NATS", () => {
  // Catches: a gh stand-in that hands the Envoy, Dispatch or NATS of its own environment to a
  // route's program or a call's --jq filter, or that publishes a fixture's settlement to the
  // listener its own environment names rather than the run's. A stand-in run by hand from a Legion
  // pane on 2026-11-18 published a settlement to the live relay that way (topic
  // notifications.github.example.widgets.pr.1003.checks). The probe is the program and the filter
  // themselves: each prints every DISPATCH_, ENVOY_ and NATS_ variable of its own environment.
  test("reach no program, filter or publish of the gh stand-in, which publishes to the run's own listener", async () => {
    const reached = { caller: [] as string[], run: [] as string[] };
    const listen = (into: string[]) =>
      Bun.serve({
        port: 0,
        hostname: "127.0.0.1",
        fetch: (request) => {
          into.push(new URL(request.url).pathname);
          return Response.json({ ok: true });
        },
      });
    const callerListener = listen(reached.caller);
    const runListener = listen(reached.run);
    try {
      const run = freshDir("service-variables");
      paneListener(run, `http://127.0.0.1:${runListener.port}`);
      const probe = '$ENV | with_entries(select(.key | test("^(DISPATCH|ENVOY|NATS)_")))';
      writeFileSync(
        path.join(run, "fixtures.json"),
        JSON.stringify({
          routes: [
            {
              match: `^gh api GET repos/${RED_REPO}/environment(?:\\s|$)`,
              stdout: {},
              program: probe,
              publish: { topic: "notifications.github.example.widgets.pr.1003.checks" },
            },
          ],
        })
      );
      const callerToken = path.join(run, "caller-token");
      writeFileSync(callerToken, "caller-bearer\n");
      const caller = {
        ENVOY_URL: `http://127.0.0.1:${callerListener.port}`,
        ENVOY_TOKEN_FILE: callerToken,
        ENVOY_NATS_URL: "nats://caller-nats.invalid:4222",
        ENVOY_CONFIG_FILE: path.join(run, "caller-envoy.json"),
        DISPATCH_URL: `http://127.0.0.1:${callerListener.port}`,
        DISPATCH_HOST: "caller-dispatch.invalid",
        DISPATCH_TOKEN_FILE: callerToken,
        DISPATCH_SESSION_ID: "caller-session",
      };
      const program = await gh(run, ["api", `repos/${RED_REPO}/environment`], caller);
      const filter = await gh(run, ["api", `repos/${RED_REPO}/environment`, "--jq", probe], caller);
      expect({
        program: [program.stdout, program.exit],
        filter: [filter.stdout, filter.exit],
        reached,
      }).toEqual({
        program: ["{}", 0],
        filter: ["{}\n", 0],
        reached: { caller: [], run: ["/v1/messages/publish", "/v1/messages/publish"] },
      });
    } finally {
      callerListener.stop(true);
      runListener.stop(true);
    }
  });
});

// The round-5 review's probes of worker_fixture's and red_fixture's routes (M2 to M5 and its
// third Minor), each a request a route answered with the head's state, or as a request GitHub
// would answer otherwise, before the routes were made to answer only what they name.
describe("a request the routes do not name is answered as GitHub answers it, or refused", () => {
  /** The rollup a GraphQL read answers: its state, null where the commit has none, or the
   * request's exit and stderr where it has no answer. */
  const rollup = async (
    run: string,
    args: string[]
  ): Promise<string | null | { exit: number; stderr: string }> => {
    const read = await gh(run, ["api", "graphql", ...args]);
    if (read.exit !== 0) return { exit: read.exit, stderr: read.stderr };
    const found = /"statusCheckRollup":(null|\{"state":"(\w+)"\})/.exec(read.stdout);
    return found === null ? read.stdout : (found[2] ?? null);
  };
  const query = (selection: string) =>
    `query={repository(owner:"example",name:"widgets"){${selection}}}`;
  const commitRollup = "... on Commit{statusCheckRollup{state}}";

  // Catches (M2): a GraphQL rollup read of the base, by the default branch, its ref, the pull
  // request's base ref or an expression, answered with the head's red, the round-9 class through
  // GraphQL; the pull request's own last commit read as null rather than the head's red; and
  // another pull request answered with this one's.
  test("a GraphQL rollup read answers the head's red only where it names the head", async () => {
    const run = redFixture("a");
    expect({
      defaultBranch: await rollup(run, ["-f", query(`defaultBranchRef{target{${commitRollup}}}`)]),
      mainRef: await rollup(run, [
        "-f",
        query(`ref(qualifiedName:"refs/heads/main"){target{${commitRollup}}}`),
      ]),
      baseRef: await rollup(run, [
        "-f",
        query(`pullRequest(number:${RED_PR}){baseRef{target{${commitRollup}}}}`),
      ]),
      expression: await rollup(run, [
        "-F",
        "ref=main",
        "-f",
        `query=query($ref:String!){repository(owner:"example",name:"widgets"){object(expression:$ref){${commitRollup}}}}`,
      ]),
      lastCommit: await rollup(run, [
        "-f",
        query(
          `pullRequest(number:${RED_PR}){commits(last:1){nodes{commit{statusCheckRollup{state}}}}}`
        ),
      ]),
      headRef: await rollup(run, [
        "-f",
        query(`pullRequest(number:${RED_PR}){headRef{target{${commitRollup}}}}`),
      ]),
      otherPull: await rollup(run, [
        "-f",
        query("pullRequest(number:999){commits(last:1){nodes{commit{statusCheckRollup{state}}}}}"),
      ]),
    }).toEqual({
      defaultBranch: null,
      mainRef: null,
      baseRef: null,
      expression: null,
      lastCommit: "FAILURE",
      headRef: "FAILURE",
      otherPull: {
        exit: 1,
        stderr: "gh: Could not resolve to a PullRequest with the number of 999.\n",
      },
    });
  });

  // Catches (M3): `repos/{owner}/{repo}/...` refused as another repository, where gh fills both
  // from GH_REPO, which the pane sets, before it sends the request.
  test("{owner} and {repo} read the repository GH_REPO names, as gh fills them", async () => {
    const run = redFixture("a");
    const env = { GH_REPO: RED_REPO };
    const pull = await gh(
      run,
      ["api", `repos/{owner}/{repo}/pulls/${RED_PR}`, "--jq", ".head.sha"],
      env
    );
    const runs = await gh(
      run,
      [
        "api",
        `repos/{owner}/{repo}/commits/${RED_HEAD}/check-runs`,
        "--jq",
        ".check_runs | length",
      ],
      env
    );
    expect([pull.stdout, pull.exit, runs.stdout, runs.exit]).toEqual([
      `${RED_HEAD}\n`,
      0,
      "3\n",
      0,
    ]);
  });

  // Catches (M4): a review or a comment sent through `gh api` with fields and no -X, which gh
  // sends as a POST, answered as a read and kept nowhere, so the role's review reads back as none
  // and its comment as never posted.
  test("a write with fields and no -X is the POST gh sends, and is kept as GitHub keeps it", async () => {
    const run = redFixture("i");
    const reviews = `repos/${RED_REPO}/pulls/${RED_PR}/reviews`;
    const sent = await gh(run, [
      "api",
      reviews,
      "-f",
      "event=APPROVE",
      "-f",
      "body=LGTM",
      "--jq",
      ".state",
    ]);
    const listed = await gh(run, ["api", reviews, "--jq", 'map(.state) | join(",")']);
    const viewed = await gh(run, [
      "pr",
      "view",
      String(RED_PR),
      "--json",
      "reviews",
      "--jq",
      '.reviews[] | .state + " " + .author.login',
    ]);
    const comment = await gh(run, [
      "api",
      `repos/${RED_REPO}/issues/${RED_PR}/comments`,
      "-f",
      "body=hello",
    ]);
    const comments = await gh(run, ["pr", "view", String(RED_PR), "--json", "comments"]);
    expect({
      sent: sent.stdout,
      listed: listed.stdout,
      viewed: viewed.stdout,
      comment: comment.exit,
      comments: [comments.stderr, comments.exit],
    }).toEqual({
      sent: "APPROVED\n",
      listed: "APPROVED\n",
      // gh reads a review's author through GraphQL, which names a bot by its bare login.
      viewed: "APPROVED rig\n",
      comment: 0,
      comments: [`rig: request not modeled: gh pr view ${RED_PR} --json comments\n`, 1],
    });
  });

  // Catches (M5): another pull request, a branch or a URL answered with this pull request; a
  // `pulls` query whose filters are ignored; another attempt answered with the newest; and a
  // re-run of another job (lint, which passed) taken as the press of the red's own.
  test("another pull request, filter, attempt or job is refused as gh refuses it, or as unmodeled", async () => {
    const run = redFixture("a");
    const answer = async (args: string[]) => {
      const read = await gh(run, args);
      return [read.stdout, read.stderr, read.exit];
    };
    const unmodeled = (args: string[]) => [
      "",
      `rig: request not modeled: gh ${args.join(" ")}\n`,
      1,
    ];
    const notFound = (n: number) => [
      "",
      `GraphQL: Could not resolve to a PullRequest with the number of ${n}. (repository.pullRequest)\n`,
      1,
    ];
    const other = [
      ["pr", "view", "main", "--json", "title"],
      ["pr", "view", "https://github.com/other/repo/pull/1003", "--json", "title"],
      ["run", "view", String(RED_RUN_ID), "--attempt", "1", "--json", "attempt,conclusion"],
      ["run", "rerun", "--job", String(RED_JOB_ID + 1)],
    ];
    expect({
      pull: await answer(["pr", "view", "999", "--json", "title"]),
      checks: await answer(["pr", "checks", "998"]),
      closed: await answer([
        "api",
        `repos/${RED_REPO}/pulls?head=example:main&state=closed`,
        "--jq",
        "length",
      ]),
      open: await answer([
        "api",
        `repos/${RED_REPO}/pulls?head=example:legion/${RED_KEY}&state=open`,
        "--jq",
        'map(.number) | join(",")',
      ]),
      other: await Promise.all(other.map(answer)),
    }).toEqual({
      pull: notFound(999),
      checks: notFound(998),
      closed: ["0\n", "", 0],
      open: [`${RED_PR}\n`, "", 0],
      other: other.map(unmodeled),
    });
  });

  // Catches (round 5's third Minor): the head's branch named with its slash encoded (`%2F`), as a
  // URL path carries it, answered as a branch GitHub does not have; a comment whose body holds
  // `-R src/dir`-like text refused as another repository; and the job flag written as gh takes it,
  // `-j<id>`, answered with the usage error rather than as the press.
  test("an encoded branch, a body that holds flag text and a joined -j each read as gh sends them", async () => {
    const run = redFixture("a");
    const branch = await gh(run, [
      "api",
      `repos/${RED_REPO}/branches/legion%2F${RED_KEY}`,
      "--jq",
      ".commit.sha",
    ]);
    const comment = await gh(run, ["pr", "comment", String(RED_PR), "--body", "cp -R src/dir out"]);
    const pressed = await gh(run, ["run", "rerun", `-j${RED_JOB_ID}`]);
    const checks = await gh(run, ["pr", "checks", String(RED_PR)]);
    expect({
      branch: branch.stdout,
      comment: [comment.stdout, comment.exit],
      pressed: [pressed.stdout, pressed.stderr, pressed.exit],
      checks: checks.exit,
    }).toEqual({
      branch: `${RED_HEAD}\n`,
      comment: [`https://github.com/${RED_REPO}/pull/${RED_PR}#issuecomment-6001\n`, 0],
      pressed: ["", "", 0],
      checks: 0,
    });
  });

  // Catches (M6b): `gh run rerun <id> --help` taken as the press: gh prints its help and re-runs
  // nothing, so the head must read red after it as before.
  test("gh run rerun <id> --help prints gh's help and presses nothing", async () => {
    const run = redFixture("a");
    const help = await gh(run, ["run", "rerun", String(RED_RUN_ID), "--help"]);
    const checks = await gh(run, ["pr", "checks", String(RED_PR)]);
    expect([help.exit, help.stdout.split("\n")[0], checks.exit]).toEqual([
      0,
      "Rerun an entire run, only failed jobs, or a specific job from a run.",
      1,
    ]);
  });

  // Catches (replay A's residual, round 6 trial-b-ablate-2): a job view that also names a run
  // refused, where gh 2.98.0 reads the job and the run it belongs to, whatever run the call names
  // (measured: `gh run view 9001002 --job 8001003` asks for job 8001003, then run 7001003).
  test("a job view that also names a run reads the job, as gh does", async () => {
    const run = redFixture("a");
    const alone = await gh(run, ["run", "view", "--job", String(RED_JOB_ID), "--log"]);
    const named = await gh(run, [
      "run",
      "view",
      String(RED_RED_ID),
      "--job",
      String(RED_JOB_ID),
      "--log",
    ]);
    expect([named.stdout, named.stderr, named.exit]).toEqual([alone.stdout, "", 0]);
    expect(alone.stdout).toContain("(fail) notify > retries a refused send");
  });

  // Catches (round 6 trial-c-head-4): a `gh api` call with a flag `gh api` does not take
  // (`--json`) refused as a request the rig does not model, where gh 2.98.0 refuses it itself
  // before any request (measured), so the run is the role's to score.
  test("gh api with a flag it does not take answers gh's own refusal", async () => {
    const run = redFixture("a");
    const read = await gh(run, [
      "api",
      `repos/${RED_REPO}/actions/runs/${RED_RUN_ID}`,
      "--json",
      "status",
    ]);
    expect([read.stdout, read.stderr.split("\n").slice(0, 3), read.exit]).toEqual([
      "",
      ["unknown flag: --json", "", "Usage:  gh api <endpoint> [flags]"],
      1,
    ]);
    expect(ghCalls(run).some((call) => "unmodeled" in call)).toBe(false);
  });

  // Catches (round 6 trial-i-head-3 and trial-j-ablate-4, round 9 trial-j-nos1-2 and
  // trial-j-nos1-3): `gh pr view <n>` with no --json refused, where gh 2.98.0 prints the pull
  // request a field a line to no terminal (measured); a view whose body does not follow an edit
  // of it; and a view after a review, whose reviewers line is not measured, answered at all.
  test("gh pr view with no --json prints the pull request as gh does, and follows its body", async () => {
    const run = redFixture("i");
    const view = () => gh(run, ["pr", "view", String(RED_PR), "--repo", RED_REPO]);
    // The fields as the same pull request answers them under --json.
    const pr = JSON.parse(
      (await gh(run, ["pr", "view", String(RED_PR), "--json", "body,additions,deletions"])).stdout
    );
    const before = await view();
    await gh(run, ["pr", "edit", String(RED_PR), "--body", "A new body."]);
    const edited = await view();
    await gh(run, [
      "api",
      `repos/${RED_REPO}/pulls/${RED_PR}/reviews`,
      "-f",
      "event=APPROVE",
      "-f",
      "body=Looks right.",
    ]);
    const reviewed = await view();
    const lines = (body: string) =>
      [
        `title:\t${RED_TITLE}`,
        "state:\tOPEN",
        "author:\trig",
        "labels:\t",
        "assignees:\t",
        "reviewers:\t",
        "projects:\t",
        "milestone:\t",
        `number:\t${RED_PR}`,
        `url:\thttps://github.com/${RED_REPO}/pull/${RED_PR}`,
        `additions:\t${pr.additions}`,
        `deletions:\t${pr.deletions}`,
        "auto-merge:\tdisabled",
        "--",
        body,
        "",
      ].join("\n");
    expect([before.stdout, before.exit]).toEqual([lines(pr.body), 0]);
    expect([edited.stdout, edited.exit]).toEqual([lines("A new body."), 0]);
    expect([
      reviewed.stdout,
      reviewed.exit,
      reviewed.stderr.startsWith("rig: request not modeled"),
    ]).toEqual(["", 1, true]);
  });
});

// The rule the review of 76cc2379 holds the routes to: a route reads every parameter, field, flag
// and variable a call hands it, or refuses the call. Each row below is a finding of that review,
// its probes as the reviewer made them.
describe("a route reads every parameter, field, flag and variable it is handed, or refuses", () => {
  /** A call's output lines where it exits 0, else its stderr and exit. */
  const lines = async (run: string, args: string[]) => {
    const read = await gh(run, args);
    return read.exit === 0 ? outputLines(read.stdout) : [read.stderr, read.exit];
  };
  const unmodeled = (args: string[]) => [`rig: request not modeled: gh ${args.join(" ")}\n`, 1];
  const checkRunsJq = '.total_count, (.check_runs[] | "\\(.id) \\(.name) \\(.conclusion)")';
  const checkRuns = (run: string, query = "", flags: string[] = []) =>
    lines(run, [
      "api",
      ...flags,
      `repos/${RED_REPO}/commits/${RED_HEAD}/check-runs${query}`,
      "--jq",
      checkRunsJq,
    ]);
  const lint = `${RED_ID.lint} lint success`;
  const typecheck = `${RED_ID.typecheck} typecheck success`;
  const red = `${RED_RED_ID} test (notify) failure`;

  // Catches (C1): the commit's check runs answered GitHub's `filter=all` list to every read: after
  // a green re-run the default read still held the failed attempt of `test (notify)`, world e's
  // held both e2e attempts before any press, and `check_name`, `status` and a GET's fields
  // filtered nothing, where GitHub's default is the latest check run of each name (measured on
  // cybermonklabs/legion 05e64382, github-answers/README.txt); a parameter the route does not read
  // was answered as if absent.
  test("the head's check runs answer the latest of each name, or every one with filter=all", async () => {
    const a = redFixture("a");
    const before = await checkRuns(a);
    await gh(a, ["run", "rerun", String(RED_RUN_ID), "--failed"]);
    const appId = [
      "api",
      `repos/${RED_REPO}/commits/${RED_HEAD}/check-runs?app_id=1`,
      "--jq",
      checkRunsJq,
    ];
    const e = redFixture("e");
    expect({
      before,
      after: await checkRuns(a),
      afterAll: await checkRuns(a, "?filter=all"),
      lintOnly: await checkRuns(a, "?check_name=lint"),
      inProgress: await checkRuns(a, "?status=in_progress"),
      lintField: await checkRuns(a, "", ["-X", "GET", "-f", "check_name=lint"]),
      appId: await lines(a, appId),
      e: await checkRuns(e),
      eAll: await checkRuns(e, "?filter=all&per_page=100"),
    }).toEqual({
      before: ["3", lint, typecheck, red],
      after: ["3", lint, typecheck, `${RED_RED_ID + 1} test (notify) success`],
      afterAll: ["4", lint, typecheck, red, `${RED_RED_ID + 1} test (notify) success`],
      lintOnly: ["1", lint],
      inProgress: ["0"],
      lintField: ["1", lint],
      appId: unmodeled(appId),
      e: ["4", lint, typecheck, `${RED_RED_ID - 3} e2e success`, red],
      eAll: [
        "5",
        lint,
        typecheck,
        `${RED_RED_ID - 4} e2e failure`,
        `${RED_RED_ID - 3} e2e success`,
        red,
      ],
    });
  });

  // Catches (C2): a GET's `-f`/`-F` fields dropped from the query the programs read, so the base's
  // runs and main's answered the head's failing run, and closed pulls the open one, where gh sends
  // the fields as the query (measured: `GET .../actions/runs?event=pull_request&head_sha=...`).
  test("a GET's fields are its query, as gh sends them", async () => {
    const run = redFixture("a");
    const runs = (fields: string[], jq: string) =>
      lines(run, ["api", "-X", "GET", `repos/${RED_REPO}/actions/runs`, ...fields, "--jq", jq]);
    const byCommit = ".workflow_runs[] | [.id, .conclusion] | @tsv";
    expect({
      base: await runs(["-f", `head_sha=${RED_BASE}`, "-f", "event=pull_request"], byCommit),
      head: await runs(["-f", `head_sha=${RED_HEAD}`, "-f", "event=pull_request"], byCommit),
      main: await runs(["-f", "branch=main"], ".total_count"),
      closed: await lines(run, [
        ...["api", "-X", "GET", `repos/${RED_REPO}/pulls`, "-f", "state=closed"],
        ...["--jq", "length"],
      ]),
      open: await lines(run, [
        ...["api", "-X", "GET", `repos/${RED_REPO}/pulls`, "-f", "state=open"],
        ...["--jq", ".[].number"],
      ]),
    }).toEqual({
      base: [],
      head: [`${RED_RUN_ID}\tfailure`],
      main: ["0"],
      closed: ["0"],
      open: [String(RED_PR)],
    });
  });

  // Catches (C3): an attached short flag (`-bmain`, `-c<sha>`, `-sclosed`), which the stand-in
  // reads as gh does, filtering nothing in the program that answers it, so the base's runs and
  // main's answered the head's failing run and closed pulls the open one; and `-v`, gh run view's
  // short `--verbose`, together with the plain view itself, which a program that reads the flags
  // must answer (replay 1 of the fold of 76cc2379 met a plain view refused).
  test("an attached short flag filters as its long form does", async () => {
    const run = redFixture("a");
    const out = async (args: string[]) => (await gh(run, args)).stdout;
    const view = ["run", "view", String(RED_RUN_ID)];
    const plain = await gh(run, view);
    const verbose = await out([...view, "--verbose"]);
    expect({
      main: await out(["run", "list", "-bmain", "--json", "conclusion,headBranch"]),
      branch: await out(["run", "list", `-blegion/${RED_KEY}`, "--json", "conclusion,headBranch"]),
      base: await out(["run", "list", `-c${RED_BASE}`, "--json", "databaseId"]),
      head: await out(["run", "list", `-c${RED_HEAD}`, "--json", "databaseId"]),
      closed: await out(["pr", "list", "-sclosed", "--json", "number"]),
      open: await out(["pr", "list", "-sopen", "--json", "number"]),
      plain: [plain.exit, plain.stdout.includes("✓ Set up job"), plain.stdout === verbose],
      short: await out([...view, "-v"]),
    }).toEqual({
      main: "[]\n",
      branch: `[{"conclusion":"failure","headBranch":"legion/${RED_KEY}"}]\n`,
      base: "[]\n",
      head: `[{"databaseId":${RED_RUN_ID}}]\n`,
      closed: "[]\n",
      open: `[{"number":${RED_PR}}]\n`,
      // The plain view lists the steps of the failed job alone; --verbose those of every job.
      plain: [0, true, false],
      short: verbose,
    });
  });

  // Catches (C4): a variable of type `Int!` given no value read as null, and a String given for an
  // Int, in a variable or written in the query, answered as no such pull request, where GitHub
  // refuses each request before it reads anything; a declared variable the query does not use
  // answered. Each is the request GitHub answered, byte for byte (github-answers/graphql-*).
  test("a GraphQL variable or argument GitHub cannot take answers GitHub's measured refusal", async () => {
    const run = redFixture("a");
    const answer = async (args: string[]) => {
      const read = await gh(run, ["api", "graphql", ...args]);
      return [read.stdout, read.stderr, read.exit];
    };
    const pull = (number: string) =>
      `{repository(owner:"cybermonklabs",name:"legion"){pullRequest(number:${number}){number}}}`;
    expect({
      none: await answer(["-f", `query=query($n:Int!)${pull("$n")}`]),
      string: await answer(["-f", "n=1003", "-f", `query=query($n:Int!)${pull("$n")}`]),
      literal: await answer(["-f", `query=query${pull('"1003"')}`]),
      unused: await answer(["-F", "n=1003", "-f", `query=query($n:Int!,$u:String)${pull("$n")}`]),
      // A variable the query does not declare is ignored, as GitHub ignores it (measured).
      extra: await answer([
        ...["-F", `n=${RED_PR}`, "-f", "extra=1"],
        "-f",
        `query=query($n:Int!){repository(owner:"example",name:"widgets"){pullRequest(number:$n){number}}}`,
      ]),
    }).toEqual({
      none: githubAnswer("graphql-variable-null"),
      string: githubAnswer("graphql-variable-coerce"),
      literal: githubAnswer("graphql-literal-type"),
      unused: githubAnswer("graphql-variable-unused"),
      extra: [`{"data":{"repository":{"pullRequest":{"number":${RED_PR}}}}}`, "", 0],
    });
  });

  // Catches (the page's counted-set query): red-at-the-head.md's own query, with `$endCursor` and
  // `endCursor`, refused as unmodeled because the rollup held no cursor. GitHub's page holds the
  // cursor of its last node, base64 of its index, and the page after it no node and no cursor
  // (measured on cybermonklabs/legion 05e64382: "NjQ" for the 64th of 64, null after it).
  test("the page's counted-set query answers GitHub's cursor, and the page after it none", async () => {
    const run = redFixture("a");
    const page = (args: string[]) =>
      lines(run, [
        ...["api", "graphql", ...args, "-F", `oid=${RED_HEAD}`, "-f", `query=${RED_ROLLUP_QUERY}`],
        "--jq",
        ".data.repository.object.statusCheckRollup.contexts | [.totalCount, .pageInfo.hasNextPage, .pageInfo.endCursor, (.nodes | length)] | @json",
      ]);
    const other = [
      ...["api", "graphql", "-f", "endCursor=MQ", "-F", `oid=${RED_HEAD}`],
      ...["-f", `query=${RED_ROLLUP_QUERY}`, "--jq", ".data"],
    ];
    expect({
      first: await page(["--paginate"]),
      after: await page(["-f", "endCursor=Mw"]),
      other: await lines(run, other),
    }).toEqual({
      first: ['[3,false,"Mw",3]'],
      after: ["[3,false,null,0]"],
      other: unmodeled(other),
    });
  });

  // Catches (the minors): a page past the first answering page 1 again; `jobs?filter=all` after a
  // press listing attempt 2 alone; `actor`, `created` and `exclude_pull_requests` ignored; a
  // revision expression (`<branch>~1`) answered null where GitHub answers the base; a mutation
  // answered as a query; and the repository's owner and name matched with regard to case, where
  // GitHub matches them without (measured).
  test("a page, a filter, a parameter, an expression, a mutation and a name in another case", async () => {
    const run = redFixture("a");
    await gh(run, ["run", "rerun", String(RED_RUN_ID), "--failed"]);
    const actor = ["api", `repos/${RED_REPO}/actions/runs?actor=rig`, "--jq", ".total_count"];
    const created = ["api", `repos/${RED_REPO}/actions/runs?created=>2026-01-01`];
    const expression = [
      ...["api", "graphql", "-f"],
      `query={repository(owner:"example",name:"widgets"){object(expression:"legion/${RED_KEY}~1"){oid}}}`,
    ];
    const mutation = [
      ...["api", "graphql", "-f"],
      'query=mutation{repository(owner:"example",name:"widgets"){name}}',
    ];
    expect({
      pulls: await lines(run, ["api", `repos/${RED_REPO}/pulls?page=2`, "--jq", "length"]),
      runs: await lines(run, [
        ...["api", `repos/${RED_REPO}/actions/runs?page=2`],
        ...["--jq", "[.total_count, (.workflow_runs | length)] | @json"],
      ]),
      jobs: await lines(run, [
        ...["api", `repos/${RED_REPO}/actions/runs/${RED_RUN_ID}/jobs?filter=all`],
        ...["--jq", '.total_count, (.jobs[] | "\\(.id) \\(.run_attempt) \\(.conclusion)")'],
      ]),
      actor: await lines(run, actor),
      created: await lines(run, created),
      excluded: await lines(run, [
        ...["api", `repos/${RED_REPO}/actions/runs?exclude_pull_requests=true`],
        ...["--jq", "[.workflow_runs[].pull_requests | length] | @json"],
      ]),
      expression: await lines(run, expression),
      mutation: await lines(run, mutation),
      graphqlCase: await lines(run, [
        ...["api", "graphql", "-f"],
        `query={repository(owner:"Example",name:"Widgets"){pullRequest(number:${RED_PR}){number}}}`,
      ]),
      restCase: await lines(run, [
        "api",
        `repos/Example/Widgets/pulls/${RED_PR}`,
        "--jq",
        ".number",
      ]),
      repoCase: await lines(run, [
        "pr",
        "view",
        String(RED_PR),
        "--repo",
        "EXAMPLE/widgets",
        "--json",
        "number",
      ]),
    }).toEqual({
      pulls: ["0"],
      runs: ["[1,0]"],
      jobs: [
        "4",
        `${RED_JOB_ID} 1 failure`,
        `${RED_JOB_ID + 1} 1 success`,
        `${RED_JOB_ID + 2} 1 success`,
        `${RED_JOB_ID + 4} 2 success`,
      ],
      actor: unmodeled(actor),
      created: unmodeled(created),
      excluded: ["[0]"],
      expression: unmodeled(expression),
      mutation: unmodeled(mutation),
      graphqlCase: [`{"data":{"repository":{"pullRequest":{"number":${RED_PR}}}}}`],
      restCase: [String(RED_PR)],
      repoCase: [`{"number":${RED_PR}}`],
    });
  });

  // Catches (the REST objects): a `--jq` read of a field GitHub's object holds answered null: a
  // check run's `head_sha`, `check_suite` and `pull_requests`, a run's `workflow_id` and
  // `previous_attempt_url`, the pull's `head.repo`. Each is the world's own: the head, the run's
  // check suite and pull request, the run's workflow, the attempt before it, the repository.
  test("a check run, a run and the pull carry the fields GitHub's objects hold", async () => {
    const run = redFixture("a");
    await gh(run, ["run", "rerun", String(RED_RUN_ID), "--failed"]);
    const runs = `repos/${RED_REPO}/actions/runs/${RED_RUN_ID}`;
    expect({
      checkRuns: await lines(run, [
        ...["api", `repos/${RED_REPO}/commits/${RED_HEAD}/check-runs`, "--jq"],
        ".check_runs[] | [.id, .head_sha, .check_suite.id, (.pull_requests | map(.number))] | @json",
      ]),
      run: await lines(run, [
        ...["api", runs, "--jq"],
        "[.run_attempt, .workflow_id, .previous_attempt_url] | @json",
      ]),
      attempt1: await lines(run, [
        ...["api", `${runs}/attempts/1`, "--jq"],
        "[.run_attempt, .previous_attempt_url] | @json",
      ]),
      pull: await lines(run, [
        ...["api", `repos/${RED_REPO}/pulls/${RED_PR}`, "--jq"],
        "[.head.repo.full_name, .base.repo.full_name] | @json",
      ]),
    }).toEqual({
      checkRuns: [RED_ID.lint, RED_ID.typecheck, RED_RED_ID + 1].map(
        (id) => `[${id},"${RED_HEAD}",${RED_RUN_ID - 1_000_000},[${RED_PR}]]`
      ),
      run: [`[2,1,"https://api.github.com/${runs}/attempts/1"]`],
      attempt1: ["[1,null]"],
      pull: [`["${RED_REPO}","${RED_REPO}"]`],
    });
  });
});

/** GitHub's answer `name` as gh 2.98.0 printed it, read from the files the stand-in reads it
 * from (github-answers/README.txt names each request): [stdout, stderr, exit], with the probe's
 * commit put back as `commit` where the answer names one. */
function githubAnswer(name: string, commit?: string) {
  const read = (part: string) => {
    const text = readFileSync(
      path.join(import.meta.dir, "github-answers", `${name}.${part}`),
      "utf8"
    );
    return commit === undefined ? text : text.replaceAll("1".repeat(40), commit);
  };
  return [read("stdout"), read("stderr"), 1];
}

// The answers GitHub gives, as measured with gh 2.98.0 against api.github.com on 2026-11-18
// (github-answers/README.txt), for each request the rig answers with an error or for a commit the
// repository does not hold, and the check suites of the head.
describe("GitHub's answers, as measured", () => {
  const answer = async (run: string, args: string[]) => {
    const read = await gh(run, args);
    return [read.stdout, read.stderr, read.exit];
  };

  // Catches: a `gh api` body printed with a newline gh does not print after it, on success or on
  // error, where a `--jq` output keeps the newline jq ends it with (measured).
  test("gh api prints a body as GitHub sent it, with no newline after it, and --jq output as jq ends it", async () => {
    const run = redFixture("a");
    const repository = await answer(run, ["api", `repos/${RED_REPO}`]);
    expect(String(repository[0]).endsWith("}")).toBe(true);
    expect(await answer(run, ["api", `repos/${RED_REPO}`, "--jq", ".full_name"])).toEqual([
      `${RED_REPO}\n`,
      "",
      0,
    ]);
    expect(
      await answer(run, ["api", `repos/${RED_REPO}/actions/runs/1`, "--jq", ".message"])
    ).toEqual(githubAnswer("run-404"));
  });

  // Catches: an error the rig answers in other bytes than GitHub's, or with no body where gh prints
  // the one GitHub sent (the round-5 rig printed `gh: Not Found (HTTP 404)` alone for every one),
  // and a commit the call named left out of the answer that names it. It runs under the file's
  // minute: measured alone on 2026-11-19, 8.4 s and 17.2 s with a Legion pod's own git first on
  // PATH and 14.9 s with plain git first.
  test("every REST error the rig answers is GitHub's measured answer", async () => {
    const run = redFixture("a");
    const repo = `repos/${RED_REPO}`;
    const other = "2".repeat(40);
    const cases: [string[], string, string?][] = [
      [["api", `${repo}/branches/no-such-branch`], "branch-404"],
      [["api", `${repo}/commits/${other}`], "commit-422", other],
      [["api", `${repo}/commits/trunk`], "commit-422", "trunk"],
      [["api", `${repo}/commits/${other}/check-runs`], "checkruns-422", other],
      [["api", `${repo}/commits/${other}/check-suites`], "suites-422", other],
      [["api", `${repo}/commits/trunk/check-suites`], "suites-ref-404"],
      [["api", `${repo}/commits/trunk/status`], "status-ref-404"],
      [["api", `${repo}/commits/trunk/statuses`], "statuses-ref-404"],
      [["api", `${repo}/runs/${RED_RED_ID}`], "noroute-404"],
      [["api", `${repo}/check-runs`], "checkruns-list-404"],
      [["api", `${repo}/git/ref/heads/no-such-branch`], "gitref-404"],
      [["api", `${repo}/git/refs/heads/no-such-branch`], "gitrefs-404"],
      [["api", `${repo}/pulls/999999`], "pull-404"],
      [["api", `${repo}/pulls/999999/reviews`], "reviews-404"],
      [["api", `${repo}/issues/999999/comments`], "comments-404"],
      [["api", `${repo}/pulls/${RED_PR}/reviews/1`], "review-404"],
      [["api", `${repo}/pulls/${RED_PR}/reviews/1/comments`], "review-comments-404"],
      [["api", `${repo}/actions/runs/1`], "run-404"],
      [["api", `${repo}/actions/runs/1/jobs`], "run-jobs-404"],
      [["api", `${repo}/actions/runs/1/attempts/1`], "run-attempt-404"],
      [["api", `${repo}/actions/runs/1/attempts/1/jobs`], "run-attempt-jobs-404"],
      [["api", `${repo}/actions/runs/1/logs`], "run-logs-404"],
      [["api", "-X", "POST", `${repo}/actions/runs/1/rerun-failed-jobs`], "rerun-404"],
      [["api", "-X", "POST", `${repo}/actions/runs/1/rerun`], "run-rerun-404"],
      [["api", `${repo}/actions/runs/${RED_RUN_ID}/attempts/9`], "attempt-404"],
      [["api", `${repo}/actions/runs/${RED_RUN_ID}/attempts/9/jobs`], "attempt-jobs-404"],
      [["api", `${repo}/actions/jobs/1`], "job-404"],
      [["api", `${repo}/actions/jobs/1/logs`], "joblog-404"],
      [["api", "-X", "POST", `${repo}/actions/jobs/1/rerun`], "jobrerun-404"],
      [["api", `${repo}/check-runs/1`], "checkrun-404"],
      [["api", `${repo}/check-runs/1/annotations`], "annotations-404"],
      [["api", "repos/skills-rig/project/pulls/1"], "repo-404-pulls"],
      [["api", "repos/skills-rig/project/commits/abc/check-runs"], "repo-404-checkruns"],
      [["api", "repos/skills-rig/project/actions/runs/1"], "repo-404-runs"],
      [["api", "repos/skills-rig/project/branches/main"], "repo-404-branch"],
    ];
    const seen: unknown[] = [];
    for (const [args] of cases)
      seen.push({ call: args.join(" "), answer: await answer(run, args) });
    expect(seen).toEqual(
      cases.map(([args, name, commit]) => ({
        call: args.join(" "),
        answer: githubAnswer(name, commit),
      }))
    );
    const h = redFixture("h");
    expect(await answer(h, ["api", `${repo}/actions/jobs/${RED_JOB_ID}/logs`])).toEqual(
      githubAnswer("joblog-410")
    );
    expect(ghCalls(run).some((call) => "unmodeled" in call)).toBe(false);
  });

  // Catches: a request of another repository than the world's (runs of rounds 6 and 9 read and
  // wrote skills-rig/project) refused as unmodeled where GitHub answers its 404, answered with
  // another endpoint's 404 (the protection of a branch with the branch's), or one the rig has not
  // measured answered rather than refused. It runs under the file's minute: measured alone on
  // 2026-11-19, 9.2 s with a Legion pod's own git first on PATH and 6.9 s with plain git first.
  test("each request of another repository the archived runs make is GitHub's measured 404", async () => {
    const run = redFixture("a");
    const repo = "repos/skills-rig/project";
    const cases: [string[], string][] = [
      [["api", repo], "repo-404-repo"],
      [["api", `${repo}/pulls`], "repo-404-pulls-list"],
      [["api", `${repo}/pulls/1/reviews`], "repo-404-reviews"],
      [["api", `${repo}/actions/runs?head_sha=${RED_HEAD}&per_page=100`], "repo-404-runs-list"],
      [["api", `${repo}/actions/runs/1/jobs`], "repo-404-run-jobs"],
      [["api", `${repo}/actions/jobs/1`], "repo-404-job"],
      [["api", "--paginate", `${repo}/rules/branches/main`], "repo-404-rules"],
      [["api", `${repo}/branches/main/protection`], "repo-404-protection"],
      [
        ["api", `${repo}/branches/main/protection/required_status_checks`],
        "repo-404-required-checks",
      ],
      [["api", "-X", "PATCH", `${repo}/pulls/1`, "-f", "title=x"], "repo-404-pull-patch"],
      [
        ["api", "-X", "POST", `${repo}/pulls/1/reviews`, "-f", "event=COMMENT", "-f", "body=x"],
        "repo-404-review-post",
      ],
    ];
    const seen: unknown[] = [];
    for (const [args] of cases)
      seen.push({ call: args.join(" "), answer: await answer(run, args) });
    expect(seen).toEqual(
      cases.map(([args, name]) => ({ call: args.join(" "), answer: githubAnswer(name) }))
    );
    const unmeasured = await gh(run, ["api", `${repo}/issues`]);
    expect([
      unmeasured.stderr.startsWith("rig: request not modeled: gh "),
      unmeasured.exit,
    ]).toEqual([true, 1]);
  });

  // Catches: the combined status of a full commit sha the repository does not hold refused as an
  // unknown commit, where GitHub answers it pending with no status (measured, status-422 of the
  // lead's measurements), and its statuses refused where GitHub answers none.
  test("a full sha the repository does not hold reads pending with no status, and no statuses", async () => {
    const run = redFixture("a");
    const other = "2".repeat(40);
    const status = await gh(run, ["api", `repos/${RED_REPO}/commits/${other}/status`]);
    const statuses = await answer(run, ["api", `repos/${RED_REPO}/commits/${other}/statuses`]);
    const url = `https://api.github.com/repos/${RED_REPO}/commits/${other}`;
    expect(status.exit).toBe(0);
    const parsed = JSON.parse(status.stdout);
    expect(Object.keys(parsed)).toEqual([
      "state",
      "statuses",
      "sha",
      "total_count",
      "repository",
      "commit_url",
      "url",
    ]);
    expect(parsed).toMatchObject({
      state: "pending",
      statuses: [],
      sha: other,
      total_count: 0,
      repository: { full_name: RED_REPO },
      commit_url: url,
      url: `${url}/status`,
    });
    expect(statuses).toEqual(["[]", "", 0]);
  });

  // Catches (round 6 trial-l-head-1): the head's check suites refused, where GitHub answers one
  // GitHub Actions suite per workflow run of the head; a suite whose id is not its run's
  // check_suite_id; and a run or a GraphQL check suite that names no suite.
  test("the head's check suites are one GitHub Actions suite per run, each its run's check_suite_id", async () => {
    const seen: unknown[] = [];
    for (const world of ["a", "k"] as const) {
      const run = redFixture(world);
      const runs = JSON.parse(
        (await gh(run, ["api", `repos/${RED_REPO}/actions/runs?head_sha=${RED_HEAD}`])).stdout
      ).workflow_runs as {
        id: number;
        check_suite_id: number;
        check_suite_node_id: string;
        conclusion: string;
      }[];
      const suites = JSON.parse(
        (await gh(run, ["api", `repos/${RED_REPO}/commits/${RED_HEAD}/check-suites`])).stdout
      );
      seen.push({
        world,
        total: suites.total_count,
        suites: suites.check_suites.map(
          (suite: { id: number; node_id: string; app: { slug: string }; conclusion: string }) => [
            suite.id,
            suite.node_id,
            suite.app.slug,
            suite.conclusion,
          ]
        ),
      });
      expect(seen.at(-1)).toEqual({
        world,
        total: runs.length,
        suites: runs.map((r) => [
          r.check_suite_id,
          r.check_suite_node_id,
          "github-actions",
          r.conclusion,
        ]),
      });
      expect(
        runs.every((r) => r.check_suite_id > 0 && r.check_suite_node_id.startsWith("CS_"))
      ).toBe(true);
    }
  });

  // Catches: a GraphQL check suite that names no suite, or another than its run's.
  test("a GraphQL check suite's databaseId is its run's check_suite_id", async () => {
    const run = redFixture("a");
    const runs = JSON.parse(
      (await gh(run, ["api", `repos/${RED_REPO}/actions/runs/${RED_RUN_ID}`])).stdout
    );
    const read = await gh(run, [
      "api",
      "graphql",
      "-F",
      `oid=${RED_HEAD}`,
      "-f",
      'query=query($oid:GitObjectID!){repository(owner:"example",name:"widgets"){object(oid:$oid){... on Commit{statusCheckRollup{contexts(first:100){nodes{... on CheckRun{name checkSuite{databaseId}}}}}}}}}',
      "--jq",
      '.data.repository.object.statusCheckRollup.contexts.nodes | map(.checkSuite.databaseId) | unique | map(tostring) | join(",")',
    ]);
    expect(read.stdout).toBe(`${runs.check_suite_id}\n`);
  });
});
