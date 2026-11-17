/**
 * The check that no request carrying a credential follows a redirect to another origin.
 *
 *   bun scripts/checks/redirect-refusals.ts [--rows <file>]
 *
 * Each listed site has one test row, in the test file beside the site. The row starts two servers
 * on ports the kernel chooses: the first answers each of 301, 302, 303, 307 and 308 with a
 * `Location` on the second, which records every request. It calls the site with a made-up
 * credential, and expects a refusal that names the status and the second server to record no
 * request (`@legion/test-fixtures/redirect-servers`). This check runs every row with `bun test`,
 * reads which tests ran from its JUnit report, and prints one line per row. It exits 1 when a row
 * fails, and when fewer rows ran than there are listed sites, so a run that found no row cannot
 * pass. --rows names a JSON file that holds the rows to run in place of ROWS, in the same shape:
 * the negative control points it at an empty list, which fails for the count.
 */
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";

/** The sites the check covers, each a client that sends a credential: a file and its symbol. */
const SITES = [
  "packages/claude-envoy/src/envoy-client.ts sendEnvoyMessage",
  "packages/pi-shared/src/daemon-client.ts createLegionDaemonClient",
  "packages/pi-legion/scripts/skill-scenarios/label-comments.ts label",
  "packages/pi-legion/scripts/skill-scenarios/production-dispatch.ts ProductionDispatch",
  "packages/envoy-client/src/slack-post.ts the poster",
  "packages/pi-legion/scripts/skill-scenarios/seed.ts specBlockAsks, agentFetch",
] as const;

/** One site's test row: the package directory `bun test` runs in, the file, and the test's name. */
interface Row {
  readonly site: string;
  readonly directory: string;
  readonly file: string;
  readonly name: string;
}

/** Every row bears the same name, in the test file beside its site. */
const NAME =
  "a redirect to another origin is refused naming its status, and that origin receives no request";

const ROWS: readonly Row[] = [
  {
    site: SITES[0],
    directory: "packages/claude-envoy",
    file: "tests/envoy-client.test.ts",
    name: NAME,
  },
  {
    site: SITES[1],
    directory: "packages/pi-shared",
    file: "src/daemon-client.test.ts",
    name: NAME,
  },
  {
    site: SITES[2],
    directory: "packages/pi-legion",
    file: "scripts/skill-scenarios/label-comments.test.ts",
    name: NAME,
  },
  {
    site: SITES[3],
    directory: "packages/pi-legion",
    file: "scripts/skill-scenarios/production-dispatch.test.ts",
    name: NAME,
  },
  {
    site: SITES[4],
    directory: "packages/envoy-client",
    file: "src/__tests__/slack-post.test.ts",
    name: NAME,
  },
  {
    site: SITES[5],
    directory: "packages/pi-legion",
    file: "scripts/skill-scenarios/seed.test.ts",
    name: NAME,
  },
];

const ROOT = path.resolve(import.meta.dir, "../..");

/** What one row's run left: whether its test ran, and whether the run failed. */
interface Outcome {
  readonly ran: boolean;
  readonly failed: boolean;
  readonly why: string;
}

const XML_ENTITIES: Record<string, string> = {
  "&amp;": "&",
  "&apos;": "'",
  "&quot;": '"',
  "&lt;": "<",
  "&gt;": ">",
};

/** One test case of a JUnit report: its name, and whether it was skipped or failed. */
interface TestCase {
  readonly name: string;
  readonly skipped: boolean;
  readonly failed: boolean;
}

/** The test cases of a JUnit report, as `bun test --reporter=junit` writes one. */
function testCases(report: string): TestCase[] {
  const cases = report.matchAll(/<testcase name="([^"]*)"[^>]*?(\/>|>([\s\S]*?)<\/testcase>)/g);
  return [...cases].map(([, name = "", , body = ""]) => ({
    name: name.replace(/&(amp|apos|quot|lt|gt);/g, (entity) => XML_ENTITIES[entity] ?? entity),
    skipped: body.includes("<skipped"),
    failed: body.includes("<failure") || body.includes("<error"),
  }));
}

/** Runs one row's test alone and reads its outcome from the JUnit report `bun test` writes. */
function runRow(row: Row, scratch: string, index: number): Outcome {
  const report = path.join(scratch, `row-${index}.xml`);
  const pattern = `${row.name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}$`;
  const run = Bun.spawnSync(
    [
      "bun",
      "test",
      row.file,
      "--test-name-pattern",
      pattern,
      "--reporter=junit",
      `--reporter-outfile=${report}`,
    ],
    { cwd: path.join(ROOT, row.directory), stdout: "pipe", stderr: "pipe" }
  );
  const output = `${run.stdout.toString()}${run.stderr.toString()}`.trim();
  let written: string;
  try {
    written = readFileSync(report, "utf8");
  } catch {
    return {
      ran: false,
      failed: true,
      why: `bun test wrote no report (exit ${run.exitCode}): ${output}`,
    };
  }
  const cases = testCases(written).filter((one) => one.name === row.name && !one.skipped);
  if (cases.length === 0) {
    return { ran: false, failed: true, why: `no test of that name ran: ${output}` };
  }
  if (run.exitCode !== 0 || cases.some((one) => one.failed)) {
    return { ran: true, failed: true, why: `bun test exited ${run.exitCode}:\n${output}` };
  }
  return { ran: true, failed: false, why: "" };
}

/** The rows `--rows <file>` names, or ROWS. Throws naming a bad argument or file. */
function rowsOf(argv: readonly string[]): readonly Row[] {
  if (argv.length === 0) return ROWS;
  const [flag, file] = argv;
  if (flag !== "--rows" || file === undefined || argv.length !== 2) {
    throw new Error("usage: redirect-refusals.ts [--rows <file>]");
  }
  const rows: unknown = JSON.parse(readFileSync(file, "utf8"));
  if (!Array.isArray(rows)) throw new Error(`${file} holds no JSON array of rows`);
  return rows as Row[];
}

const rows = rowsOf(Bun.argv.slice(2));
const scratch = mkdtempSync(path.join(tmpdir(), "redirect-refusals-"));
let ran = 0;
let failed = 0;
try {
  for (const [index, row] of rows.entries()) {
    const outcome = runRow(row, scratch, index);
    if (outcome.ran) ran += 1;
    if (outcome.failed) failed += 1;
    const verdict = !outcome.ran ? "DID NOT RUN" : outcome.failed ? "FAIL" : "PASS";
    console.log(`${verdict} ${row.site}: ${row.directory}/${row.file}`);
    if (outcome.why !== "") console.log(outcome.why.replace(/^/gm, "    "));
  }
} finally {
  rmSync(scratch, { recursive: true, force: true });
}

const summary = `${ran} of ${rows.length} listed rows ran, for ${SITES.length} listed sites; ${failed} failed`;
if (failed > 0 || ran < SITES.length) {
  const short =
    ran < SITES.length
      ? `: fewer rows ran than there are listed sites${rows.length === 0 ? ", since the list of rows is empty" : ""}`
      : "";
  console.log(`FAIL ${summary}${short}`);
  process.exit(1);
}
console.log(`PASS ${summary}`);
