import { afterEach, beforeEach } from "bun:test";
import { REMOVAL_BOUND_MS } from "@legion/test-fixtures/temporary-directories";

/** One thing a row waits for: what it is, and the bound it must end within, in milliseconds. */
export interface Wait {
  readonly part: string;
  readonly boundMs: number;
}

/**
 * What a row's limit holds past the bounds of what it waits for, in milliseconds: bun's own 5 s,
 * the bound bun gives a row that runs no process, for the row's own reads and assertions.
 */
export const ROW_MARGIN_MS = 5_000;

/**
 * The limit of a row of this directory's test files, in milliseconds: the bound of each thing the
 * row waits for, one after another, the fixture's removal of the row's directories
 * (REMOVAL_BOUND_MS), and ROW_MARGIN_MS.
 */
export function rowLimit(...waits: readonly Wait[]): number {
  return waits.reduce((sum, wait) => sum + wait.boundMs, REMOVAL_BOUND_MS + ROW_MARGIN_MS);
}

/** When the running row started, and the limit its file gave it (fileRowLimit). */
let rowStarted = 0;
let rowLimitMs = Number.POSITIVE_INFINITY;

/**
 * rowLimit for every row of the calling file, which passes it to `setDefaultTimeout`. It also
 * registers, in the file's scope, the hooks that start each row's clock and stop it after the row,
 * so spawnWithin gives a process no more than its row has left: a process that would outlast the
 * row's limit is killed ROW_MARGIN_MS before it, and the row fails naming that process, never on
 * bun's own timeout. A hook that runs between rows (a describe's beforeAll or afterAll) runs on no
 * row's clock, so its processes get their own bounds, not what an earlier row left.
 */
export function fileRowLimit(...waits: readonly Wait[]): number {
  const limit = rowLimit(...waits);
  beforeEach(() => {
    rowStarted = performance.now();
    rowLimitMs = limit;
  });
  afterEach(() => {
    rowLimitMs = Number.POSITIVE_INFINITY;
  });
  return limit;
}

/**
 * `Bun.spawnSync(command, options)` for `wait`: the process is killed at `wait.boundMs`, or where
 * the row's limit leaves it less, and then the row fails naming `wait.part`.
 */
export function spawnWithin(
  wait: Wait,
  command: string[],
  options: {
    readonly cwd?: string;
    readonly env?: Record<string, string | undefined>;
    readonly stdout?: "pipe";
    readonly stderr?: "pipe";
  } = {}
) {
  const left = Math.floor(rowLimitMs - (performance.now() - rowStarted) - ROW_MARGIN_MS);
  const timeout = Math.max(1, Math.min(wait.boundMs, left));
  const result = Bun.spawnSync(command, {
    ...options,
    stdout: "pipe",
    stderr: "pipe",
    timeout,
    killSignal: "SIGKILL",
  });
  if (result.exitedDueToTimeout && timeout < wait.boundMs) {
    throw new Error(
      `${wait.part} did not end within the ${timeout} ms its row's limit of ${rowLimitMs} ms left it: the limit counts less than the parts the row waits for`
    );
  }
  if (result.exitedDueToTimeout) {
    throw new Error(`${wait.part} did not end within its bound of ${wait.boundMs} ms`);
  }
  return result;
}

/**
 * A step of rig.sh run as a process of its own (a `bash -c` of its functions, or `rig.sh
 * <command>`). The build of the heads fixture took 22 to 59 s in 18 of 20 runs at 1-minute loads
 * of 436 to 621 on the devbox, and passed 60 s in the other two; it took 6.8 to 44 s at loads of
 * 530 to 830. Four times the slowest run that finished.
 */
export const RIG_STEP: Wait = { part: "a rig.sh step", boundMs: 240_000 };

/**
 * One git or jj command. In the heads fixture's build at a 1-minute load of 830 the slowest was
 * `jj git clone`, 7.5 s, and a `git commit` took up to 4.6 s.
 */
export const VCS_COMMAND: Wait = { part: "a git or jj command", boundMs: 60_000 };

/**
 * One of this directory's scripts run by bun as a process of its own (`score.ts`,
 * `hidden-proof.ts`, `visible-world.ts`, `gh-standin.ts`). The slowest measured was score.ts over
 * its staged runs, 8.2 s at a load of 268, and a row of score.test.ts that runs it took 37 s at a
 * load of 410.
 */
export const SCRIPT_RUN: Wait = { part: "a bun script", boundMs: 120_000 };

/**
 * One run of label-checkouts.sh: three or four clones of the fixture repository, each with a
 * `bun install`. Its slowest row took 19.4 s at a load of 372.
 */
export const LABEL_CHECKOUTS: Wait = { part: "label-checkouts.sh", boundMs: 120_000 };

/**
 * One batch of rig-isolation.test.ts's rig, its two runs started side by side, so the batch waits
 * for the slower run. Its slowest row took 14 s at a load of 344.
 */
export const RIG_BATCH: Wait = { part: "a batch of rig.sh run", boundMs: 120_000 };
