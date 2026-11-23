/** What the binaries provider of e2e/harness-postgres.ts does when the postmaster it started ends
 *  while the start waits for it to answer `select 1`, as a server that crashes on its way up does,
 *  and when the template's server will not stop once the template is whole. The wait (`ready`) has
 *  no bound of its own and reads the postmaster's end through `postmasterGone`: a refusal must name
 *  the server that stopped, at once, rather than wait for ever. And a template is copied only once
 *  its server has stopped: a stop that fails must refuse the start by name. Found by Tester4958-2,
 *  whose plants read every postmaster as running, read a data directory with no `postmaster.pid`
 *  as running, and let the whole template's stop fail in silence, and passed every row before these;
 *  this file's provider rows follow that tester's probes.
 *
 *  The first row reads `postmasterGone` itself, against a pid that runs, one that ended, and a data
 *  directory with no `postmaster.pid`, and runs everywhere. The provider rows start the real
 *  binaries provider in a child of their own. Two kill a held server: a stand-in `psql` first on
 *  the child's PATH answers as a server starting up does while a hold file exists and runs the
 *  real psql otherwise; once the held server's postmaster has written its pid and a held `psql`
 *  has been refused, the child SIGKILLs that postmaster and prints what the start answered. The
 *  template's row holds from the start; the stack's sets the hold as the stack's data directory
 *  appears, so the template is made by the real psql first. The third hands the provider a
 *  binaries directory whose `pg_ctl` refuses to stop the template's server and runs the real one
 *  for everything else. They need `DISPATCH_E2E_PG_BIN`, the binaries that make the databases, and
 *  a psql on PATH, and are skipped without them, saying so. A start that never ends is ended by
 *  the child's `oneDatabase` bound, which names it. */

import { beforeAll, expect, test } from "bun:test";
import { chmodSync, existsSync, mkdirSync, readdirSync, symlinkSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";

import { postmasterGone } from "../harness-postgres";
import { rowLimit, runBounded } from "./bounded-subprocess";

const harnessPostgres = fileURLToPath(new URL("../harness-postgres.ts", import.meta.url));
const stubs = temporaryDirectory("harness-postgres-ends-");
const bin = (process.env.DISPATCH_E2E_PG_BIN ?? "").trim();
const realPsql = Bun.which("psql") ?? "";
const withoutBinaries = bin === "" || realPsql === "";
if (withoutBinaries) {
  console.error(
    "e2e/__tests__/harness-postgres-ends.test.ts: the provider rows are skipped, since " +
      (bin === "" ? "DISPATCH_E2E_PG_BIN names no Postgres binaries" : "no psql is on PATH") +
      " to start the template's and a stack's server with."
  );
}
const child = join(stubs, "end-during-start.ts");

beforeAll(() => {
  writeFileSync(
    child,
    `// Starts stack 0 of the binaries provider and prints what the start answered. For the
// \`template\` and \`stack\` targets it SIGKILLs the held server's postmaster once its pid is
// written and a held psql was refused; for \`stopfails\` it kills nothing.
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

const [module, target, dir, hold, calls] = process.argv.slice(2);
const { stackPostgres } = await import(module);
const provider = stackPostgres({ dir, run: "ends-" + target });
let settled = false;
const starting = provider.start(0).then(
  async (database) => {
    settled = true;
    await database.stop(false).catch(() => {});
    return { started: true };
  },
  (error) => {
    settled = true;
    return { message: error.message };
  }
);
// Each wait below ends on what it waits for or on the start settling; the bound the row puts on
// this child ends one that never does.
const until = async (fact) => {
  while (!fact()) await Bun.sleep(5);
};
const data = join(dir, target === "template" ? "template" : "worker-0", "pgdata");
let killed = null;
if (target !== "stopfails") {
  if (target === "stack") {
    await until(() => existsSync(data) || settled);
    writeFileSync(hold, "");
  }
  const pidFile = join(data, "postmaster.pid");
  await until(() => (existsSync(pidFile) && existsSync(calls)) || settled);
  if (!settled) {
    killed = Number(readFileSync(pidFile, "utf8").split("\\n")[0]);
    process.kill(killed, "SIGKILL");
  }
}
console.log(JSON.stringify({ killed, ...(await starting) }));
`
  );
});

test(
  "postmasterGone reads a postmaster that runs as running, and one that ended or wrote no pid as gone",
  async () => {
    const data = join(stubs, "gone-data");
    mkdirSync(data);
    // This process runs; the shell below has ended, and been reaped, by the time it is read.
    writeFileSync(join(data, "postmaster.pid"), `${process.pid}\n${data}\n`);
    expect(postmasterGone(data)(), "this running process read as gone").toBe(false);
    const ended = await runBounded({
      argv: ["/bin/sh", "-c", "echo $$"],
      bound: "short",
      from: import.meta.url,
    });
    const pid = Number(ended.stdout.trim());
    expect(Number.isInteger(pid) && pid > 0, `${ended.named} printed ${ended.stdout}`).toBe(true);
    writeFileSync(join(data, "postmaster.pid"), `${pid}\n${data}\n`);
    expect(postmasterGone(data)(), `pid ${pid}, which has ended, read as running`).toBe(true);
    // A data directory with no postmaster.pid: the server pg_ctl started has gone, and a wait
    // that read it as running would wait for ever.
    const empty = join(stubs, "no-pid-data");
    mkdirSync(empty);
    expect(postmasterGone(empty)(), "a data directory with no postmaster.pid read as running").toBe(
      true
    );
  },
  rowLimit("short")
);

for (const row of [
  { target: "template", what: "the stacks' Postgres template" },
  { target: "stack", what: "stack 0's Postgres" },
] as const) {
  test.skipIf(withoutBinaries)(
    `${row.what}, its postmaster killed while the start waits, is refused at once by name`,
    async () => {
      const dir = join(stubs, row.target);
      const shims = join(stubs, `${row.target}-shims`);
      const hold = join(stubs, `${row.target}-hold`);
      const calls = join(stubs, `${row.target}-held-calls`);
      mkdirSync(dir);
      mkdirSync(shims);
      writeFileSync(
        join(shims, "psql"),
        `#!/bin/sh
if [ -e "${hold}" ]; then
  echo held >> "${calls}"
  echo 'psql: error: connection to server at "127.0.0.1" failed: FATAL:  the database system is starting up' >&2
  exit 2
fi
exec "${realPsql}" "$@"
`
      );
      chmodSync(join(shims, "psql"), 0o755);
      if (row.target === "template") writeFileSync(hold, "");
      const { code, named, stderr, stdout } = await runBounded({
        argv: [process.execPath, child, harnessPostgres, row.target, dir, hold, calls],
        bound: "oneDatabase",
        env: {
          DISPATCH_E2E_PG_BIN: bin,
          HOME: process.env.HOME ?? "",
          PATH: `${shims}:${process.env.PATH ?? ""}`,
          TMPDIR: stubs,
        },
        from: import.meta.url,
      });
      expect(code, `${named} exited ${code}:\n${stderr}`).toBe(0);
      const read = JSON.parse(stdout.trim().split("\n").at(-1) ?? "{}") as {
        killed?: number | null;
        message?: string;
        started?: true;
      };
      expect(read.killed, `${named} printed ${stdout}:\n${stderr}`).toBeGreaterThan(0);
      expect(read.message, `${named} printed ${stdout}:\n${stderr}`).toStartWith(
        `The Dispatch e2e harness cannot start: ${row.what} stopped before it answered select 1: `
      );
    },
    rowLimit("oneDatabase")
  );
}

// The template is copied only once its server has stopped. A `pg_ctl` that refuses that stop -
// exit 1, the server left running - must refuse the start by name rather than let a stack start
// from a copy of a running server.
test.skipIf(withoutBinaries)(
  "the stacks' Postgres template whose server will not stop refuses the start by name",
  async () => {
    const dir = join(stubs, "stopfails");
    const binaries = join(stubs, "stopfails-bin");
    const refused = join(stubs, "stopfails-refused");
    mkdirSync(dir);
    mkdirSync(binaries);
    for (const tool of readdirSync(bin)) {
      if (tool !== "pg_ctl") symlinkSync(join(bin, tool), join(binaries, tool));
    }
    writeFileSync(
      join(binaries, "pg_ctl"),
      `#!/bin/sh
case " $* " in
  *" stop "*)
    case " $* " in
      *"/template/pgdata "*)
        echo refused >> "${refused}"
        echo "stand-in pg_ctl: this stop is refused" >&2
        exit 1
        ;;
    esac
    ;;
esac
exec "${join(bin, "pg_ctl")}" "$@"
`
    );
    chmodSync(join(binaries, "pg_ctl"), 0o755);
    const { code, named, stderr, stdout } = await runBounded({
      argv: [process.execPath, child, harnessPostgres, "stopfails", dir, "", ""],
      bound: "oneDatabase",
      env: {
        DISPATCH_E2E_PG_BIN: binaries,
        HOME: process.env.HOME ?? "",
        PATH: process.env.PATH ?? "",
        TMPDIR: stubs,
      },
      from: import.meta.url,
    });
    expect(code, `${named} exited ${code}:\n${stderr}`).toBe(0);
    expect(
      existsSync(refused),
      `the stand-in pg_ctl was never asked to stop the template:\n${stdout}`
    ).toBe(true);
    const read = JSON.parse(stdout.trim().split("\n").at(-1) ?? "{}") as {
      message?: string;
      started?: true;
    };
    expect(read.message, `${named} printed ${stdout}:\n${stderr}`).toStartWith(
      "The Dispatch e2e harness cannot start: the stacks' Postgres template's server would not " +
        "stop, and no stack starts from a copy of a running server: "
    );
  },
  rowLimit("oneDatabase")
);
