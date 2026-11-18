import { beforeAll, describe, expect, setDefaultTimeout, test } from "bun:test";
import {
  appendFileSync,
  cpSync,
  existsSync,
  mkdirSync,
  readFileSync,
  realpathSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
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
 * decides nothing here. */
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
    const served = Bun.spawnSync([path.join(dir, "model-key", "anthropic-key")]);
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

// The key-file route's key command reads the operator's key for as long as it exists
// (install-key-file-model-route.sh: the caller removes --key-dir on every exit that keeps no
// profile). A setup that stops after the route is installed leaves no profile to use it.
describe("a setup that fails after install_model_route", () => {
  /** A checkout whose plugin installer and home maker do nothing, with the real key-file route. */
  function checkout(): string {
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
        "cmd_profile",
      ].map(fn),
      "checkout_commit() { printf 'fixture parents=base\\n'; }",
      "install_superpowers() { fail 'the superpowers probe failed'; }",
      'work=$1 maps=$1/maps; cmd_profile probe "$2"',
    ].join("\n");
    const failed = bash(script, [work, checkout()], {
      ...routeEnv(),
      SKILL_SCENARIOS_SUPERPOWERS: "set",
    });
    expect(failed.code).toBe(1);
    expect(failed.stderr).toContain("the superpowers probe failed");
    // The profile's directory has a random name, and the map's link names the label.
    const P = realpathSync(path.join(work, "maps/profiles/probe"));
    expect(path.relative(realpathSync(work), P)).toMatch(/^profiles\/[0-9a-f]{16}$/);
    expect(existsSync(path.join(P, "home/.omp/profiles/skills/agent/models.yml"))).toBe(true);
    expect(existsSync(path.join(P, "model-key"))).toBe(false);
    expect(existsSync(path.join(P, "ready"))).toBe(false);

    // The same setup that succeeds keeps its key command: a ready profile uses it.
    const kept = freshDir("route-work");
    const ok = bash(
      script.replace("fail 'the superpowers probe failed'", ":"),
      [kept, checkout()],
      {
        ...routeEnv(),
        SKILL_SCENARIOS_SUPERPOWERS: "set",
      }
    );
    expect({ code: ok.code, stderr: ok.stderr }).toEqual({ code: 0, stderr: "" });
    const keptProfile = realpathSync(path.join(kept, "maps/profiles/probe"));
    expect(existsSync(path.join(keptProfile, "model-key/anthropic-key"))).toBe(true);
    expect(existsSync(path.join(keptProfile, "ready"))).toBe(true);
  });

  // Catches: a judge-profile build that fails at its second profile and keeps the first one's key
  // command.
  test("judge_profiles removes both profiles' model-key and marks nothing ready", () => {
    const work = freshDir("judge-route-work");
    const co = checkout();
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
// scenario and label in the map, so a later batch never replaces an earlier run (cmd_run replaces a
// run of its name).
describe("next_run", () => {
  const next = (work: string, scenario: string, label: string) =>
    bash(`${fn("next_run")}\nwork=$1 maps=$1/maps\nnext_run "$2" "$3"`, [work, scenario, label]);

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
      mkdirSync(path.join(work, "maps", "runs", run), { recursive: true });
    writeFileSync(path.join(work, "maps", "runs", "spec-writing-s1-99"), "");
    expect(next(work, "spec-writing", "s1")).toEqual({ code: 0, stdout: "11\n", stderr: "" });
  });

  // Catches: a first batch that fails because $work/maps/runs does not exist yet.
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
  printf 'launch%s | %s\\n' "\${*:+ $*}" "$(<"$R/prompt.txt")" >>"$R/calls"
  [ -n "\${SECOND:-}" ] && [ $# -gt 0 ] && : >"$R/sessions/fresh.jsonl"
  printf x >>"$R/launches"
  status=0
  [ "$(wc -c <"$R/launches")" != "\${FAIL_AT:-0}" ] || status=1
  printf 'output of turn %s%s\\nexit=%s\\n' "$(wc -c <"$R/launches")" "\${*:+ $*}" "$status" >"$R/out.txt"
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
      "launch | Design it.",
      "seed state <R>/fixture.json 1 <R>",
      "judge answer <R> 1",
      "seed answer <R>/fixture.json <R>/answers-1.jsonl",
      `launch --continue | ${answered}`,
      "seed state <R>/fixture.json 2 <R>",
      "seed approve <R>/fixture.json",
      `launch --continue | ${approved}`,
      "seed state <R>/fixture.json 3 <R>",
    ]);
    expect(read("turns.txt")).toBe("1 first exit=0\n2 answered exit=0\n3 approved exit=0\n");
    expect(read("answers.jsonl")).toBe(read("answers-1.jsonl"));
    expect(read("out-2.txt")).toBe("output of turn 2 --continue\nexit=0\n");
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
    expect(calls).toEqual(["launch | Design it.", "seed state <R>/fixture.json 1 <R>"]);
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
        `${fn("fail")}\n${fn("pinned_omp")}\n${fn("install_superpowers")}\nsuperpowers_skills=(writing-plans brainstorming)\ninstall_superpowers "$1" skills-s1 omp@pin`,
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
    const env = readFileSync(path.join(bin, "env.txt"), "utf8");
    expect(env).toContain("ENVOY_URL=http://127.0.0.1:1\n");
    expect(env).toContain("OMP_PROFILE=skills-s1\n");
    expect(env).toContain(`HOME=${label}/home\n`);
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
  env | grep -q LEGION_E2E && echo "env LEGION_E2E set"
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

// launch with a stand-in tmux that records its session's name and starts the pane's command a
// moment later, as a real tmux server does, a stand-in systemd-run that records the scope it was
// asked for and runs the command after `--`, a stand-in bwrap that records its arguments and runs
// the command after `--`, and a stand-in mise that prints the omp command line it was given. The
// run's directory and the label's profile sit in a work directory, as rig.sh lays them out.
describe("launch", () => {
  function launchRun(args: string, env: Record<string, string> = {}) {
    const work = freshDir("launch-work");
    const run = path.join(work, "runs", "0123456789abcdef");
    const profile = path.join(work, "profiles", "fedcba9876543210");
    const bin = freshDir("launch-bin");
    for (const dir of ["cwd", "sessions", "tmp"])
      mkdirSync(path.join(run, dir), { recursive: true });
    for (const dir of ["pi-envoy", "pi-legion"])
      mkdirSync(path.join(profile, dir), { recursive: true });
    // The label's checkout, which every agent's view hides, and the map's link to its profile.
    const checkout = freshDir("launch-checkout");
    writeFileSync(path.join(profile, "checkout"), `${checkout}\n`);
    mkdirSync(path.join(work, "services"));
    mkdirSync(path.join(work, "maps", "profiles"), { recursive: true });
    symlinkSync(
      "../../profiles/fedcba9876543210",
      path.join(work, "maps", "profiles", "ablate-head")
    );
    writeFileSync(path.join(run, "prompt.txt"), "The person answered your asks on LWEVAL-2.\n");
    writeFileSync(path.join(run, "pane.env"), `PATH=${bin}:${process.env.PATH ?? ""}\n`);
    writeFileSync(path.join(run, "out.txt"), "the first turn\nexit=0\n");
    mkdirSync(path.join(bin, "pin", "bin"), { recursive: true });
    writeFileSync(path.join(bin, "pin", "bin", "omp"), "#!/bin/sh\n", { mode: 0o755 });
    writeFileSync(
      path.join(bin, "mise"),
      `#!/bin/sh\nif [ "$1" = where ]; then echo "${bin}/pin"; exit 0; fi\nshift 3\necho "$@"\n`,
      { mode: 0o755 }
    );
    writeFileSync(
      path.join(bin, "systemd-run"),
      `#!/bin/bash\nscope=()\nwhile [ "$1" != -- ]; do scope+=("$1"); shift; done\nshift\necho "\${scope[*]}" >"${run}/scope"\nexec "$@"\n`,
      { mode: 0o755 }
    );
    writeFileSync(
      path.join(bin, "bwrap"),
      `#!/bin/bash\n: >"${run}/view"\nif [ "$1" = --args ]; then tr '\\0' '\\n' <&"$2" >>"${run}/view"; shift 2; fi\nwhile [ "$1" != -- ]; do printf '%s\\n' "$1" >>"${run}/view"; shift; done\nshift\nexec "$@"\n`,
      { mode: 0o755 }
    );
    const standIns = `
tmux() { case $1 in new-session) printf '%s\\n' "$4" >"$R/session"; (command sleep 0.3 && bash -c "\${@: -1}") & ;; esac; }
sleep() { command sleep 0.05; }`;
    const result = bash(
      `${fn("fail")}\n${fn("pinned_omp")}\n${fn("launch")}\n${standIns}\nR=$1 P=$2 work=$3 maps=$3/maps tag=t pin=omp@pin\nlaunch${args}\ncp "$R/out.txt" "$R/seen"`,
      [run, profile, work],
      { PATH: `${bin}:${childEnv.PATH}`, ...env }
    );
    const read = (name: string) => readFileSync(path.join(run, name), "utf8");
    return { ...result, run, bin, read, work, profile, checkout };
  }

  // Catches: a later turn that reads the previous turn's `exit=` line as its own and returns at
  // once, and extra omp arguments placed anywhere but after the prompt.
  test("a later turn passes its arguments to omp and waits for its own exit line", () => {
    const { code, read, bin, run } = launchRun(" --continue");
    expect(code).toBe(0);
    expect(read("seen")).toBe(
      `${bin}/pin/bin/omp -p The person answered your asks on LWEVAL-2. --continue --session-dir ${run}/sessions\nexit=0\n`
    );
  });

  // Catches: an agent that runs in the caller's own scope with no memory cap. On 2026-11-18 one
  // reviewer's shell held an endless command substitution (an unquoted here-document whose
  // backticks ran `yes`) and its omp reached 79 GiB in the session's scope before the kernel
  // killed it. In a scope of its own, capped with no swap, such an agent is killed alone.
  test("the agent runs in a user scope of its own, capped at 8G with no swap, or at SKILL_SCENARIOS_MEMORY_MAX", () => {
    const capped = launchRun("");
    expect(capped.code).toBe(0);
    expect(capped.read("scope")).toBe(
      "--user --scope --quiet -p MemoryMax=8G -p MemorySwapMax=0\n"
    );
    const named = launchRun("", { SKILL_SCENARIOS_MEMORY_MAX: "2G" });
    expect(named.read("scope")).toBe("--user --scope --quiet -p MemoryMax=2G -p MemorySwapMax=0\n");
  });

  // Catches: an agent that can learn its label or case from what it sees: the map (<work>/maps),
  // another run's directory, the profile's records (its checkout's path names the label), the
  // batch's command line in the process table, another run's scratch files in the machine's /tmp,
  // or another label's texts in its checkout; and a tmux session named after the run.
  test("the agent runs under bwrap, in a PID namespace of its own, with <work> empty but for its run, its label's plugins and the services, its own /tmp, and every label's checkout empty", () => {
    const { code, read, run, work, profile, checkout } = launchRun("");
    expect(code).toBe(0);
    expect(read("view").split("\n").filter(Boolean)).toEqual([
      ...["--dev-bind", "/", "/", "--tmpfs", work, "--bind", run, run],
      ...["--bind", `${run}/tmp`, "/tmp", "--tmpfs", "/var/tmp", "--tmpfs", "/dev/shm"],
      ...["--tmpfs", checkout],
      ...["--ro-bind", `${profile}/pi-envoy`, `${profile}/pi-envoy`],
      ...["--ro-bind", `${profile}/pi-legion`, `${profile}/pi-legion`],
      ...["--ro-bind", `${work}/services`, `${work}/services`],
      ...["--unshare-pid", "--proc", "/proc", "--die-with-parent", "--chdir", `${run}/cwd`],
    ]);
    expect(read("session")).toBe("t-0123456789abcdef\n");
    // The view, which names every label's checkout, is in no file of the run and on no command line
    // the agent's /proc shows: bwrap reads it from a file under <work>, which its view hides.
    expect(read("launch.sh")).not.toContain(checkout);
    expect(read("launch.sh")).toContain(" --args 3 -- ");
  });
});

// The heads scenarios wait for the run's Dispatch to know #702's head before any agent starts:
// fixture_heads with a stand-in for the label checkout's `dispatch` command (its
// packages/envoy-client/bin/dispatch.ts), which records each call and prints the line of #702 from
// the call HEADS_LINE_AT on, beside a line of #703 on every call, and a stand-in sleep that records
// each wait. Like the real command, the stand-in refuses a call that names no host or no session
// (`DISPATCH_HOST is not set`), and prints no line of a head then.
describe("fixture_heads", () => {
  function waitFor(lineAt: number) {
    const run = freshDir("heads-run");
    const co = freshDir("heads-checkout");
    const rigWork = freshDir("heads-work");
    const bin = path.join(co, "packages", "envoy-client", "bin");
    mkdirSync(bin, { recursive: true });
    writeFileSync(
      path.join(bin, "dispatch.ts"),
      `import { appendFileSync, existsSync, readFileSync } from "node:fs";
const calls = process.env.HEADS_CALLS ?? "";
const n = (existsSync(calls) ? readFileSync(calls, "utf8").split("\\n").filter(Boolean).length : 0) + 1;
appendFileSync(calls, \`\${JSON.stringify({ argv: process.argv.slice(2), url: process.env.DISPATCH_URL, token: process.env.DISPATCH_TOKEN_FILE, host: process.env.DISPATCH_HOST, session: process.env.DISPATCH_SESSION_ID })}\\n\`);
if (!process.env.DISPATCH_HOST || !process.env.DISPATCH_SESSION_ID) {
  console.log("dispatch: DISPATCH_HOST is not set.");
  process.exit(1);
}
console.log(\`- k1 (before_merge). Compared with the heads of 2 open pull requests. Read \${n}.\`);
console.log("  A pass stands: no, as 0 of 2 heads read yes");
console.log("  - #703 at ${"3".repeat(40)}: no, as fail f1 counts there.");
if (n >= Number(process.env.HEADS_LINE_AT)) console.log("  - #702 at ${"2".repeat(40)}: yes, from implementer, tester. Passes p1, p2 decide it.");
`
    );
    const calls = path.join(run, "calls.jsonl");
    const result = bash(
      [
        fn("fail"),
        fn("fixture_heads"),
        `sleep() { printf '%s\\n' "$1" >>"$R/sleeps"; }`,
        "R=$1 co=$2 work=$3 dispatch_port=4567 worker_key=LWEVAL-9",
        "fixture_heads 702",
      ].join("\n"),
      [run, co, rigWork],
      { HEADS_CALLS: calls, HEADS_LINE_AT: String(lineAt) }
    );
    const read = (name: string) =>
      existsSync(path.join(run, name)) ? readFileSync(path.join(run, name), "utf8") : "";
    const made = read("calls.jsonl").split("\n").filter(Boolean);
    return { ...result, run, rigWork, read, made: made.map((line) => JSON.parse(line)) };
  }

  // Catches: a wait that starts the agent before Dispatch learned #702's head, one that takes the
  // line of #703 for it, one that reads another Dispatch than the run's own, one that names no host
  // or session (the real command refuses it and prints no line of a head), and one that keeps no
  // record of what it read.
  test("a line of #702 on the third read passes, and that read's output is results-before.txt", () => {
    const { code, stderr, read, made, rigWork } = waitFor(3);
    expect({ code, stderr }).toEqual({ code: 0, stderr: "" });
    expect(made).toHaveLength(3);
    expect(made[0]).toEqual({
      argv: ["results", "--issue", "LWEVAL-9"],
      url: "http://127.0.0.1:4567",
      token: path.join(rigWork, "services", "dispatch-token"),
      host: "omp",
      session: "rig-fixture-heads",
    });
    expect(read("sleeps")).toBe("2\n2\n");
    expect(read("results-before.txt")).toContain("Read 3.");
    expect(read("results-before.txt")).toContain(`  - #702 at ${"2".repeat(40)}: yes`);
  });

  // Catches: a wait with no deadline, one shorter than 60 s, a failure that does not name the head
  // or the file, and one that leaves no record of the last read.
  test("no line of #702 in 60 s fails, naming #702 and the last read's file", () => {
    const { code, stderr, read, made, run } = waitFor(1000);
    expect(code).toBe(1);
    expect(stderr).toContain("#702");
    expect(stderr).toContain(path.join(run, "results-before.txt"));
    expect(made).toHaveLength(31);
    const waited = read("sleeps")
      .split("\n")
      .filter(Boolean)
      .reduce((sum, seconds) => sum + Number(seconds), 0);
    expect(waited).toBe(60);
    expect(read("results-before.txt")).toContain("Read 31.");
    expect(read("results-before.txt")).not.toContain("#702");
  });
});

// cmd_batch of reviewer-own-head runs each of its four cases <runs> times for each label, each
// as a run of its own. A stand-in rig.sh records each run's arguments, which cmd_batch hands to
// `rig.sh run` through xargs, and a stand-in go records the build of the runs' natsd.
describe("cmd_batch", () => {
  // Catches: a batch that runs one case alone, gives two runs one index (and so one PR number and
  // one commit time), drops the case from a run's arguments, or builds no natsd for the runs.
  test("reviewer-own-head runs each case for each label and run number, each with its own index", () => {
    const here = freshDir("batch-here");
    const rigWork = freshDir("batch-work");
    writeFileSync(path.join(here, "rig.sh"), `printf '%s\\n' "$*" >>"${here}/runs"\n`);
    const result = bash(
      [
        fn("fail"),
        fn("cmd_batch"),
        "load_label() { :; }",
        'lock_services() { S=$work/services; mkdir -p "$S"; }',
        "on_exit() { :; }",
        "services_up() { :; }",
        "date() { echo 1700000000; }",
        `go() { printf 'go %s\\n' "$*" >>"$here/builds"; }`,
        "here=$1 work=$2 root=/checkout",
        "cmd_batch reviewer-own-head 2 head ablate-head",
      ].join("\n"),
      [here, rigWork]
    );
    expect({ code: result.code, stderr: result.stderr }).toEqual({ code: 0, stderr: "" });
    const cases = ["sibling-fails", "ci-only", "no-implementer", "no-tester"];
    let index = 0;
    const want = ["head", "ablate-head"].flatMap((label) =>
      cases.flatMap((which) =>
        [1, 2].map((n) => `run reviewer-own-head ${label} ${n} ${++index} 1700000000 ${which}`)
      )
    );
    const runs = readFileSync(path.join(here, "runs"), "utf8").split("\n").filter(Boolean);
    expect(runs.toSorted()).toEqual(want.toSorted());
    expect(readFileSync(path.join(here, "builds"), "utf8")).toBe(
      `go -C /checkout/packages/envoy build -o ${rigWork}/services/bin/natsd ./internal/testnats/cmd/natsd\n`
    );
  });

  // Catches: a run whose name leaves out its case, so the cases' batches of one label, each
  // numbered from 1, replace each other's runs; a case outside the four; and a run directory whose
  // own name tells its agent the scenario, the case or the label (each is in the map alone).
  test("cmd_run names a reviewer-own-head run by its case in the map, in a directory of a random name, and refuses another case", () => {
    // rig.sh resolves its work directory's path once (realpath), as this does.
    const rigWork = realpathSync(freshDir("run-work"));
    mkdirSync(path.join(rigWork, "services"), { recursive: true });
    writeFileSync(path.join(rigWork, "services", "env"), "");
    const script = [
      fn("fail"),
      fn("cmd_run"),
      'load_label() { P=$work/profiles/0000000000000001; mkdir -p "$P"; echo "checkout /c/$1" >"$P/built-from"; }',
      "on_exit() { :; }",
      `run_reviewer_own_head() { printf '%s\\n' "$*" >"$R/ran"; echo exit=0 >"$R/out.txt"; }`,
      "work=$1 maps=$1/maps services_env=$1/services/env",
      'cmd_run reviewer-own-head head 1 3 1700000000 "$2"',
    ].join("\n");
    const link = path.join(rigWork, "maps", "runs", "reviewer-own-head-ci-only-head-1");
    const ran = bash(script, [rigWork, "ci-only"]);
    expect({ code: ran.code, stderr: ran.stderr }).toEqual({ code: 0, stderr: "" });
    const first = realpathSync(link);
    expect(path.relative(rigWork, first)).toMatch(/^runs\/[0-9a-f]{16}$/);
    expect(readFileSync(path.join(first, "ran"), "utf8")).toBe("3 1700000000 ci-only\n");
    expect(existsSync(path.join(first, "case.txt"))).toBe(false);
    expect(existsSync(path.join(first, "built-from"))).toBe(false);
    expect(
      readFileSync(
        path.join(rigWork, "maps", "built-from", "reviewer-own-head-ci-only-head-1"),
        "utf8"
      )
    ).toBe("checkout /c/head\n");
    // A second run of the name replaces the first: its directory and its link.
    expect(bash(script, [rigWork, "ci-only"]).code).toBe(0);
    expect(realpathSync(link)).not.toBe(first);
    expect(existsSync(first)).toBe(false);
    const refused = bash(script, [rigWork, "frobnicate"]);
    expect(refused.code).toBe(1);
    expect(refused.stderr).toContain(
      "'frobnicate' is not sibling-fails, ci-only, no-implementer or no-tester"
    );
  });
});

// The world of the heads scenarios: fixture_repo's bare remote, source repository and workspace,
// and heads_fixture's two pull request branches of one issue on it. worker_fixture, the
// tester-proof world, builds on the same fixture_repo, and keeps its world as it was.
describe("heads_fixture", () => {
  function build(fixture: "heads_fixture" | "worker_fixture") {
    const run = freshDir(`${fixture}-run`);
    const result = bash(
      [
        "set -euo pipefail",
        ...["fail", "g", "fixture_repo", "workspace_on", fixture].map(fn),
        "R=$1 worker_key=LWEVAL-9 worker_repo=example/widgets",
        `${fixture} 3 1700000000`,
      ].join("\n"),
      [run]
    );
    return { ...result, run };
  }
  const git = (dir: string, ...args: string[]) => {
    const result = Bun.spawnSync(["git", "-C", dir, ...args], { env: childEnv });
    if (result.exitCode !== 0) throw new Error(`git ${args.join(" ")}: ${result.stderr}`);
    return result.stdout.toString().trim();
  };
  /** The commit the workspace's working copy stands on, as jj reads it with the fixture's own
   * configuration. */
  const workspaceParent = (run: string) =>
    Bun.spawnSync(["jj", "log", "--no-graph", "-r", "@-", "-T", "commit_id"], {
      cwd: path.join(run, "ws"),
      env: { ...childEnv, JJ_CONFIG: path.join(run, "jj-setup.toml") },
    })
      .stdout.toString()
      .trim();

  // Catches: one branch where the case needs two heads, a head that is no commit of the remote, a
  // commit that is not one above main or that changes code (the reviewer then runs its
  // thermonuclear pair), a fixture that ignores the run's index and start, a workspace left on
  // main, and a world that names another pull request than #702.
  test("two branches of the issue, each one docs-only commit above main, and the world of #702", () => {
    const { code, stderr, run } = build("heads_fixture");
    expect({ code, stderr }).toEqual({ code: 0, stderr: "" });
    const remote = path.join(run, "remote.git");
    const heads = JSON.parse(readFileSync(path.join(run, "heads.json"), "utf8")) as Record<
      string,
      string
    >;
    expect(Object.keys(heads).toSorted()).toEqual(["702", "703"]);
    expect(heads["702"]).not.toBe(heads["703"]);
    const main = git(remote, "rev-parse", "main");
    for (const [pr, head] of Object.entries(heads)) {
      expect(head).toMatch(/^[0-9a-f]{40}$/);
      expect(git(remote, "rev-parse", `legion/LWEVAL-9-${pr}`)).toBe(head);
      expect(git(remote, "rev-parse", `${head}^`)).toBe(main);
      expect(git(remote, "rev-list", "--count", `main..${head}`)).toBe("1");
      const changed = git(remote, "diff", "--name-only", main, head).split("\n");
      expect(changed.length).toBeGreaterThan(0);
      expect(changed.filter((file) => !file.endsWith(".md"))).toEqual([]);
      expect(git(remote, "log", "-1", "--format=%at %ct", head)).toBe("1700000003 1700000003");
    }
    expect(JSON.parse(readFileSync(path.join(run, "world.json"), "utf8"))).toEqual({
      key: "LWEVAL-9",
      repo: "example/widgets",
      pr: 702,
      branch: "legion/LWEVAL-9-702",
      head: heads["702"],
    });
    expect(workspaceParent(run)).toBe(heads["702"]);
    expect(readFileSync(path.join(run, "pushes.log"), "utf8")).toBe("");
  });

  // Catches: a move of the shared part into fixture_repo that changes the tester's world: its
  // branch, its code commit, its PR number or its workspace.
  test("worker_fixture with the same inputs keeps legion/LWEVAL-9 one commit above main with greet.ts, and pr 1003", () => {
    const { code, stderr, run } = build("worker_fixture");
    expect({ code, stderr }).toEqual({ code: 0, stderr: "" });
    const remote = path.join(run, "remote.git");
    const head = git(remote, "rev-parse", "legion/LWEVAL-9");
    expect(git(remote, "rev-parse", `${head}^`)).toBe(git(remote, "rev-parse", "main"));
    expect(git(remote, "rev-list", "--count", `main..${head}`)).toBe("1");
    expect(git(remote, "diff", "--name-only", "main", head).split("\n")).toEqual([
      "greet.test.ts",
      "greet.ts",
    ]);
    expect(git(remote, "show", `${head}:greet.ts`)).toContain(
      "export function greet(name: string): string {"
    );
    expect(JSON.parse(readFileSync(path.join(run, "world.json"), "utf8"))).toEqual({
      key: "LWEVAL-9",
      repo: "example/widgets",
      pr: 1003,
      branch: "legion/LWEVAL-9",
      head,
    });
    expect(workspaceParent(run)).toBe(head);
    expect(readFileSync(path.join(run, "pushes.log"), "utf8")).toBe("");
  });
});
