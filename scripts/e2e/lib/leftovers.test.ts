import { beforeAll, describe, expect, test } from "bun:test";
import { chmodSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";

// lib/leftovers.sh: the one mark and the one removal of a shell stack's Postgres
// (packages/dispatch/e2e/postgres-mark.ts), and refuse_leftovers, which names what else an earlier
// run of a stage left. The image rows of packages/dispatch/e2e/__tests__/harness-postgres-leftover
// .test.ts start real shell stacks' containers and remove them, and lib/sweep-order.test.ts runs
// each driver's own file to hold it to the removal before its refusals and its creates; these rows
// hold the removal to bun, and refuse_leftovers to its reading of container names and of the
// moments a container was made and its owner started, with a `docker` of their own.

const lib = join(import.meta.dir, "leftovers.sh");
const dir = temporaryDirectory("leftovers-test");

describe("the removal of the Postgres of dead stacks", () => {
  test("a start with no bun on PATH fails with a line that names it", () => {
    const run = Bun.spawnSync(
      [
        "/bin/bash",
        "-c",
        `. ${JSON.stringify(lib)}; remove_leftover_postgres || echo "refused $?"`,
      ],
      { env: { PATH: "/nonexistent" } }
    );
    expect(run.stdout.toString()).toBe("refused 1\n");
    expect(run.stderr.toString()).toContain("lib/leftovers.sh: bun is required");
  });
});

/** A `docker` that answers refuse_leftovers from a fixture: `ps` prints each line of
 *  $FAKE_PS_LINES, and `inspect -f {{.Created}}` the time of $FAKE_CREATED. */
const fakeDocker = `#!/usr/bin/env bash
case "$1" in
  ps) printf '%s\\n' "$FAKE_PS_LINES" ;;
  inspect) printf '%s\\n' "$FAKE_CREATED" ;;
  *) exit 64 ;;
esac
`;

/** A pid no process of this machine has: one this row started and waited for. */
function deadPid(): number {
  const child = Bun.spawnSync(["bash", "-c", "echo $$"]);
  return Number(child.stdout.toString().trim());
}

describe("refuse_leftovers", () => {
  const bin = join(dir, "bin");
  beforeAll(() => {
    mkdirSync(bin);
    writeFileSync(join(bin, "docker"), fakeDocker);
    chmodSync(join(bin, "docker"), 0o755);
  });

  // refuse_leftovers also reads every process of this PID namespace, with three child processes
  // each, so a row's time grows with the machine's processes: 14 s alone on a dev box of 840,
  // where Bun's default of 5 s a row stopped both rows before refuse_leftovers ended.
  const walkBound = 120_000;

  // refuse_leftovers PREFIX run with the fake docker, which dates every container `created`; the
  // prefix is this row's, so no process of the machine carries its work directory. Answers the
  // shell's exit status on its first line, then what it printed.
  function refuse(prefix: string, lines: string[], created = new Date()) {
    const run = Bun.spawnSync(
      [
        "bash",
        "-c",
        `. ${JSON.stringify(lib)}; fail() { echo "FAIL: $*"; exit 1; }; refuse_leftovers ${prefix}; echo passed`,
      ],
      {
        env: {
          ...process.env,
          PATH: `${bin}:${process.env.PATH ?? ""}`,
          FAKE_PS_LINES: lines.join("\n"),
          FAKE_CREATED: created.toISOString(),
        },
      }
    );
    return `exit ${run.exitCode}\n${run.stdout.toString()}`;
  }

  test(
    "a dead run's NATS container is refused under main's name and under the pid and random name; a marked Postgres is the removal's, and a live run's container is no leftover",
    () => {
      const prefix = `legion-e2erow${process.pid}`;
      const dead = deadPid();
      const live = process.pid;
      const out = refuse(prefix, [
        `${prefix}-nats-${dead}|`,
        `${prefix}-nats-${dead}-0123456789abcdef|`,
        `${prefix}-pg-${dead}-0123456789abcdef|{"owner":{"pid":${dead}}}`,
        `${prefix}-nats-${live}-fedcba9876543210|`,
      ]);
      expect(out).toStartWith(
        `exit 1\nFAIL: an earlier ${prefix} run whose driver is gone left these running:`
      );
      expect(out).toContain(
        `container ${prefix}-nats-${dead} (docker rm -f -v ${prefix}-nats-${dead})`
      );
      expect(out).toContain(`container ${prefix}-nats-${dead}-0123456789abcdef (`);
      expect(out).not.toContain(`${prefix}-pg-`);
      expect(out).not.toContain(`${prefix}-nats-${live}-fedcba9876543210`);
    },
    walkBound
  );

  test(
    "a run whose every container is a live run's, or marked, passes",
    () => {
      const prefix = `legion-e2erow${process.pid}`;
      const out = refuse(prefix, [
        `${prefix}-nats-${process.pid}-fedcba9876543210|`,
        `${prefix}-pg-${deadPid()}|{"owner":null}`,
      ]);
      expect(out).toBe("exit 0\npassed\n");
    },
    walkBound
  );

  /** The fraction of a second past the whole second that btime, the boot's line of /proc/stat,
   *  drops. */
  function bootFraction(): number {
    const boot = Date.now() / 1000 - Number(readFileSync("/proc/uptime", "utf8").split(" ")[0]);
    return boot - Math.floor(boot);
  }

  const btime = Number(/^btime (\d+)$/m.exec(readFileSync("/proc/stat", "utf8"))?.[1]);
  const ticksPerSecond = Number(Bun.spawnSync(["getconf", "CLK_TCK"]).stdout.toString());

  /** The tick since the boot that a reading of whole seconds since btime gives the moment `ms` of
   *  the wall clock: the reading of a container's creation that refuse_leftovers must not make. */
  const wholeSeconds = (ms: number) => (Math.floor(ms / 1000) - btime) * ticksPerSecond;

  /** The tick since the boot in which the kernel dates a process's start (proc(5), stat field
   *  22). */
  function startTick(pid: number): number {
    const stat = readFileSync(`/proc/${pid}/stat`, "utf8");
    return Number(stat.slice(stat.lastIndexOf(") ") + 2).split(" ")[19]);
  }

  /** A live process of this row's, started at a moment of the wall clock whose fraction of a
   *  second is in [from, to - 30 ms), with the moments, in milliseconds, before and after its
   *  spawn, and whether whole seconds since btime misread the case the row makes of it
   *  (`misread`, from the kernel's own tick of the start). Where that range is narrower than
   *  50 ms the row has no start to wait for and says so (`window` false); where load moved the
   *  start out of it, it starts another, up to 20 times. */
  async function startedAt(
    from: number,
    to: number,
    misread: (tick: number, before: number, after: number) => boolean
  ) {
    const window = to - 0.03 - from >= 0.05;
    for (let tries = 1; ; tries++) {
      // A real wait for a fraction of the wall clock's second: the fault is in how the kernel's
      // ticks since the boot meet Docker's wall-clock creation and btime's whole second, and no
      // fake timer moves /proc or the `date` that refuse_leftovers runs in its own bash.
      while (
        window &&
        !((Date.now() % 1000) / 1000 >= from && (Date.now() % 1000) / 1000 < to - 0.03)
      )
        await Bun.sleep(5);
      const before = Date.now();
      const child = Bun.spawn(["sleep", "120"]);
      const after = Date.now() + 1;
      const read = misread(startTick(child.pid), before, after);
      if (read || !window || tries === 20) return { child, before, after, window, misread: read };
      child.kill();
    }
  }

  // Docker dates a container in nanoseconds and the kernel a process's start in ticks (10 ms):
  // refuse_leftovers compares them at that precision. A driver that made its container in the same
  // second it started, after a boot whose fraction of a second btime drops, read as younger than
  // its container when the creation was read in whole seconds since btime, and its live run's
  // container was refused. These rows start the driver in a tick of the boot's own wall-clock
  // second after the boot's fraction, where that reading refuses it: from 20 ms past the fraction
  // to 30 ms before the second ends. A boot in the last 100 ms of its second leaves no such range
  // (at its last 10 ms no such start at all: whole seconds then date each container made in its
  // owner's tick right), and there the row of the 50 ms below sets the case of that reading.
  for (const [form, suffix] of [
    ["<pid>", ""],
    ["<pid>-<random>", "-fedcba9876543210"],
  ] as const) {
    test(
      `a live run's container named ${form} and made in the tick its driver started is no leftover`,
      async () => {
        const started = await startedAt(
          bootFraction() + 0.02,
          1,
          (tick, _before, after) => tick > wholeSeconds(after)
        );
        try {
          const prefix = `legion-e2eyoung${process.pid}`;
          const name = `${prefix}-nats-${started.child.pid}${suffix}`;
          const out = refuse(prefix, [`${name}|`], new Date(started.after));
          // Where the boot's second leaves a window, the row's case is one whole seconds misread.
          expect({ out, caseSet: started.misread || !started.window }).toEqual({
            out: "exit 0\npassed\n",
            caseSet: true,
          });
        } finally {
          started.child.kill();
        }
      },
      walkBound
    );
  }

  // The same reading dates a container made 50 ms before a later process of its pid started, in
  // the same second and before the boot's fraction, at or after that start, so the dead run's
  // container read as the live one's: this row starts that process from 70 ms into a second to
  // 20 ms before the boot's fraction. A boot in the first 170 ms of its second leaves no such
  // range, and there the two rows above set the case.
  test(
    "a container made 50 ms before the process that now has its pid started is that dead run's leftover",
    async () => {
      const started = await startedAt(
        0.07,
        bootFraction() - 0.02,
        (tick, before) => tick <= wholeSeconds(before - 50)
      );
      try {
        const prefix = `legion-e2elater${process.pid}`;
        const name = `${prefix}-nats-${started.child.pid}-0123456789abcdef`;
        const out = refuse(prefix, [`${name}|`], new Date(started.before - 50));
        expect({
          failed: out.startsWith("exit 1\nFAIL: "),
          named: out.includes(`container ${name} (docker rm -f -v ${name})`),
          caseSet: started.misread || !started.window,
        }).toEqual({ failed: true, named: true, caseSet: true });
      } finally {
        started.child.kill();
      }
    },
    walkBound
  );
});
