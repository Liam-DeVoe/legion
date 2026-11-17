/** What the rows of the check `v-no-leftover-postgres` plant beside the marks a stack writes: a
 *  live stand-in process, and a hostile mark that names a program of the planter's and a directory
 *  outside its run (`harness-postgres-marks.test.ts`, `harness-postgres-leftover.test.ts`). */

import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { join } from "node:path";
import type { Subprocess } from "bun";

import { marksDirectory, startOf } from "../harness-postgres-marks";

/** The start of the process `pid` as `/proc` gives it, or "" where it is gone or a zombie. */
function startTimeOf(pid: number): string {
  try {
    const stat = readFileSync(`/proc/${pid}/stat`, "utf8");
    const state = stat.slice(stat.lastIndexOf(")") + 2, stat.lastIndexOf(")") + 3);
    return state === "Z" || state === "X" ? "" : startOf(stat);
  } catch {
    return "";
  }
}

/** A live process whose working directory is `cwd`, as a postmaster's is its data directory. */
export interface StandIn {
  readonly pid: number;
  alive(): boolean;
  /** SIGKILL, only while `/proc` still names the process by its pid and its start. */
  stop(): void;
}

export function standIn(cwd: string, command = ["sleep", "600"]): StandIn {
  const child: Subprocess = Bun.spawn(command, { cwd, stdout: "ignore", stderr: "ignore" });
  const start = startTimeOf(child.pid);
  const alive = () => start !== "" && startTimeOf(child.pid) === start;
  return {
    pid: child.pid,
    alive,
    stop: () => {
      if (alive()) child.kill("SIGKILL");
    },
  };
}

/** A directory in `tmp` that holds a `pg_ctl` which records that it ran, in `marker`, and then
 *  kills the process the data directory's `postmaster.pid` names: the program a hostile mark names
 *  as its postmaster's binaries. It uses shell builtins alone, so it runs under any `PATH`. */
export function plantedBin(tmp: string, marker: string): string {
  const bin = mkdtempSync(join(tmp, "planted-bin-"));
  writeFileSync(
    join(bin, "pg_ctl"),
    `#!/bin/sh\nprintf 'ran\\n' >${JSON.stringify(marker)}\n` +
      `read -r pid <"$2/postmaster.pid"\nkill -9 "$pid"\n`,
    { mode: 0o755 }
  );
  return bin;
}

/** What a start must leave of a hostile mark, as `kept` of `HostileMark` reads it. */
export interface HostileMarkState {
  /** Whether the planted program ran: it wrote its marker. */
  readonly ran: boolean;
  /** Whether the stand-in its `postmaster.pid` names still runs. */
  readonly lives: boolean;
  /** Whether the directory outside the run still holds its file. */
  readonly outside: boolean;
  /** Whether the mark file is still there. */
  readonly mark: boolean;
}

/** A hostile mark a row planted (`plantHostileMark`). */
export interface HostileMark {
  /** The file the planted program writes when it runs. */
  readonly marker: string;
  readonly live: StandIn;
  /** The mark file. */
  readonly file: string;
  /** The link out of the run, `worker-0`. */
  readonly link: string;
  kept(): HostileMarkState;
}

/** The mark of stack 0 in `run`, whose owner is `owner` and whose postmaster is a program of the
 *  planter's (`plantedBin`) serving from `worker-0/pgdata`, where `worker-0` is a link to a
 *  directory of `tmp` outside the run, in which a live stand-in has that data directory for its
 *  working directory and is named by its `postmaster.pid`. */
export function plantHostileMark(run: string, tmp: string, owner: unknown): HostileMark {
  const marker = join(mkdtempSync(join(tmp, "marker-")), "the-planted-program-ran");
  const outside = mkdtempSync(join(tmp, "outside-"));
  mkdirSync(join(outside, "pgdata"));
  const live = standIn(join(outside, "pgdata"));
  writeFileSync(join(outside, "pgdata", "PG_VERSION"), "18\n");
  writeFileSync(join(outside, "pgdata", "postmaster.pid"), `${live.pid}\n`);
  symlinkSync(outside, join(run, "worker-0"));
  const sockets = mkdtempSync(join(run, "pg-0-"));
  mkdirSync(join(run, marksDirectory), { recursive: true });
  const file = join(run, marksDirectory, "worker-0.json");
  const postmaster = { bin: plantedBin(tmp, marker), data: join(run, "worker-0", "pgdata") };
  writeFileSync(file, JSON.stringify({ owner, run, sockets, postmaster }));
  return {
    marker,
    live,
    file,
    link: join(run, "worker-0"),
    kept: () => ({
      ran: existsSync(marker),
      lives: live.alive(),
      outside: existsSync(join(outside, "pgdata", "PG_VERSION")),
      mark: existsSync(file),
    }),
  };
}

/** What `kept` of `HostileMark` reads once a start left the hostile mark as it must. */
export const hostileMarkKept: HostileMarkState = {
  ran: false,
  lives: true,
  outside: true,
  mark: true,
};
