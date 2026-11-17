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
// and the line its pattern's first word stands on. The last three rows are the shapes no file
// carried, in the words a reword would bring them back in. Every row is found by its own pattern
// alone, so a row whose pattern is taken out of the guard reports nothing and fails.
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
  [
    'architect-common.md\'s "re-run it, wait for it, or have the implementer fix the code"',
    [
      "A `checks-red` notice is one thing only: the head of an issue in `testing` conflicts with its",
      "base. A red is yours to decide on — re-run",
      "it, wait for it, or have the implementer fix the code — and GitHub's to hold the merge for.",
    ],
    2,
  ],
  // worker-common.md's one sentence carries both grant shapes, so it is two rows, each shortened
  // to the half its own pattern reads.
  [
    'worker-common.md\'s "whether it is re-run, waited for or fixed"',
    [
      "When CI settles on the pull request's head, the daemon names every check GitHub reported. A red",
      "check is information, not a verdict: whether it is re-run,",
      "waited for or fixed, and whether the pull request merges, is GitHub's under its own rules.",
    ],
    2,
  ],
  [
    'worker-common.md\'s "is a decision for the roles it reached"',
    [
      "When CI settles on the pull request's head, the daemon names every check GitHub reported. What",
      "becomes of a red is a decision",
      "for the roles it reached, and whether the pull request merges is GitHub's.",
    ],
    2,
  ],
  [
    'troubleshooting.md\'s "Re-run a check that failed on itself, have the implementer fix code"',
    [
      "- **A red check.** Nothing moves for it. The daemon names every check GitHub reported, with the",
      "  conclusion GitHub gave it, and judges none of them. Re-run a check that failed",
      "  on itself, have the implementer fix code that is at fault, or leave it to GitHub, which holds",
      "  the merge under the base branch's own required checks.",
    ],
    2,
  ],
  [
    'review.go\'s message, "decide whether to re-run the checks, wait for them, or fix the code"',
    [
      "The daemon's message to the three roles told of a settlement read: decide whether to",
      "re-run the checks, wait for them, or fix the code.",
    ],
    2,
  ],
  [
    'merge-gate.md\'s "leaves the decision to the roles it told"',
    [
      "  CI red at the head in `accepting` moves nothing, as it moves nothing anywhere: the daemon names",
      "  the checks GitHub reported and leaves the decision",
      "  to the roles it told. Read the head's checks on GitHub yourself before you accept.",
    ],
    2,
  ],
  [
    'concepts.md\'s "A red is for those roles to decide on"',
    [
      "  them: it sends one message each to the tree's architect, the worker of the issue's current",
      "  phase and the implementer, and moves the issue nowhere. A red is for those",
      "  roles to decide on, and for GitHub to hold the merge over.",
    ],
    2,
  ],
  [
    'a reviewer\'s re-run of a check the base does not require, "is yours to re-run once before you approve"',
    [
      "The reviewer reads the head's checks on GitHub before it approves the head it reviews.",
      "A red on a check the base does not",
      "require is yours to re-run once before you",
      "approve.",
    ],
    2,
  ],
  [
    'main\'s rule turned on a required check, "You neither wait for a required check nor re-run it"',
    [
      "  `gh pr view <n> --json baseRefName,headRefOid`, two reads. The required set is the",
      "  base branch's rulesets' required checks and its branch protection's, when it has any.",
      "  You neither wait for a required",
      "  check nor re-run it for the approval, and the daemon reads no verdict from it either.",
    ],
    3,
  ],
  [
    'a ban of the reviewer\'s re-run of a required red, "You re-run nothing, a required check red at the head included."',
    [
      "The reviewer reads the head's checks on GitHub before it approves.",
      "You re-run",
      "nothing, a required check red at the head included.",
    ],
    2,
  ],
];

test.each(retired)("%s is reported at the line it starts on", (_source, page, line) => {
  expect(reported(page)).toEqual([`page.md:${line}`]);
});

// Each sentence the trees keep near the words of a retired one, which a pattern too wide would
// refuse, in the words of the file that carries it and the text around it shortened. The sentences
// no tree carries are the send-back limited by `only`, which the merge-queue pattern's lookahead
// lets through, and the tester's re-run of a check the base does not require, which holds no
// approval.
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
    'merge-gate.md\'s "never re-run a job for a `cancelled` row that a newer run of the same name has superseded"',
    [
      "  When the list holds more than one run of a required name, the newest run decides. An older run",
      "  of that name, whatever its conclusion, is no result: never re-run a",
      "  job for a `cancelled` row that a newer run of the same name has superseded.",
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
    "worker-common.md's \"only the role that holds the issue's phase re-runs one the change did not make\"",
    [
      "A red check is information, not a verdict: only the role that holds the issue's phase re-runs",
      "one the change did not make, once at that head and after it records the failure — the tester",
      "in `testing`, the reviewer in `reviewing` or the implementer while it holds `implementing`,",
      "never the merger — a red the change made is the implementer's to fix, and whether the pull",
      "request merges is GitHub's.",
    ],
  ],
  [
    'troubleshooting.md\'s "re-runs a check that failed on itself once at that head"',
    [
      "  issue's current phase and the implementer, and judges none of them. The tester in `testing`,",
      "  the reviewer in `reviewing` or the implementer while it holds `implementing` re-runs a check",
      "  that failed on itself once at that head, after it records the failure, and no other role",
      "  re-runs one; the implementer fixes code that is at fault; and GitHub holds the merge.",
    ],
  ],
  [
    "concepts.md's \"A red is for the role that holds the issue's phase to act on\"",
    [
      "  and the implementer whose code the checks ran on, and moves the issue nowhere. A red is for",
      "  the role that holds the issue's phase to act on — the tester in `testing`, the reviewer in",
      "  `reviewing` or the implementer while it holds `implementing` re-runs a check that failed on",
      "  itself once at that head, the implementer fixes code that is at fault, and a check still running",
      "  is waited for — and for GitHub to hold the merge over.",
    ],
  ],
  [
    'merge-gate.md\'s "the daemon names the checks GitHub reported and judges none of them"',
    [
      "  CI red at the head in `accepting` moves nothing, as it moves nothing anywhere: the daemon names",
      "  the checks GitHub reported and judges none of them. Read the head's checks",
    ],
  ],
  [
    "architect-common.md's \"where the head's one re-run is already spent it is the implementer's too\"",
    [
      "a red required check the change made is the implementer's, `request_backward_move` to",
      "`implementing`; one it did not make is the reviewer's to re-run once, so tell the reviewer on",
      "its topic, and where the head's one re-run is already spent it is the implementer's too",
      "(`skill://legion-worker/references/red-at-the-head.md` decides which);",
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
  [
    // Brought in from main (cybermonklabs/legion#4211) by the forward merge at 32d06c1c. The
    // architect's decision kept main's rule for a check the base does not require: the reviewer
    // neither waits for nor re-runs such a check for the approval, and keeps its one re-run for a
    // red on a check the base requires. So the sentence is the rule, and passes.
    'merge-gate.md\'s "You neither wait for it nor re-run it for the approval"',
    [
      "  `gh pr view <n> --json baseRefName,headRefOid`, two reads. The required set is the",
      "  base branch's rulesets' required checks and its branch protection's, when it has any.",
      "  A check the base does not require holds no approval. You neither wait for it nor",
      "  re-run it for the approval, and the daemon reads no verdict from it either.",
    ],
  ],
  [
    'red-at-the-head.md\'s "The reviewer neither waits for a check the base does not require nor re-runs it"',
    [
      "- The reviewer neither waits for a check the base does not",
      "  require nor re-runs it for the approval, as `skill://legion-worker/references/merge-gate.md` says.",
      "  It approves over such a red the change did not make and names the red in its `redAtTheHead`",
      "  record and its review body, and nowhere else: with no re-run, nothing shows the test fails at",
      "  random, so the red is no occurrence for the census. The tester and the implementer still",
      "  re-run such a red once.",
    ],
  ],
  [
    "a tester's re-run of a check the base does not require, which holds no approval",
    [
      "The reviewer approves over a red the change did not make on a check the base does not require.",
      "The tester re-runs a red on a check the base",
      "does not require once.",
    ],
  ],
];

test.each(kept)("%s is not reported", (_source, page) => {
  expect(reported(page)).toEqual([]);
});
