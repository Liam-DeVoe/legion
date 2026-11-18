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

  /** A live process of this row's, started at a moment of the wall clock whose fraction of a
   *  second is past btime's by at least 50 ms: where a reading of whole seconds since btime dates
   *  a container made in the same second before the process's own start. Answers the process and
   *  the moments, in milliseconds, before and after its spawn. */
  async function startedPastTheBootsFraction() {
    const from = Math.min(bootFraction() + 0.05, 0.9);
    // A real wait for a fraction of the wall clock's second: the fault is in how the kernel's
    // ticks since the boot meet Docker's wall-clock creation and btime's whole second, and no fake
    // timer moves /proc or the `date` that refuse_leftovers runs in its own bash.
    while (!((Date.now() % 1000) / 1000 > from && (Date.now() % 1000) / 1000 < 0.95)) {
      await Bun.sleep(5);
    }
    const before = Date.now();
    const child = Bun.spawn(["sleep", "120"]);
    const after = Date.now() + 1;
    return { child, before, after };
  }

  // Docker dates a container in nanoseconds and the kernel a process's start in ticks (10 ms):
  // refuse_leftovers compares them at that precision. A driver that made its container in the same
  // second it started, after a boot whose fraction of a second btime drops, read as younger than
  // its container when the creation was read in whole seconds since btime, and its live run's
  // container was refused.
  for (const [form, suffix] of [
    ["<pid>", ""],
    ["<pid>-<random>", "-fedcba9876543210"],
  ] as const) {
    test(
      `a live run's container named ${form} and made in the tick its driver started is no leftover`,
      async () => {
        const { child, after } = await startedPastTheBootsFraction();
        try {
          const prefix = `legion-e2eyoung${process.pid}`;
          const out = refuse(prefix, [`${prefix}-nats-${child.pid}${suffix}|`], new Date(after));
          expect(out).toBe("exit 0\npassed\n");
        } finally {
          child.kill();
        }
      },
      walkBound
    );
  }

  test(
    "a container made 50 ms before the process that now has its pid started is that dead run's leftover",
    async () => {
      const { child, before } = await startedPastTheBootsFraction();
      try {
        const prefix = `legion-e2elater${process.pid}`;
        const name = `${prefix}-nats-${child.pid}-0123456789abcdef`;
        const out = refuse(prefix, [`${name}|`], new Date(before - 50));
        expect(out).toStartWith("exit 1\nFAIL: ");
        expect(out).toContain(`container ${name} (docker rm -f -v ${name})`);
      } finally {
        child.kill();
      }
    },
    walkBound
  );
});
