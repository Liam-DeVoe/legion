/**
 * The check that no request carrying a credential follows a redirect to another origin.
 *
 *   bun scripts/checks/redirect-refusals.ts [--rows <file>]
 *
 * Each listed site has one test row, in the test file beside the site. The row is one case for
 * each of 301, 302, 303, 307 and 308, named `<row> (<code>)`. Each case starts two servers on ports
 * the kernel chooses: the first answers its code with a `Location` on the second, which records
 * every request. It calls the site with a made-up credential, and expects a refusal that names the
 * status and the second server to record no request (`@legion/test-fixtures/redirect-servers`).
 * This check runs each row with `bun test`, reads which of its cases ran in the row's file from
 * its JUnit report, and prints one line per row. It exits 1 when a case fails or did not run; when
 * a row names no listed site, is not the test file the list gives its site, or names a site another
 * row names; and when a listed site has no row whose cases all ran and passed. So a run that found
 * no row cannot pass, and neither can one site's row run in place of another's. --rows names a
 * JSON file that holds the rows to run in place of the list's own, each `{site, directory, file}`:
 * the negative controls point it at an empty list, and at one site's row once for each listed
 * site.
 */
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import {
  REDIRECT_CODES,
  REDIRECT_ROW,
  redirectCase,
} from "../../packages/test-fixtures/src/redirect-servers";

/** Where a site's row is: the package directory `bun test` runs in, and the test file there. */
interface RowPlace {
  readonly directory: string;
  readonly file: string;
}

/** Each listed site, a client that sends a credential, by its file and symbol, with its row. */
const SITES: Readonly<Record<string, RowPlace>> = {
  "packages/claude-envoy/src/envoy-client.ts sendEnvoyMessage": {
    directory: "packages/claude-envoy",
    file: "tests/envoy-client.test.ts",
  },
  "packages/pi-shared/src/daemon-client.ts createLegionDaemonClient": {
    directory: "packages/pi-shared",
    file: "src/daemon-client.test.ts",
  },
  "packages/pi-legion/scripts/skill-scenarios/label-comments.ts label": {
    directory: "packages/pi-legion",
    file: "scripts/skill-scenarios/label-comments.test.ts",
  },
  "packages/pi-legion/scripts/skill-scenarios/production-dispatch.ts ProductionDispatch": {
    directory: "packages/pi-legion",
    file: "scripts/skill-scenarios/production-dispatch.test.ts",
  },
  "packages/envoy-client/src/slack-post.ts the poster": {
    directory: "packages/envoy-client",
    file: "src/__tests__/slack-post.test.ts",
  },
  "packages/pi-legion/scripts/skill-scenarios/seed.ts specBlockAsks, agentFetch": {
    directory: "packages/pi-legion",
    file: "scripts/skill-scenarios/seed.test.ts",
  },
  "packages/dispatch/e2e/api.ts request": {
    directory: "packages/dispatch",
    file: "e2e/__tests__/api.test.ts",
  },
};

/** One site's row, as the list gives it or `--rows` names it. */
interface Row extends RowPlace {
  readonly site: string;
}

const ROWS: readonly Row[] = Object.entries(SITES).map(([site, place]) => ({ site, ...place }));

const ROOT = path.resolve(import.meta.dir, "../..");

/** What one row's run left: how many of its cases ran, which failed, and why when it did not pass. */
interface Outcome {
  readonly ran: number;
  readonly failed: number;
  readonly why: string;
}

const XML_ENTITIES: Record<string, string> = {
  "&amp;": "&",
  "&apos;": "'",
  "&quot;": '"',
  "&lt;": "<",
  "&gt;": ">",
};

/** One test case of a JUnit report: its name, its file, and whether it was skipped or failed. */
interface TestCase {
  readonly name: string;
  readonly file: string;
  readonly skipped: boolean;
  readonly failed: boolean;
}

/** An attribute of a JUnit tag, its entities read. */
function attribute(tag: string, name: string): string {
  const value = new RegExp(`\\s${name}="([^"]*)"`).exec(tag)?.[1] ?? "";
  return value.replace(/&(amp|apos|quot|lt|gt);/g, (entity) => XML_ENTITIES[entity] ?? entity);
}

/** The test cases of a JUnit report, as `bun test --reporter=junit` writes one. */
function testCases(report: string): TestCase[] {
  const cases = report.matchAll(/(<testcase\s[^>]*?)(\/>|>([\s\S]*?)<\/testcase>)/g);
  return [...cases].map(([, tag = "", , body = ""]) => ({
    name: attribute(tag, "name"),
    file: attribute(tag, "file"),
    skipped: body.includes("<skipped"),
    failed: body.includes("<failure") || body.includes("<error"),
  }));
}

/** Runs one row's cases alone, its file named as a path, and reads their outcome from the JUnit
 * report `bun test` writes: a case counts only in the row's own file. */
function runRow(row: Row, scratch: string, index: number): Outcome {
  const report = path.join(scratch, `row-${index}.xml`);
  const name = REDIRECT_ROW.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const pattern = `${name} \\((${REDIRECT_CODES.join("|")})\\)$`;
  const run = Bun.spawnSync(
    [
      "bun",
      "test",
      `./${row.file}`,
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
    return { ran: 0, failed: 0, why: `bun test wrote no report (exit ${run.exitCode}): ${output}` };
  }
  const cases = testCases(written).filter(
    (one) => path.normalize(one.file) === path.normalize(row.file) && !one.skipped
  );
  const missing: number[] = [];
  const failing: number[] = [];
  for (const code of REDIRECT_CODES) {
    const named = cases.filter((one) => one.name === redirectCase(code));
    if (named.length === 0) missing.push(code);
    else if (named.some((one) => one.failed)) failing.push(code);
  }
  const ran = REDIRECT_CODES.length - missing.length;
  const why = [
    missing.length === 0 ? "" : `no case ran for ${missing.join(", ")}`,
    failing.length === 0 ? "" : `the case of ${failing.join(", ")} failed`,
    run.exitCode === 0 ? "" : `bun test exited ${run.exitCode}`,
  ].filter((part) => part !== "");
  return {
    ran,
    failed: failing.length,
    why: why.length === 0 ? "" : `${why.join("; ")}:\n${output}`,
  };
}

/** The rows `--rows <file>` names, or ROWS. Throws naming a bad argument or file. */
function rowsOf(argv: readonly string[]): readonly Row[] {
  if (argv.length === 0) return ROWS;
  const [flag, file] = argv;
  if (flag !== "--rows" || file === undefined || argv.length !== 2) {
    throw new Error("usage: redirect-refusals.ts [--rows <file>]");
  }
  const rows: unknown = JSON.parse(readFileSync(file, "utf8"));
  const isRow = (row: unknown): row is Row => {
    if (typeof row !== "object" || row === null) return false;
    const fields = new Map(Object.entries(row));
    return ["site", "directory", "file"].every((key) => typeof fields.get(key) === "string");
  };
  if (!Array.isArray(rows) || !rows.every(isRow)) {
    throw new Error(`${file} holds no JSON array of {site, directory, file} rows`);
  }
  return rows;
}

const rows = rowsOf(Bun.argv.slice(2));
const scratch = mkdtempSync(path.join(tmpdir(), "redirect-refusals-"));
const passed = new Set<string>();
const named = new Set<string>();
let cases = 0;
let failed = 0;
let refused = 0;
try {
  for (const [index, row] of rows.entries()) {
    const shown = `${row.site}: ${row.directory}/${row.file}`;
    const place = Object.hasOwn(SITES, row.site) ? SITES[row.site] : undefined;
    if (place === undefined) {
      refused += 1;
      console.log(`NAMES NO LISTED SITE ${shown}`);
      continue;
    }
    if (place.directory !== row.directory || place.file !== row.file) {
      refused += 1;
      console.log(`NOT THE ROW BESIDE ITS SITE ${shown}`);
      continue;
    }
    if (named.has(row.site)) {
      refused += 1;
      console.log(`A SECOND ROW OF ITS SITE ${shown}`);
      continue;
    }
    named.add(row.site);
    const outcome = runRow(row, scratch, index);
    cases += outcome.ran;
    failed += outcome.failed;
    const verdict = outcome.why === "" ? "PASS" : outcome.ran === 0 ? "DID NOT RUN" : "FAIL";
    if (verdict === "PASS") passed.add(row.site);
    console.log(`${verdict} ${shown}: ${outcome.ran} of ${REDIRECT_CODES.length} cases ran`);
    if (outcome.why !== "") console.log(outcome.why.replace(/^/gm, "    "));
  }
} finally {
  rmSync(scratch, { recursive: true, force: true });
}

const listed = Object.keys(SITES);
const uncovered = listed.filter((site) => !named.has(site));
for (const site of uncovered) console.log(`NO ROW ${site}`);
const summary = `${passed.size} of ${listed.length} listed sites have a row whose ${REDIRECT_CODES.length} cases ran and passed; ${cases} cases ran, ${failed} failed`;
if (passed.size < listed.length || refused > 0) {
  const why = [
    uncovered.length === 0 ? "" : `${uncovered.length} listed sites have no row`,
    refused === 0
      ? ""
      : `${refused} rows name no listed site, are not the row beside their site, or name a site another row names`,
  ].filter((part) => part !== "");
  console.log(`FAIL ${summary}${why.length === 0 ? "" : `: ${why.join("; ")}`}`);
  process.exit(1);
}
console.log(`PASS ${summary}`);
