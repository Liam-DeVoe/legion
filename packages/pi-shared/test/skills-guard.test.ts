import { expect, test } from "bun:test";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { anchors, headingText, retiredRerunProhibitions } from "./skills-guard";

// The heading parse behind `anchors`, which decides whether a `skill://<name>/<path>#anchor` link
// resolves. It stands in for `/^#{1,6}\s+(.+?)\s*#*$/`, an expression quadratic in a line's
// trailing whitespace (dispatch://INFRA-1639), and each row below is that expression's own answer,
// oddities included: it is the meaning every skill's anchors were written against.
const headings: readonly (readonly [line: string, text: string | undefined])[] = [
  ["# One", "One"],
  ["###### six", "six"],
  ["##\ttab", "tab"],
  // Seven hashes is no heading, and neither is a hash with no whitespace after it.
  ["####### seven", undefined],
  ["#no space", undefined],
  ["   ## indented", undefined],
  // The text must be a character, so a line of hashes and whitespace alone is no heading until
  // there is a character for the group to take: `##   ` gives it a space, whose anchor is `-`.
  ["##", undefined],
  ["######", undefined],
  ["## ", undefined],
  ["##   ", " "],
  ["## ###", "#"],
  // Trailing hashes are dropped with the whitespace before them, but only where they end the
  // line: after `##  ` stands whitespace, so the hashes are part of the text.
  ["## a##", "a"],
  ["## a ##", "a"],
  ["## a  ##  ", "a  ##"],
  ["## a#b", "a#b"],
  // A file read with CRLF endings and split on "\n" leaves the carriage return on the line, where
  // it is whitespace to drop; a carriage return inside the text is no heading, since `.` matches
  // no line terminator.
  ["## a\r", "a"],
  ["## a\rb", undefined],
];

test.each(headings)("the heading text of %j is %j", (line, text) => {
  expect(headingText(line)).toBe(text);
});

test("an anchor is the heading lower-cased, its punctuation dropped and its spaces hyphens", () => {
  expect([...anchors(["# One", "## a  ##  ", "## 💡 Tip ##", "#no space"].join("\n"))]).toEqual([
    "one",
    "a--",
    "-tip",
  ]);
});

// A heading line long enough to matter: the old expression took two minutes over this one, and a
// parse that reads it a bounded number of times takes about a millisecond. The bound is Bun's own
// per-test timeout rather than a wall-clock assertion of our own, which a loaded runner would
// make flaky: a parse quadratic in the trailing whitespace cannot finish inside it.
test("a heading line of 200,000 trailing spaces is parsed, not scanned once per character", () => {
  const spaces = " ".repeat(200_000);
  expect(headingText(`## a${spaces}x`)).toBe(`a${spaces}x`);
  expect(headingText(`## a${spaces}###`)).toBe("a");
  expect(headingText(`##${spaces}`)).toBe(" ");
  // The control the measurement used: one hash, so `\s+` takes the whole run in one pass.
  expect(headingText(`#${spaces}x`)).toBe("x");
});

// The rules INFRA-1303 retired (`retiredRerunProhibitions`), each run over a page of its own. The
// rows that run them over the trees (pi-legion's and pi-envoy's skills-guard.test.ts) expect
// nothing found, which a pattern that matches nothing passes too, so these rows show each pattern
// finds the sentence it was written for and lets through each sentence the trees keep beside it.
// Every page is wrapped as the files wrap, with a break inside the words a pattern reads, and the
// retired wording starts below the page's first line, so a hit names the line that wording starts
// on rather than the first line of the file.

/** What `retiredRerunProhibitions` reports over a root that holds `page` alone, as `page.md`. */
function reported(page: readonly string[]): string[] {
  const root = mkdtempSync(path.join(os.tmpdir(), "retired-rerun-"));
  try {
    writeFileSync(path.join(root, "page.md"), `${page.join("\n")}\n`);
    return retiredRerunProhibitions([root]);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
}

// Each retired sentence in the words of the file that carried it, the text around it shortened,
// and the line its pattern's first word stands on. Every row is found by its own pattern alone,
// so a row whose pattern is taken out of the guard reports nothing and fails.
const retired: readonly (readonly [source: string, page: readonly string[], line: number])[] = [
  [
    'pr-body.md\'s "no CI lane is rerun as evidence"',
    [
      "- **The tester** reads CI on GitHub at the head it is judging for everything static: lint,",
      "  types and the unit and integration suites are CI's, read there and recorded for no later",
      "  step, except a `ci_check` job, and no CI lane is",
      "  rerun as evidence.",
    ],
    3,
  ],
  [
    'tester.md\'s "never re-run a CI lane as evidence"',
    [
      "Read CI for everything static, on GitHub at the head you tested and written down nowhere for a",
      "later step to trust, except a `ci_check` job, and never re-run a CI",
      "lane as evidence.",
    ],
    2,
  ],
  [
    'tester.md\'s "rerun none of them yourself"',
    [
      "Read CI for everything static, on GitHub at the head you tested and written down nowhere for a",
      "later step to trust, except a `ci_check` job; rerun none of",
      "them yourself.",
    ],
    2,
  ],
  [
    'review-threads.md\'s "you re-run nothing"',
    [
      "## The reviewer, on a re-review",
      "",
      "A review bot that is red while a thread it opened still stands is CI like any other, and you",
      "re-run nothing: that red moves the issue nowhere, but the base branch holds the merge for a",
      "check it requires.",
    ],
    3,
  ],
  [
    'envoy/SKILL.md\'s "A Legion implementer waits this way only"',
    [
      "## Waiting on a re-run",
      "",
      "- A Legion implementer waits this way",
      "  only on a re-run of a red check its task names. Otherwise it pushes and completes its phase,",
      "  and the daemon reads CI for it (`skill://legion-worker`).",
    ],
    3,
  ],
  [
    'merge-gate.md\'s "is work to fix, so the issue goes back"',
    [
      "  - *The watch.* On a merge-queue base it reads the pull request until GitHub merges it or takes",
      "    it out of the queue, and acts on a removal: a head the queue took out over its own `merge_group`",
      "    checks is work to fix, so the issue goes",
      "    back; a person's dequeue is that person's decision, reported to the architect. A removal is",
      "    never a merge.",
    ],
    2,
  ],
  [
    'merger.md\'s "That is work to fix. Send the issue back"',
    [
      "5. Act on a removal by its `removalReason`:",
      "   - `failed_checks`: the queue's own `merge_group` checks failed. That is work to",
      "     fix. Send the issue back with `request_backward_move` (step 6).",
    ],
    2,
  ],
  [
    'merger.md\'s "is work to fix, so send the issue back"',
    [
      "5. On a merge-queue base, watch the pull request until GitHub merges it or takes it out of the",
      "queue, and act on a removal: a queue that took the head out because its own `merge_group`",
      "checks failed is work to fix, so send the issue back; a person's dequeue is that person's",
      "decision, which you report.",
    ],
    2,
  ],
  [
    'tester.md\'s "A lane still running is read as running; never wait for it."',
    [
      "Read CI for everything static, on GitHub at the head you tested and written down nowhere for a",
      "later step to trust. A lane still",
      "running is read as running; never wait",
      "for it.",
    ],
    2,
  ],
  [
    'pr-body.md\'s "the tester never waits for it"',
    [
      "  read CI on GitHub at the head you are judging for everything static: lint, types and the unit",
      "  and integration suites are CI's, read there and recorded for no later step. A lane GitHub shows",
      "  `queued` or `in progress` is read as such: the tester",
      "  never waits for it, since a required red that settles sends the work back from `testing` and",
      "  the reviewer reads the head's checks before it approves.",
    ],
    2,
  ],
];

test.each(retired)("%s is reported at the line it starts on", (_source, page, line) => {
  expect(reported(page)).toEqual([`page.md:${line}`]);
});

// Each sentence the trees keep near the words of a retired one, which a pattern too wide would
// refuse, in the words of the file that carries it and the text around it shortened. The one
// sentence no tree carries is the send-back limited by `only`, which the merge-queue pattern's
// lookahead lets through.
const kept: readonly (readonly [source: string, page: readonly string[]])[] = [
  [
    'merge-gate.md\'s "A merge-group run is never re-run"',
    [
      "  failed and cancelled jobs, once, then wait — never the whole run, which runs again every job",
      "  that succeeded too. A merge-group run is never",
      "  re-run: the implementer finishes its round instead, and the pull request is tested again when",
      "  it is queued again.",
    ],
  ],
  [
    'merge-gate.md\'s "No role re-runs a group build."',
    [
      "  as that page's *What becomes of the record* says. No role",
      "  re-runs a group build. GitHub does re-run one, but the re-run puts nothing back in the queue.",
    ],
  ],
  [
    'red-at-the-head.md\'s "Where no run at the head holds the failing check, the role re-runs nothing"',
    [
      "- Red again: the round fails, or the reviewer requests changes, and the check is named.",
      "- Where no run at the head holds the failing check, the role re-runs",
      "  nothing and tells the architect.",
    ],
  ],
  [
    'red-at-the-head.md\'s "When the re-run is spent, the role re-runs nothing."',
    [
      "When the re-run is spent, the role",
      "re-runs nothing. It fails the round, or it requests changes,",
      "and it names the check.",
    ],
  ],
  [
    'red-at-the-head.md\'s "The merger, the architect in `accepting` and an implementer that runs retro re-run nothing."',
    [
      "- The merger, the architect in `accepting` and an implementer that runs retro re-run",
      "  nothing.",
    ],
  ],
  [
    'red-at-the-head.md\'s "A role that is told of a red whose phase it does not hold re-runs nothing."',
    ["- A role that is told of a red whose phase it does not hold", "  re-runs nothing."],
  ],
  [
    'red-at-the-head.md\'s "what the counted set carries, re-runs nothing"',
    [
      "there spends the head's one re-run on a run nobody counted. The wait ends three ways: the",
      "counted set reports that check, and the role acts on it; the counted set settles with every",
      "check it holds `completed` and that one absent, or the head moves, and the role then records",
      "what it was told and what the counted set carries, re-runs",
      "nothing, goes on with its round and tells the architect.",
    ],
  ],
  [
    'red-at-the-head.md\'s "it gets no re-run"',
    [
      "1. A check that judges the pull request itself rather than its code, such as the title check, is",
      "   the change's, and it gets no",
      "   re-run.",
    ],
  ],
  [
    'review-threads.md\'s "a re-run does not clear that red"',
    [
      "App's. A review bot that is red while a thread it opened still stands is CI like any other, and a",
      "re-run does not clear that red: that red moves the issue nowhere, but the base branch holds the",
      "merge for a check it requires.",
    ],
  ],
  [
    'tester.md\'s "do not run in your own pod what CI runs for you"',
    [
      "Read CI for everything static, on GitHub at the head you tested and written down nowhere for a",
      "later step to trust, except a `ci_check` job; do not run in your own",
      "pod what CI runs for you.",
    ],
  ],
  [
    "envoy/SKILL.md's \"The role that holds the issue's phase waits this way on the re-run\"",
    [
      "- The role that holds the issue's phase waits this way on the re-run of a red at the head,",
      "  which `skill://legion-worker/references/red-at-the-head.md` decides. Otherwise a Legion",
      "  worker pushes and completes its phase, and the daemon reads CI for it.",
    ],
  ],
  [
    'merge-gate.md\'s "is work to fix, so the issue goes back on the second such removal"',
    [
      "    it out of the queue, and acts on a removal: a head the queue took out over its own",
      "    `merge_group` checks is work to fix, so the issue goes back",
      "    on the second such removal of that head, the first being queued once more (the bullet",
      "    below); a person's dequeue is that person's decision, reported to the architect.",
    ],
  ],
  [
    'merger.md\'s "the issue goes back only on the second such removal of that head"',
    [
      "   - `failed_checks`: the queue's own `merge_group` checks failed. At a head that has not changed,",
      "     the removal gets that same head queued once more (step 4), and the issue goes back only on",
      "     the second such removal of that head, with `request_backward_move` (step 6).",
    ],
  ],
  [
    'a merge-queue send-back limited by "only on the second"',
    [
      "    it out of the queue, and acts on a removal: a head the queue took out over its own",
      "    `merge_group` checks is work to fix, so the issue goes back only",
      "    on the second such removal of that head.",
    ],
  ],
  [
    'tester.md\'s "you wait for none of them but that re-run"',
    [
      "The round goes on when the re-run is green. A lane GitHub shows `queued` or `in progress` is",
      "read as such, and you wait for none of them but that re-run.",
    ],
  ],
  [
    'pr-body.md\'s "the tester waits for none of them but the re-run"',
    [
      "  and integration suites are CI's, read there and recorded for no later step, except a",
      "  `ci_check` job. A lane GitHub shows `queued` or `in progress` is read as such, and the tester",
      "  waits for none of them but the re-run `skill://legion-worker/references/red-at-the-head.md`",
      "  gives it, since the reviewer reads the head's checks before it approves.",
    ],
  ],
  [
    'implementer.md\'s "never wait for CI"',
    [
      "Push, then complete: never wait for CI. Run your role's fast local checks before the push, then",
      "push and call `handoff_complete`. Never wait for, watch or poll the pull request's remote CI —",
      "no `gh run watch`, no `gh pr checks --watch`.",
    ],
  ],
  [
    'merge-gate.md\'s "still in progress: wait for it"',
    [
      "  cancelled results again and stays red. GitHub refuses a re-run while an attempt of that run is",
      "  still in progress: wait for it.",
    ],
  ],
];

test.each(kept)("%s is not reported", (_source, page) => {
  expect(reported(page)).toEqual([]);
});
