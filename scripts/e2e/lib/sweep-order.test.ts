import { afterAll, describe, expect, test } from "bun:test";
import {
  chmodSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  realpathSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { mark, refusals, sweep, sweepOrder } from "./sweep-order";

// R3 of pull request #4236: "The next start of a test stack removes each Postgres of a stack that
// it can show dead. … It leaves a Postgres whose stack it cannot judge, and it names that Postgres
// in one line." A shell stack's start that refuses, because of what a dead run left, before it
// runs remove_leftover_postgres never reaches the removal while those leftovers stay: the dead
// stack's Postgres stays and no line names it. A start that creates its own Postgres before the
// removal starts it beside the dead stacks' (lib/sweep-order.ts names the refusals and the mark).
//
// The first rows run the search over this tree and over planted scripts. The rest run each
// driver's own file, in its own order: lib/sweep-order.ts names the drivers, the refusals each one
// reaches and each mark of its own Postgres, and each row makes one refusal refuse, or lets the
// driver reach one create of its Postgres, and requires that the removal ran first.

const root = join(import.meta.dir, "..", "..", "..");
const scratch = mkdtempSync(join(tmpdir(), "sweep-order-"));
afterAll(() => rmSync(scratch, { recursive: true, force: true }));

describe("the search for a start that refuses or creates its Postgres before it removes the Postgres of dead stacks", () => {
  test("no driver of this tree refuses or marks before its removal, and the search reads every driver", () => {
    const { drivers, findings } = sweepOrder(root);
    // A search that read nothing passed nothing: every shell stack that starts a Postgres
    // container runs the removal (leftovers.test.ts's list), so each one is a driver here.
    expect(drivers.map((each) => each.driver).sort()).toEqual([
      "packages/pi-legion/scripts/skill-scenarios/rig.sh",
      "scripts/e2e/claude-backend-sandbox.sh",
      "scripts/e2e/claude-backend-tmux.sh",
      "scripts/e2e/controller-start-tmux.sh",
      "scripts/e2e/controller-sweep-kind.sh",
      "scripts/e2e/dispatch-user-turns.sh",
      "scripts/e2e/parked-decision-devbox.sh",
      "scripts/e2e/stage1-skeleton.sh",
      "scripts/e2e/stage2-tmux-supervision.sh",
      "scripts/e2e/stage3-devbox-workflow.sh",
      "scripts/e2e/stage4a-sandbox-runtime.sh",
      "scripts/e2e/stage4b-sandbox-tree.sh",
      "scripts/e2e/verifiers-staging-token.sh",
    ]);
    expect(findings).toEqual([]);
  });

  test("every script that names a Postgres image is a driver, or holds a removal a driver reaches", () => {
    // A Postgres image as the drivers name one: the pinned pgvector or PostgreSQL digest, through
    // the mirror or through the repository's own copy, or the session database's image variable.
    const postgresImage =
      /pgvector\/pgvector@sha256:|library\/postgres@sha256:|legion\/ci@sha256:(?:2358fcba|979bccfd)|"\$session_db_image"/;
    const named = [
      ...new Bun.Glob("scripts/e2e/*.sh").scanSync({ cwd: root }),
      ...new Bun.Glob("scripts/e2e/lib/*.sh").scanSync({ cwd: root }),
      "packages/pi-legion/scripts/skill-scenarios/rig.sh",
    ].filter((path) => postgresImage.test(readFileSync(join(root, path), "utf8")));
    const { drivers } = sweepOrder(root);
    const removing = new Set([
      ...drivers.map((each) => each.driver),
      ...drivers.flatMap((each) =>
        each.calls.filter((call) => call.name === sweep).map((call) => call.at.replace(/:\d+$/, ""))
      ),
    ]);
    expect(named.length).toBeGreaterThan(0);
    expect(named.filter((path) => !removing.has(path))).toEqual([]);
  });

  test("a planted driver whose refusal or mark comes first, directly or through a function of a library it sources, is found", () => {
    const planted = join(scratch, "planted");
    mkdirSync(join(planted, "scripts", "e2e", "lib"), { recursive: true });
    writeFileSync(
      join(planted, "scripts", "e2e", "lib", "planted.sh"),
      "check_ports() {\n  ports_free 1234\n}\n"
    );
    writeFileSync(
      join(planted, "scripts", "e2e", "first.sh"),
      [
        "#!/usr/bin/env bash",
        '. "$root/scripts/e2e/lib/planted.sh"',
        "begin() {",
        "  refuse_leftovers legion-planted",
        "}",
        "begin",
        "check_ports",
        'pg_mark=$(postgres_mark "$work" "$pg") || fail "no mark"',
        'remove_leftover_postgres || fail "no removal"',
        "",
      ].join("\n")
    );
    writeFileSync(
      join(planted, "scripts", "e2e", "after.sh"),
      'remove_leftover_postgres || fail "no removal"\nrefuse_leftovers legion-after\n'
    );
    const { drivers, findings } = sweepOrder(planted);
    expect(drivers.map((each) => each.driver)).toEqual([
      "scripts/e2e/after.sh",
      "scripts/e2e/first.sh",
    ]);
    expect(findings).toEqual([
      {
        driver: "scripts/e2e/first.sh",
        before: [
          { name: "refuse_leftovers", at: "scripts/e2e/first.sh:4" },
          { name: "ports_free", at: "scripts/e2e/lib/planted.sh:2" },
          { name: "postgres_mark", at: "scripts/e2e/first.sh:8" },
        ],
        sweep: "scripts/e2e/first.sh:9",
      },
    ]);
  });

  test("the search's command says UNKNOWN, not clean, where it read no driver", () => {
    const empty = mkdtempSync(join(scratch, "empty-"));
    const run = Bun.spawnSync(["bun", join(import.meta.dir, "sweep-order.ts"), empty]);
    expect({ code: run.exitCode, out: run.stdout.toString() }).toEqual({
      code: 2,
      out: expect.stringContaining("UNKNOWN: no script under"),
    });
  });
});

/** A stand-in for each command a driver runs on its way, and for mktemp, which makes each
 *  directory under $STANDIN_ROOT. Each records its call in $STANDIN_CALLS. `docker ps` of a name
 *  prefix lists a dead run's NATS container where $STANDIN_DEAD_PID is set; `ss` lists a listener
 *  on the port it is asked of where $STANDIN_TAKEN is set; `bun … sweep` is the removal, recorded
 *  as SWEEP, and `bun … label` is the real mark command, which only prints a label. A `docker run`
 *  or `docker create` is recorded as CREATE, with its name and its mark, where it carries a mark,
 *  else as RUN. $STANDIN_STOP_AT ends the driver with SIGTERM at the create of that number, or,
 *  set to `first`, at the first of the removal and any container. */
const standIn = `#!/usr/bin/env bash
name=\${0##*/}
printf '%s %s\\n' "$name" "$*" >>"$STANDIN_CALLS"
stop() { kill -TERM "$GATE_DRIVER"; }
case $name in
  mktemp)
    flags=() template=tmp.XXXXXXXXXX
    for a in "$@"; do case $a in -*) flags+=("$a") ;; *) template=$a ;; esac; done
    exec /usr/bin/mktemp "\${flags[@]}" "$STANDIN_ROOT/\${template##*/}" ;;
  docker)
    case \${1:-} in
      ps)
        for a in "$@"; do
          case $a in name=^*-) [ -z "\${STANDIN_DEAD_PID:-}" ] || printf '%snats-%s|\\n' "\${a#name=^}" "$STANDIN_DEAD_PID" ;; esac
        done ;;
      inspect) date -u +%FT%T.%NZ ;;
      run | create)
        label='' container='' previous=''
        for a in "$@"; do
          case $previous in
            --label) case $a in dev.legion.dispatch-e2e.postgres-mark=*) label=\${a#*=} ;; esac ;;
            --name) container=$a ;;
          esac
          previous=$a
        done
        if [ -n "$label" ]; then
          printf 'CREATE %s %s\\n' "$container" "$label" >>"$STANDIN_CALLS"
          [ "\${STANDIN_STOP_AT:-}" != "$(grep -c '^CREATE ' "$STANDIN_CALLS")" ] || stop
        else
          printf 'RUN %s\\n' "$container" >>"$STANDIN_CALLS"
        fi
        [ "\${STANDIN_STOP_AT:-}" != first ] || stop
        printf 'id-%s\\n' "$container" ;;
    esac ;;
  bun)
    case " $* " in
      *" sweep "*) echo SWEEP >>"$STANDIN_CALLS"; [ "\${STANDIN_STOP_AT:-}" != first ] || stop ;;
      *" label "*) exec "$STANDIN_BUN" "$@" ;;
    esac ;;
  ss) [ -z "\${STANDIN_TAKEN:-}" ] || echo "LISTEN 0 4096 127.0.0.1:\${*##*:} 0.0.0.0:* users:((\\"stand-in\\",pid=1,fd=3))" ;;
esac
exit 0
`;

const standIns = [
  "agent-secrets",
  "aws",
  "bun",
  "claude",
  "curl",
  "docker",
  "flock",
  "gh",
  "getent",
  "go",
  "hawk-token",
  "jj",
  "jq",
  "kubectl",
  "mise",
  "mktemp",
  "openssl",
  "pgrep",
  "python3",
  "secrets",
  "shellcheck",
  "sleep",
  "ss",
  "sudo",
  "tmux",
];

/** What a row cuts: the functions that run whole, the other names that run, and whether the
 *  driver's own `set` runs. A row that runs the driver to its creates skips `set`, so no errexit or
 *  nounset ends the driver at a line that reads what a skipped command would have made: the shell
 *  still reaches each line in the order the driver's own lines and branches give. */
interface Cut {
  readonly run: readonly string[];
  readonly follow: readonly string[];
  readonly keepSet: boolean;
}

/** The shell that runs a driver's own file in its own order with what is slow or outside the
 *  machine cut. A DEBUG trap (bash's extdebug: a trap that answers non-zero skips the command)
 *  runs, at the driver's top level and in each function on its way: each assignment, source,
 *  `case`, test and builtin of the shell's own control, every command in a subshell or a command
 *  substitution, the whole of each function of `run`, and each call of a name of `follow`; it
 *  skips every other command, and names each in $GATE_SKIPPED. The cut cannot change the order of
 *  the calls the rows read: it runs every line of the driver on the way to them and skips only
 *  commands that call none of them, so the shell reaches them in the order the driver's own lines
 *  and branches give. What a skipped command would have refused (a missing tool, an input left
 *  unset) is never refused, and the driver's own traps are skipped too, so a driver the row ends
 *  runs no cleanup. */
function gate({ run, follow, keepSet }: Cut): string {
  return `shopt -s extdebug
set -T
export GATE_DRIVER=$$
__gate_run=" ${run.join(" ")} "
__gate_follow=" ${[...run, ...follow].join(" ")} "
__gate_skip() {
  [ "$BASH_SUBSHELL" -eq 0 ] || return 1
  local frame
  for frame in "\${FUNCNAME[@]:1}"; do
    case $__gate_run in *" $frame "*) return 1 ;; esac
  done
  local word=\${BASH_COMMAND%%[[:space:]]*}
  case $word in
    *=* | . | source | ${keepSet ? "set | " : ""}shopt | shift | '[[' | '[' | test | true | false | : | return | break | continue | local | declare | typeset | export | readonly | unset | read | mapfile | readarray | case) return 1 ;;
  esac
  case $__gate_follow in *" $word "*) return 1 ;; esac
  printf '%s\\n' "$BASH_COMMAND" >>"$GATE_SKIPPED"
  return 0
}
trap '! __gate_skip' DEBUG
# A skipped background start leaves $! as it was; one no-op job gives it a pid, of a job that ended.
: &
wait "$!"
. "$0" "$@"`;
}

/** What makes each refusal refuse, and the words of its refusal. */
const refuses: Record<(typeof refusals)[number], { env: Record<string, string>; said: RegExp }> = {
  refuse_leftovers: {
    env: {},
    said: /an earlier \S+ run whose driver is gone left these running: container \S+-nats-\d+ /,
  },
  ports_free: { env: { STANDIN_TAKEN: "1" }, said: /port \d+ is taken on the devbox/ },
  lock_services: { env: {}, said: /a batch already holds \S+\/services\/up/ },
};

/** The ports a driver's operator gives it, which a driver reads at its top before any refusal. No
 *  row binds one or asks the machine of one: `ss` and `docker` are the stand-ins above, and each
 *  number is only text in a refusal's line. */
const operatorInputs = {
  LEGION_E2E_DAEMON_PORT: "13372",
  LEGION_E2E_WORKER_STREAM_PORT: "13373",
  LEGION_E2E_DISPATCH_PORT: "13374",
  LEGION_E2E_ENVOY_PORT: "13375",
  LEGION_E2E_NATS_PORT: "13376",
  LEGION_E2E_SESSION_DB_PORT: "13377",
};

/** The arguments and inputs a driver needs to reach its refusals at all. */
const startOf: Record<string, { args: string[]; env: Record<string, string> }> = {
  "packages/pi-legion/scripts/skill-scenarios/rig.sh": {
    args: ["batch", "askgate", "0", "on"],
    env: { SKILL_SCENARIOS_ASKGATE_CASE: "facts" },
  },
};

/** A pid no process of this machine has: one this row started and waited for. */
function deadPid(): string {
  return Bun.spawnSync(["bash", "-c", "echo $$"]).stdout.toString().trim();
}

// refuse_leftovers reads every process of its PID namespace, three child processes each, so a
// row's time grows with the machine's processes (14 s on a dev box of 840; leftovers.test.ts).
const walkBound = 120_000;

const bin = join(scratch, "bin");
mkdirSync(bin);
writeFileSync(join(bin, "stand-in"), standIn);
chmodSync(join(bin, "stand-in"), 0o755);
for (const name of standIns) symlinkSync("stand-in", join(bin, name));
const bearer = join(scratch, "dispatch-token");
writeFileSync(bearer, "stand-in-bearer\n", { mode: 0o600 });

/** One run of `driver`'s own file through the gate, with `env` over the row's own: what it said,
 *  the stand-ins' log, what the cut skipped, its pid and its run directory. */
function start(
  driver: string,
  cut: Cut,
  env: Record<string, string>,
  setUp: (dir: string) => void = () => {}
) {
  const dir = mkdtempSync(join(scratch, "run-"));
  const calls = join(dir, "calls.log");
  const skipped = join(dir, "skipped.log");
  writeFileSync(calls, "");
  writeFileSync(skipped, "");
  for (const each of ["home", "tmp", "xdg", "work/services"])
    mkdirSync(join(dir, each), { recursive: true });
  setUp(dir);
  const started = Bun.spawnSync(
    ["bash", "-c", gate(cut), join(root, driver), ...(startOf[driver]?.args ?? [])],
    {
      env: {
        HOME: join(dir, "home"),
        USER: process.env.USER ?? "legion",
        PATH: `${bin}:/usr/bin:/bin`,
        TMPDIR: join(dir, "tmp"),
        XDG_RUNTIME_DIR: join(dir, "xdg"),
        SKILL_SCENARIOS_WORK: join(dir, "work"),
        STANDIN_ROOT: join(dir, "tmp"),
        STANDIN_CALLS: calls,
        STANDIN_BUN: process.execPath,
        GATE_SKIPPED: skipped,
        ...operatorInputs,
        LEGION_E2E_DISPATCH_TOKEN_FILE: bearer,
        ...startOf[driver]?.env,
        ...env,
      },
      timeout: walkBound - 10_000,
    }
  );
  return {
    exit: started.exitCode,
    pid: started.pid,
    dir,
    said: `${started.stdout.toString()}${started.stderr.toString()}`,
    log: readFileSync(calls, "utf8").split("\n"),
    // The last commands the cut skipped, for a row that fails: what came before the driver
    // stopped short of what the row waits for.
    cut: readFileSync(skipped, "utf8").trim().split("\n").slice(-4),
  };
}

const { drivers } = sweepOrder(root);

describe("each driver runs the removal of the Postgres of dead stacks before each refusal, in its own order", () => {
  for (const driver of drivers) {
    for (const kind of refusals.filter((name) => driver.calls.some((call) => call.name === name))) {
      test(
        `${driver.driver}: the removal runs though ${kind} refuses what a dead run left`,
        () => {
          const ran = start(
            driver.driver,
            { run: [...refusals, sweep], follow: driver.reaching, keepSet: true },
            {
              ...refuses[kind].env,
              ...(kind === "refuse_leftovers" ? { STANDIN_DEAD_PID: deadPid() } : {}),
            },
            // A batch killed outright leaves its lock (rig.sh's lock_services).
            (dir) =>
              kind === "lock_services" && writeFileSync(join(dir, "work", "services", "up"), "")
          );
          expect({
            exit: ran.exit,
            refused: refuses[kind].said.test(ran.said),
            removalRan: ran.log.includes("SWEEP"),
            said: ran.said.trim().split("\n").slice(-4),
            cut: ran.cut,
          }).toEqual({
            exit: 1,
            refused: true,
            removalRan: true,
            said: expect.any(Array),
            cut: expect.any(Array),
          });
        },
        walkBound
      );
    }
  }
});

// The rows of each create run the driver past its refusals to its creates: they cut the refusals
// themselves, which either pass, calling nothing, or end the start, so the cut cannot move a
// create before or after the removal; and a dead run another program of the machine left cannot
// refuse the row.
const createCut = (reaching: readonly string[]): Cut => ({
  run: [sweep, mark, "start_container"],
  follow: [...reaching, "docker", "session_db_start"].filter(
    (name) => !(refusals as readonly string[]).includes(name)
  ),
  keepSet: false,
});

describe("each driver runs the removal of the Postgres of dead stacks before each create of its own Postgres, in its own order", () => {
  for (const driver of drivers) {
    const marks = driver.calls.filter((call) => call.name === mark);
    marks.forEach((marked, index) => {
      test(
        `${driver.driver}: the removal runs before its Postgres ${index + 1} of ${marks.length} (marked at ${marked.at}) is created, and the create carries its mark`,
        () => {
          const ran = start(driver.driver, createCut(driver.reaching), {
            STANDIN_STOP_AT: String(index + 1),
          });
          const creates = ran.log.filter((line) => line.startsWith("CREATE "));
          const created = creates[index] ?? "";
          const [, name = "", label = "{}"] = /^CREATE (\S*) (.*)$/.exec(created) ?? [];
          const marking = JSON.parse(label) as {
            owner?: { pid: number };
            run?: string;
            container?: string;
            sockets?: string;
          };
          expect({
            created: created !== "",
            removalFirst:
              ran.log.indexOf("SWEEP") >= 0 && ran.log.indexOf("SWEEP") < ran.log.indexOf(created),
            owner: marking.owner?.pid,
            underRun: realpathSync(marking.run ?? "/").startsWith(realpathSync(ran.dir)),
            container: marking.container,
            sockets: marking.sockets,
            said: ran.said.trim().split("\n").slice(-4),
            cut: ran.cut,
          }).toEqual({
            created: true,
            removalFirst: true,
            owner: ran.pid,
            underRun: true,
            container: name,
            sockets: undefined,
            said: expect.any(Array),
            cut: expect.any(Array),
          });
        },
        walkBound
      );
    });

    // A start that uses a Postgres it was given (CI's service) creates none, and still removes the
    // Postgres of dead stacks first, before any container of its own.
    if (readFileSync(join(root, driver.driver), "utf8").includes("LEGION_E2E_PG_DSN")) {
      test(
        `${driver.driver}: with LEGION_E2E_PG_DSN set, the removal runs before any container of the start`,
        () => {
          const ran = start(driver.driver, createCut(driver.reaching), {
            LEGION_E2E_PG_DSN: "postgres://stand-in@127.0.0.1:1/stand-in",
            STANDIN_STOP_AT: "first",
          });
          const first = ran.log.find((line) => /^(SWEEP|RUN |CREATE )/.test(line));
          expect({ first, said: ran.said.trim().split("\n").slice(-4), cut: ran.cut }).toEqual({
            first: "SWEEP",
            said: expect.any(Array),
            cut: expect.any(Array),
          });
        },
        walkBound
      );
    }
  }
});
