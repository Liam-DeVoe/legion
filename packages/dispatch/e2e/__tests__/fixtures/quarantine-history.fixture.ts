/** The planted history of `e2e/__tests__/quarantine-check.test.ts`: what the merge jobs of six
 *  runs said, read at `readAt`, the nightly's 02:17 on 2026-11-19. The rows and runs are planted;
 *  each message has the shape the merge job's "Reading two" step prints,
 *  `<file>:<line> [<project>] <title>`.
 *
 * - `pictures.e2e.ts [chromium] a picture uploads @pr` is flaky in two runs that count, a
 *   nightly and a merge group, at two lines, since the row moved between them.
 * - `keyed-writes.e2e.ts [webkit] … @webkit` is flaky in one run, named by two attempts of it.
 * - `deep-links.e2e.ts [firefox] … @firefox` is flaky in a merge group and in a pull request.
 * - `mermaid.e2e.ts [webkit] … @webkit` is flaky in a merge group and in a nightly of 18 days
 *   before, outside the window.
 */
import type { QuarantineEntry } from "../../quarantine";
import type { RunReading } from "../../tools/quarantine-check";

export const readAt = new Date("2026-11-19T02:17:00Z");

const run = (id: number, event: string, createdAt: string, flakyRows: string[]): RunReading => ({
  createdAt,
  event,
  flakyRows,
  id,
  unread: 0,
  url: `https://github.com/cybermonklabs/legion/actions/runs/${id}`,
});

const pictures = "pictures.e2e.ts:412 [chromium] a picture uploads @pr";
const keyedWrites = "keyed-writes.e2e.ts:233 [webkit] an ask answer whose answer is lost @webkit";
const deepLinks = "deep-links.e2e.ts:88 [firefox] a held chunk lands @firefox";
const mermaid = "mermaid.e2e.ts:40 [webkit] a diagram draws @webkit";

export const history: readonly RunReading[] = [
  run(101, "schedule", "2026-11-08T03:02:11Z", [pictures]),
  run(102, "merge_group", "2026-11-12T15:40:07Z", [
    "pictures.e2e.ts:415 [chromium] a picture uploads @pr",
    keyedWrites,
    keyedWrites,
  ]),
  run(103, "merge_group", "2026-11-14T09:12:55Z", [deepLinks, mermaid]),
  run(104, "pull_request", "2026-11-16T11:30:00Z", [deepLinks]),
  run(105, "schedule", "2026-11-01T02:31:40Z", [mermaid]),
  run(106, "merge_group", "2026-11-18T20:05:19Z", []),
];

/** The entry that holds the pictures row, added a week before `readAt`. */
export const picturesEntry: QuarantineEntry = {
  file: "pictures.e2e.ts",
  project: "chromium",
  title: "a picture uploads @pr",
  issue: "dispatch://INFRA-2001",
  added: "2026-11-12",
};

/** An entry of 14 days at `readAt`, which is the most the quarantine allows. */
export const fourteenDays: QuarantineEntry = {
  file: "heavy-streamed-turn.e2e.ts",
  project: "webkit",
  title: "a streamed body grows under its own paint @webkit",
  issue: "dispatch://INFRA-2002",
  added: "2026-11-05",
};

/** An entry of 18 days at `readAt`. */
export const eighteenDays: QuarantineEntry = {
  file: "offline-edits.e2e.ts",
  project: "firefox",
  title: "an edit is restored across a reload @firefox",
  issue: "dispatch://INFRA-2003",
  added: "2026-11-01",
};
