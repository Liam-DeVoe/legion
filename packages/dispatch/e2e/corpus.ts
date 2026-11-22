/** The two Go commands `e2e/graph-model.e2e.ts` runs as children over the database of its own
 *  stack: the loader of the graph model's seeded corpus (`packages/envoy/cmd/dispatch-corpus`),
 *  and the conversion of a pre-mortem document into nodes (`envoy-dispatch premortem-convert`).
 *  Each takes its database from `DATABASE_URL`, which is set here to `databaseUrl()` of
 *  e2e/psql.ts, so the rule for which database a run writes to stays in one place and no test
 *  spells a database address. Of e2e/ it imports only e2e/psql.ts, as e2e/psql.ts imports only
 *  e2e/harness-ports.ts, so importing it evaluates neither e2e/api.ts nor a browser. */
import { execFile } from "node:child_process";
import { access, constants, mkdir } from "node:fs/promises";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { databaseUrl } from "./psql";

/** packages/envoy, the module both commands are built from. */
const envoyDirectory = fileURLToPath(new URL("../../envoy", import.meta.url));

/** The captured fixtures the loader replays, named to it rather than found from a working
 *  directory, so it reads this checkout's copies whatever directory Playwright runs from. */
const fixturesDirectory = fileURLToPath(new URL("./fixtures", import.meta.url));

/** What one child left: its exit status and everything it wrote to each stream. */
export interface CommandOutcome {
  readonly code: number;
  readonly stdout: string;
  readonly stderr: string;
}

/** The loader prints its own self-check after the names, and a seed of a worker's database prints
 *  well under a megabyte of both; the bound is only there so a runaway child cannot fill memory. */
const OUTPUT_BOUND_BYTES = 16 * 1024 * 1024;

/** Runs `file` with `args` in `cwd`, with `DATABASE_URL` naming this run's database where
 *  `database` says so, and answers what it left, whatever its status: a caller asserts on the
 *  status and prints both streams. */
function run(
  file: string,
  args: readonly string[],
  cwd: string,
  database = true
): Promise<CommandOutcome> {
  const env = database ? { ...process.env, DATABASE_URL: databaseUrl() } : process.env;
  const { promise, resolve: done } = Promise.withResolvers<CommandOutcome>();
  execFile(file, args, { cwd, env, maxBuffer: OUTPUT_BOUND_BYTES }, (error, stdout, stderr) => {
    const code = error === null ? 0 : typeof error.code === "number" ? error.code : 1;
    done({ code, stderr: String(stderr), stdout: String(stdout) });
  });
  return promise;
}

/** The loader's executable for this worker, found or built once. */
let corpusBinary: Promise<string> | undefined;

/**
 * The loader's executable: the one in the directory a caller hands its prebuilt binaries over in
 * (`DISPATCH_E2E_BROKER_BIN_DIR`, which CI's shard and alone jobs set to the directory their
 * binaries job built dispatch-corpus into beside the broker's three, since their containers have
 * no Go), and otherwise one built from this checkout into packages/envoy/out, which is ignored,
 * under a lock, so the workers of one run build it once, as e2e/run-server.sh builds the server
 * and e2e/run-broker.sh the broker. The build is skipped only where the caller hands the binary
 * over, never on a comparison of what is newer, so a run never seeds with a stale loader.
 */
function dispatchCorpus(): Promise<string> {
  corpusBinary ??= (async () => {
    const handed = process.env.DISPATCH_E2E_BROKER_BIN_DIR;
    if (handed) {
      const binary = resolve(handed, "dispatch-corpus");
      await access(binary, constants.X_OK).catch(() => {
        throw new Error(
          `DISPATCH_E2E_BROKER_BIN_DIR=${handed} holds no executable dispatch-corpus`
        );
      });
      return binary;
    }
    const directory = join(envoyDirectory, "out", "dispatch-e2e-corpus");
    const binary = join(directory, "dispatch-corpus");
    await mkdir(directory, { recursive: true });
    const built = await run(
      "flock",
      [join(directory, ".build.lock"), "go", "build", "-o", binary, "./cmd/dispatch-corpus"],
      envoyDirectory,
      false
    );
    if (built.code !== 0) {
      throw new Error(`go build ./cmd/dispatch-corpus exited ${built.code}\n${built.stderr}`);
    }
    return binary;
  })();
  return corpusBinary;
}

/** What a seed wrote, by the names `seedcorpus.Corpus` prints, one `key=value` line each. */
export type Corpus = ReadonlyMap<string, string>;

/**
 * Writes the seeded corpus into this run's database (`dispatch-corpus seed`) and answers the keys
 * and ids it printed. The stack's own server has written events of its own by the time a row
 * runs, so the database is not empty and the seed is told it is a test copy (`--test-copy`),
 * which `freshDatabase` (e2e/test.ts) has emptied of every row a scenario wrote. Where
 * `preventedByOut` names a file, under the row's own `testInfo.outputPath()`, the loader also
 * writes there the map a second conversion of the captured pre-mortem reads, which names checks of
 * the issue it seeded. A seed that is refused, or whose self-check fails, throws with both streams.
 */
export async function seedCorpus(preventedByOut?: string): Promise<Corpus> {
  const seeded = await run(
    await dispatchCorpus(),
    [
      "seed",
      "--test-copy",
      "--fixtures",
      fixturesDirectory,
      ...(preventedByOut === undefined ? [] : ["--prevented-by-out", preventedByOut]),
    ],
    envoyDirectory
  );
  if (seeded.code !== 0) {
    throw new Error(
      `dispatch-corpus seed exited ${seeded.code}\n--- stdout\n${seeded.stdout}\n--- stderr\n${seeded.stderr}`
    );
  }
  const names = new Map<string, string>();
  for (const line of seeded.stdout.split("\n")) {
    // The names come first, then `seeded in <duration>`, then the self-check's own lines, none of
    // which holds an `=` before its first space.
    if (line.startsWith("seeded in ")) break;
    const equals = line.indexOf("=");
    if (equals > 0 && !line.slice(0, equals).includes(" ")) {
      names.set(line.slice(0, equals), line.slice(equals + 1));
    }
  }
  if (names.size === 0) {
    throw new Error(`dispatch-corpus seed printed no name\n--- stdout\n${seeded.stdout}`);
  }
  return names;
}

/** The envoy-dispatch the harness's own server runs: the one `DISPATCH_E2E_SERVER_BIN` names,
 *  from the directory the run started in as e2e/run-server.sh reads it, else the one run-server.sh
 *  builds into packages/envoy under its lock before the server starts. */
function serverBinary(): string {
  const given = process.env.DISPATCH_E2E_SERVER_BIN;
  return given ? resolve(given) : resolve(envoyDirectory, "dispatch-e2e");
}

/** One run of `envoy-dispatch premortem-convert` over this run's database: the pre-mortem
 *  document `artifact` (its slug) of `issue`, with the map file `preventedBy`, written under the
 *  row's own `testInfo.outputPath()`, and `--dry-run` where `dryRun` says so. The caller reads the
 *  status and the report it prints. */
export function premortemConvert(input: {
  readonly issue: string;
  readonly artifact: string;
  readonly preventedBy: string;
  readonly dryRun: boolean;
}): Promise<CommandOutcome> {
  const args = [
    "premortem-convert",
    "--issue",
    input.issue,
    "--artifact",
    input.artifact,
    "--prevented-by",
    input.preventedBy,
    ...(input.dryRun ? ["--dry-run"] : []),
  ];
  return run(serverBinary(), args, envoyDirectory);
}
