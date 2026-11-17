import { expect, test } from "bun:test";
import { LISTED_SITES, run, SITES } from "./redirect-refusals";

/** What the check of `sites`, counted as `count`, printed and answered, with no `--rows`. */
function checked(sites: typeof SITES, count: number): { exit: number; lines: string[] } {
  const lines: string[] = [];
  const exit = run([], sites, count, (line) => lines.push(line));
  return { exit, lines };
}

// A list that names no site checks nothing: such a run is not a pass, and it says what it could not
// read. No row runs.
test("a list of sites that names none is UNKNOWN, naming the list, and exits 1", () => {
  for (const count of [LISTED_SITES, 0]) {
    expect(checked({}, count)).toEqual({
      exit: 1,
      lines: [
        "UNKNOWN 0 listed sites: the list of sites of scripts/checks/redirect-refusals.ts (SITES) names none, so no row ran and nothing was checked",
      ],
    });
  }
});

// A site an edit drops from the list is checked by no row, so the list's length is held to the
// count beside it. No row runs.
test("a list of sites that lost one of its count fails before any row runs", () => {
  const [, ...six] = Object.entries(SITES);
  expect(checked(Object.fromEntries(six), LISTED_SITES)).toEqual({
    exit: 1,
    lines: [
      `FAIL the list of sites (SITES) names 6 sites, and the count beside it (LISTED_SITES) says ${LISTED_SITES}: a site the list lost or gained is checked only once the count moves with it`,
    ],
  });
});

test("the check's own list of sites is as long as the count beside it", () => {
  expect(Object.keys(SITES)).toHaveLength(LISTED_SITES);
});
