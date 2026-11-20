import { afterAll, expect, test } from "bun:test";
import { closeSync, mkdtempSync, openSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { mkdirSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

import { rowLimit, watch, whether } from "./bounded-subprocess";

// What `bun run dispatch:local` (e2e/local-instance.ts) does with a signal during its first step,
// the run of packages/envoy/scripts/dev-postgres.sh: a Ctrl-C, a SIGTERM, a closing terminal's
// SIGHUP and its quit key's SIGQUIT each stop that step's process, which the command started
// detached, in a session of its own (`Supervisor.start`), so no signal sent to the command
// reaches it. A signal the command does not listen for ends it by its default action and leaves
// that process running.
//
// No database, build or container runs. The command checks only that its tools are on PATH
// before that step, so PATH leads with a directory of stand-ins: one for each tool, which does
// nothing, and a `bash` that becomes the step's process - it records its pid and becomes
// `sleep 276` under it, a command line no other process of this directory's tests has. The command
// runs under a shell that sets the core size to 0 and execs it, so a SIGQUIT that met its default
// action would write no core into the tree.
const dispatchPackage = fileURLToPath(new URL("../..", import.meta.url));
const stubs = mkdtempSync(join(tmpdir(), "local-instance-signals-"));
const seconds = "276";
const standIns: number[] = [];
afterAll(() => {
  for (const pid of standIns) {
    if (standInRuns(pid)) process.kill(pid, "SIGKILL");
  }
  rmSync(stubs, { force: true, recursive: true });
});

/** Whether pid is still the step's stand-in, read from /proc, so a pid given out again since is
 *  not taken for it. */
function standInRuns(pid: number): boolean {
  try {
    return readFileSync(`/proc/${pid}/cmdline`, "utf8").split("\0").filter(Boolean).join(" ") === `sleep ${seconds}`;
  } catch {
    return false;
  }
}

/** The pid the stand-in recorded, or 0 before it has. */
function recorded(pidFile: string): number {
  try {
    return Number(readFileSync(pidFile, "utf8"));
  } catch {
    return 0;
  }
}

for (const signal of ["SIGINT", "SIGTERM", "SIGHUP", "SIGQUIT"] as const) {
  test(
    `a ${signal} during dispatch:local's first step stops that step's process, not the command alone`,
    async () => {
      const dir = mkdtempSync(join(stubs, `${signal}-`));
      const bin = join(dir, "bin");
      mkdirSync(bin);
      for (const tool of ["go", "psql", "flock", "openssl", "python3", "docker"]) {
        writeFileSync(join(bin, tool), "#!/bin/sh\nexit 0\n", { mode: 0o755 });
      }
      const pidFile = join(dir, "step.pid");
      writeFileSync(
        join(bin, "bash"),
        `#!/bin/sh\necho $$ >'${pidFile}.new'\nmv '${pidFile}.new' '${pidFile}'\nexec sleep ${seconds}\n`,
        { mode: 0o755 }
      );
      // The command's output, and the stand-in's, which inherits it, go to a file: a pipe would
      // stay open for as long as a stand-in the command left behind runs.
      const said = join(dir, "said");
      const output = openSync(said, "w");
      const local = Bun.spawn(
        ["/bin/sh", "-c", 'ulimit -c 0 && exec "$@"', "sh", process.execPath, "e2e/local-instance.ts"],
        {
          cwd: dispatchPackage,
          env: { HOME: process.env.HOME, PATH: `${bin}:${process.env.PATH}`, TMPDIR: dir },
          stdio: ["ignore", output, output],
        }
      );
      closeSync(output);
      const ended = watch(local.exited);
      try {
        const started = await whether(() => standInRuns(recorded(pidFile)), ended);
        expect(started, `the step's stand-in never ran; the command said:\n${readFileSync(said, "utf8")}`).toBe(
          true
        );
        const standIn = recorded(pidFile);
        standIns.push(standIn);
        local.kill(signal);
        expect(await whether(ended), `dispatch:local still ran after its ${signal}`).toBe(true);
        expect(
          await whether(() => !standInRuns(standIn)),
          `the step's stand-in (pid ${standIn}) still runs after dispatch:local ended on a ${signal} ` +
            `(exit code ${local.exitCode}, signal ${local.signalCode}):\n${readFileSync(said, "utf8")}`
        ).toBe(true);
      } finally {
        if (!ended()) local.kill("SIGKILL");
      }
    },
    rowLimit("short", "short", "short")
  );
}
