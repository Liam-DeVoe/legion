import { expect, test } from "bun:test";
import {
  type Arm,
  controlCase,
  descriptionFault,
  dropFigures,
  isControlLoss,
  isFalseLoss,
  isFlagged,
  isInformative,
  isLoss,
  type LoadTask,
  lossChance,
  poorName,
  RunRecord,
  rankSkills,
  type SkillOutcome,
  type Stage,
  scoreSkill,
  screeningDone,
  screeningRefusal,
  servedScreeningRefusal,
  skillVerdict,
  smallestDrop,
  stageArms,
  stageOutcome,
  taskFault,
  walk,
} from "./score";

// The rules of the behaviour test (score.ts's header), the load test's fault rule and the name
// test's poor-name rule, one row on each side of each boundary. Every row is built here: this
// file reads no fixture. A
// row is a name, the call it checks and the value that call must give.

/** `total` run records of one case, stage and arm: `passes` passes, the rest fails. */
function runs(caseId: string, stage: Stage, arm: Arm, passes: number, total: number): RunRecord[] {
  return Array.from({ length: total }, (_, n) => ({
    caseId,
    stage,
    arm,
    verdict: n < passes ? "pass" : "fail",
    reason: "judged",
  }));
}
/** `count` run records that got no judged verdict: an error, a timeout or an unparsed judgement. */
function unjudged(caseId: string, stage: Stage, arm: Arm, count: number): RunRecord[] {
  return Array.from({ length: count }, () => ({
    caseId,
    stage,
    arm,
    verdict: null,
    reason: "timed out",
  }));
}
/** A case's 10 screen runs in each arm, with these passes. */
const screen = (caseId: string, old: number, cut: number, none: number): RunRecord[] => [
  ...runs(caseId, "screen", "old", old, 10),
  ...runs(caseId, "screen", "new", cut, 10),
  ...runs(caseId, "screen", "none", none, 10),
];
/** A case's 40 fresh runs of `old` and of `other` (`new` to confirm, `minus` for the control), and
 *  for the control the false-loss control's `twin` too, passing `twin` (old's passes unless
 *  given). */
const fresh = (
  caseId: string,
  stage: "confirm" | "control",
  old: number,
  other: number,
  twin = old
): RunRecord[] => [
  ...runs(caseId, stage, "old", old, 40),
  ...runs(caseId, stage, stage === "confirm" ? "new" : "minus", other, 40),
  ...(stage === "control" ? runs(caseId, stage, "twin", twin, 40) : []),
];
/** Six informative cases (old 8, none 5 of 10), none flagged (new 8), and the control's runs on
 * c1, the lowest id of the six equal gaps: old 30, minus `minus` and twin `twin` (30) of 40. */
const sixCases = (minus: number, twin = 30): RunRecord[] => [
  ...["c1", "c2", "c3", "c4", "c5", "c6"].flatMap((id) => screen(id, 8, 8, 5)),
  ...fresh("c1", "control", 30, minus, twin),
];
/** A load test that passes: 5 need tasks, each loaded on 2 or 3 of 3 runs, and 5 near-miss tasks,
 * each loaded on at most 1; and the same test with its first need task loaded once. */
const passing: LoadTask[] = [
  ...[3, 2, 2, 3, 2].map((loads) => ({ label: "need" as const, loads })),
  ...[0, 1, 0, 1, 0].map((loads) => ({ label: "near-miss" as const, loads })),
];
const failing: LoadTask[] = [{ label: "need", loads: 1 }, ...passing.slice(1)];

const keeps: SkillOutcome = { verdict: "keeps" };
const loss: SkillOutcome = { verdict: "loss" };
const tooFew: SkillOutcome = { verdict: null, reason: "fewer than 6 informative cases: 5" };
const ranking = ["alpha", "beta", "gamma", "delta", "epsilon"];

const rows: [string, () => unknown, unknown][] = [
  // The run record: one JSON line per run.
  [
    "a run record with a null verdict parses",
    () =>
      RunRecord.safeParse({
        caseId: "c1",
        arm: "minus",
        stage: "control",
        verdict: null,
        reason: "judgement did not parse",
      }).success,
    true,
  ],
  [
    "a run record whose verdict is neither pass, fail nor null is refused",
    () =>
      RunRecord.safeParse({
        caseId: "c1",
        arm: "old",
        stage: "screen",
        verdict: "error",
        reason: "x",
      }).success,
    false,
  ],
  [
    "a run record with a key the shape does not name is refused",
    () =>
      RunRecord.safeParse({
        caseId: "c1",
        arm: "old",
        stage: "screen",
        verdict: "pass",
        reason: "x",
        skill: "alpha",
      }).success,
    false,
  ],

  // Judged runs: an error, a timeout or an unparsed judgement is no judged run.
  [
    "10 judged screen runs in each arm are counted",
    () => stageOutcome(screen("c1", 7, 6, 5), "c1", "screen"),
    { state: "counted", passes: { old: 7, new: 6, none: 5 } },
  ],
  [
    "a run with no judged verdict is no fail: 9 judged of 10 runs leave one run to start",
    () =>
      stageOutcome(
        [...screen("c1", 7, 6, 5).slice(1), ...unjudged("c1", "screen", "old", 1)],
        "c1",
        "screen"
      ),
    { state: "running", toStart: { old: 1, new: 0, none: 0 } },
  ],
  [
    "10 judged runs among 15 (5 extra runs) are counted",
    () =>
      stageOutcome(
        [...screen("c1", 7, 6, 5), ...unjudged("c1", "screen", "new", 5)],
        "c1",
        "screen"
      ),
    { state: "counted", passes: { old: 7, new: 6, none: 5 } },
  ],
  [
    "9 judged runs among 14 leave one extra run to start",
    () =>
      stageOutcome(
        [...screen("c1", 7, 6, 5).slice(1), ...unjudged("c1", "screen", "old", 5)],
        "c1",
        "screen"
      ),
    { state: "running", toStart: { old: 1, new: 0, none: 0 } },
  ],
  [
    "9 judged runs among 15 leave the case unscreened",
    () =>
      stageOutcome(
        [...screen("c1", 7, 6, 5).slice(1), ...unjudged("c1", "screen", "old", 6)],
        "c1",
        "screen"
      ),
    { state: "unscreened" },
  ],
  [
    "the confirm stage needs 40 judged runs in each arm",
    () => stageOutcome(fresh("c1", "confirm", 30, 20).slice(1), "c1", "confirm"),
    { state: "running", toStart: { old: 1, new: 0 } },
  ],
  [
    "40 judged control runs of old, minus and twin are counted",
    () => stageOutcome(fresh("c1", "control", 30, 20, 28), "c1", "control"),
    { state: "counted", passes: { old: 30, minus: 20, twin: 28 } },
  ],
  [
    "the control stage needs twin's 40 judged runs too",
    () =>
      stageOutcome(
        fresh("c1", "control", 30, 20).filter((run) => run.arm !== "twin"),
        "c1",
        "control"
      ),
    { state: "running", toStart: { old: 0, minus: 0, twin: 40 } },
  ],

  // informative: old passes - none passes >= 2, of 10 each.
  ["informative at 2 more passes than no skill", () => isInformative({ old: 7, none: 5 }), true],
  ["not informative at 1 more pass than no skill", () => isInformative({ old: 6, none: 5 }), false],

  // flagged: informative, and old - new >= 2, of 10 each.
  ["flagged at 2 fewer passes of 10", () => isFlagged({ old: 7, new: 5, none: 5 }), true],
  ["not flagged at 1 fewer pass of 10", () => isFlagged({ old: 7, new: 6, none: 5 }), false],
  ["not flagged when not informative", () => isFlagged({ old: 7, new: 5, none: 6 }), false],

  // loss: old - new >= 10, of 40 fresh runs each.
  ["a loss at 10 fewer passes of 40", () => isLoss({ old: 30, new: 20 }), true],
  ["no loss at 9 fewer passes of 40", () => isLoss({ old: 30, new: 21 }), false],

  // control loss: old - minus >= 10, of 40 fresh runs each.
  [
    "a control loss at 10 fewer passes of 40",
    () => isControlLoss({ old: 30, minus: 20, twin: 30 }),
    true,
  ],
  [
    "no control loss at 9 fewer passes of 40",
    () => isControlLoss({ old: 30, minus: 21, twin: 30 }),
    false,
  ],
  // false loss: old - twin >= 10, of 40 fresh runs each, the same text twice.
  [
    "a false loss at 10 fewer passes of 40 of the twin",
    () => isFalseLoss({ old: 30, minus: 20, twin: 20 }),
    true,
  ],
  [
    "no false loss at 9 fewer passes of 40 of the twin",
    () => isFalseLoss({ old: 30, minus: 20, twin: 21 }),
    false,
  ],
  [
    "no false loss when the twin passes more than old",
    () => isFalseLoss({ old: 20, minus: 10, twin: 35 }),
    false,
  ],

  // control case: the informative case with the largest old - none gap; a tie to the lower id.
  [
    "the control case is the informative case with the largest gap",
    () =>
      controlCase([
        { caseId: "a", passes: { old: 8, new: 8, none: 5 } },
        { caseId: "b", passes: { old: 9, new: 9, none: 5 } },
        { caseId: "c", passes: { old: 6, new: 6, none: 5 } },
      ]),
    "b",
  ],
  [
    "a tie for the largest gap goes to the lower case id",
    () =>
      controlCase([
        { caseId: "b", passes: { old: 8, new: 8, none: 5 } },
        { caseId: "a", passes: { old: 9, new: 9, none: 6 } },
      ]),
    "a",
  ],
  [
    "the lower case id is the lower in string order",
    () =>
      controlCase([
        { caseId: "c9", passes: { old: 8, new: 8, none: 5 } },
        { caseId: "c10", passes: { old: 8, new: 8, none: 5 } },
      ]),
    "c10",
  ],
  [
    "no informative case gives no control case",
    () => controlCase([{ caseId: "a", passes: { old: 6, new: 6, none: 5 } }]),
    undefined,
  ],

  // skill verdict.
  [
    "6 informative cases and a control loss give a verdict",
    () => skillVerdict({ informative: 6, controlLoss: true, falseLoss: false, losses: 0 }),
    keeps,
  ],
  [
    "5 informative cases give no verdict",
    () => skillVerdict({ informative: 5, controlLoss: true, falseLoss: false, losses: 0 }),
    tooFew,
  ],
  [
    "a control that is no loss gives no verdict",
    () => skillVerdict({ informative: 6, controlLoss: false, falseLoss: false, losses: 1 }),
    { verdict: null, reason: "control not a loss" },
  ],
  [
    "a false-loss control that is a loss gives no verdict, whatever the confirmed cases say",
    () => skillVerdict({ informative: 6, controlLoss: true, falseLoss: true, losses: 1 }),
    { verdict: null, reason: "false-loss control a loss" },
  ],
  [
    "both controls failing give no verdict, and the reason names both",
    () => skillVerdict({ informative: 6, controlLoss: false, falseLoss: true, losses: 0 }),
    { verdict: null, reason: "control not a loss; false-loss control a loss" },
  ],
  [
    "one confirmed loss gives loss",
    () => skillVerdict({ informative: 6, controlLoss: true, falseLoss: false, losses: 1 }),
    loss,
  ],
  [
    "no confirmed loss gives keeps",
    () => skillVerdict({ informative: 7, controlLoss: true, falseLoss: false, losses: 0 }),
    keeps,
  ],

  // The skill's score from its run records: an unscreened case is not counted.
  [
    "six informative cases with a control loss on the lowest id keep",
    () => {
      const score = scoreSkill(sixCases(20));
      return [score.informative, score.controlCase, score.outcome];
    },
    [6, "c1", keeps],
  ],
  [
    "a planted-loss control at 9 fewer passes of 40 gives no verdict",
    () => scoreSkill(sixCases(21)).outcome,
    { verdict: null, reason: "control not a loss" },
  ],
  [
    "a false-loss control at 10 fewer passes of 40 gives no verdict",
    () => scoreSkill(sixCases(20, 20)).outcome,
    { verdict: null, reason: "false-loss control a loss" },
  ],
  [
    "a false-loss control at 9 fewer passes of 40 keeps the verdict",
    () => scoreSkill(sixCases(20, 21)).outcome,
    keeps,
  ],
  [
    "a control stage whose twin lacks its runs waits for them",
    () => scoreSkill(sixCases(20).filter((run) => run.arm !== "twin")).outcome,
    undefined,
  ],
  [
    "a flagged case's confirm runs are not waited for when the false-loss control is a loss",
    () => scoreSkill([...sixCases(20, 20), ...screen("c7", 8, 6, 5)]).outcome,
    { verdict: null, reason: "false-loss control a loss" },
  ],
  [
    "an unscreened case is not counted among the informative cases",
    () => {
      const records = sixCases(20).filter(
        (run) => !(run.caseId === "c6" && run.arm === "old" && run.verdict === "pass")
      );
      records.push(...runs("c6", "screen", "old", 7, 7), ...unjudged("c6", "screen", "old", 6));
      const score = scoreSkill(records);
      return [score.screened, score.informative, score.outcome];
    },
    [5, 5, tooFew],
  ],
  [
    "a flagged case waits for its confirm runs",
    () => scoreSkill([...sixCases(20), ...screen("c7", 8, 6, 5)]).outcome,
    undefined,
  ],
  [
    "a flagged case that is a loss on its confirm runs gives loss",
    () =>
      scoreSkill([...sixCases(20), ...screen("c7", 8, 6, 5), ...fresh("c7", "confirm", 30, 20)])
        .outcome,
    loss,
  ],
  [
    "a flagged case whose confirm runs stay unscreened gives no verdict, never keeps",
    () =>
      scoreSkill([
        ...sixCases(20),
        ...screen("c7", 8, 6, 5),
        ...runs("c7", "confirm", "old", 30, 40),
        ...runs("c7", "confirm", "new", 20, 34),
        ...unjudged("c7", "confirm", "new", 11),
      ]).outcome,
    { verdict: null, reason: "confirm runs missing: c7" },
  ],

  // skills tested: the walk of the ranking.
  [
    "the ranking is by sessions, ties by name",
    () =>
      rankSkills([
        { skill: "beta", sessions: 5 },
        { skill: "alpha", sessions: 5 },
        { skill: "gamma", sessions: 9 },
      ]),
    ["gamma", "alpha", "beta"],
  ],
  [
    "the first three skills get tested",
    () => walk(ranking, new Map()),
    {
      tested: [
        { skill: "alpha", outcome: undefined },
        { skill: "beta", outcome: undefined },
        { skill: "gamma", outcome: undefined },
      ],
      next: ["alpha", "beta", "gamma"],
      done: false,
    },
  ],
  [
    "a no-verdict skill is replaced by the next",
    () =>
      walk(
        ranking,
        new Map<string, SkillOutcome>([
          ["alpha", keeps],
          ["beta", tooFew],
        ])
      ).next,
    ["gamma", "delta"],
  ],
  [
    "the walk stops at three verdicts",
    () =>
      walk(
        ranking,
        new Map<string, SkillOutcome>([
          ["alpha", keeps],
          ["beta", tooFew],
          ["gamma", loss],
          ["delta", keeps],
          ["epsilon", keeps],
        ])
      ),
    {
      tested: [
        { skill: "alpha", outcome: keeps },
        { skill: "beta", outcome: tooFew },
        { skill: "gamma", outcome: loss },
        { skill: "delta", outcome: keeps },
      ],
      next: [],
      done: true,
    },
  ],
  [
    "the walk stops at the end of the list, each tested skill with a verdict or its reason",
    () =>
      walk(
        ["alpha", "beta", "gamma", "delta"],
        new Map<string, SkillOutcome>([
          ["alpha", tooFew],
          ["beta", keeps],
          ["gamma", tooFew],
          ["delta", loss],
        ])
      ),
    {
      tested: [
        { skill: "alpha", outcome: tooFew },
        { skill: "beta", outcome: keeps },
        { skill: "gamma", outcome: tooFew },
        { skill: "delta", outcome: loss },
      ],
      next: [],
      done: true,
    },
  ],

  // case cap.
  [
    "a skill 5 sessions read gets no screening",
    () => screeningRefusal(5),
    { verdict: null, reason: "fewer than 6 sessions read it" },
  ],
  ["a skill 6 sessions read is screened", () => screeningRefusal(6), undefined],
  [
    "a skill 5 of whose 8 reading sessions are on served models gets no screening",
    () => servedScreeningRefusal({ served: 5, sessions: 8 }),
    {
      verdict: null,
      reason:
        "too few sessions on served models: 5 of its 8 reading sessions are on a model the gateway serves, fewer than 6",
    },
  ],
  [
    "a skill 6 of whose 8 reading sessions are on served models is screened",
    () => servedScreeningRefusal({ served: 6, sessions: 8 }),
    undefined,
  ],
  [
    "screening stops at 6 informative cases",
    () => screeningDone({ screened: 6, informative: 6 }),
    true,
  ],
  [
    "screening goes on at 5 informative of 14 screened cases",
    () => screeningDone({ screened: 14, informative: 5 }),
    false,
  ],
  [
    "screening stops at 15 screened cases",
    () => screeningDone({ screened: 15, informative: 5 }),
    true,
  ],

  // load fault, per task of 3 runs.
  [
    "a need task loaded on 1 of 3 runs is a fault",
    () => taskFault({ label: "need", loads: 1 }),
    true,
  ],
  [
    "a need task loaded on 2 of 3 runs is no fault",
    () => taskFault({ label: "need", loads: 2 }),
    false,
  ],
  [
    "a near-miss task loaded on 2 of 3 runs is a fault",
    () => taskFault({ label: "near-miss", loads: 2 }),
    true,
  ],
  [
    "a near-miss task loaded on 1 of 3 runs is no fault",
    () => taskFault({ label: "near-miss", loads: 1 }),
    false,
  ],
  ["a description with one faulty task is a fault", () => descriptionFault(failing), true],
  ["a description with no faulty task is no fault", () => descriptionFault(passing), false],

  // poor name: the current name fails the load test and a proposed name passes.
  ["current fails, proposed passes: a poor name", () => poorName(failing, passing), true],
  ["current fails, proposed fails: no poor name", () => poorName(failing, failing), false],
  ["current passes, proposed passes: no poor name", () => poorName(passing, passing), false],
  ["current passes, proposed fails: no poor name", () => poorName(passing, failing), false],
];

test.each(rows)("%s", (_name, got, want) => {
  expect(got()).toEqual(want);
});

// The arm the old text is compared with (`new`, `ours` or `none`; the rows above compare with
// `new`), the chance that the rule calls a loss, and each case's smallest drop.

/** A case's 10 screen runs of old and none alone: the screen with no skill compared. */
const screenNone = (caseId: string, old: number, none: number): RunRecord[] => [
  ...runs(caseId, "screen", "old", old, 10),
  ...runs(caseId, "screen", "none", none, 10),
];
/** A case's 10 screen runs of old, ours and none: the screen with a skill of ours compared. */
const screenOurs = (caseId: string, old: number, ours: number, none: number): RunRecord[] => [
  ...runs(caseId, "screen", "old", old, 10),
  ...runs(caseId, "screen", "ours", ours, 10),
  ...runs(caseId, "screen", "none", none, 10),
];
/** Six cases informative with no skill compared (old 8, none 5 of 10), so each is flagged too,
 *  each confirmed on 40 fresh runs of old (30) and none (`confirmNone`), and the control's runs on
 *  c1: old 30, minus `minus` and twin 30 of 40. */
const sixNone = (minus: number, confirmNone: number): RunRecord[] => [
  ...["c1", "c2", "c3", "c4", "c5", "c6"].flatMap((id) => [
    ...screenNone(id, 8, 5),
    ...runs(id, "confirm", "old", 30, 40),
    ...runs(id, "confirm", "none", confirmNone, 40),
  ]),
  ...fresh("c1", "control", 30, minus),
];

/** The chance of each count, 0 to `n`, of `n` runs that each pass with chance `p`. */
function pmf(n: number, p: number): number[] {
  const choose = (k: number) => {
    let ways = 1;
    for (let i = 1; i <= k; i += 1) ways = (ways * (n - k + i)) / i;
    return ways;
  };
  return Array.from({ length: n + 1 }, (_, k) => choose(k) * p ** k * (1 - p) ** (n - k));
}
/** The chance that a count of `n` runs at `a` is at least `gap` more than one of `n` runs at `b`,
 *  every pair of counts summed. */
function ahead(n: number, a: number, b: number, gap: number): number {
  const [x, y] = [pmf(n, a), pmf(n, b)];
  let sum = 0;
  for (let i = 0; i <= n; i += 1) {
    for (let j = 0; j <= i - gap; j += 1) sum += (x[i] ?? 0) * (y[j] ?? 0);
  }
  return sum;
}
/** Whether `exact` is `counted` but for rounding: a relative difference below 1e-9. */
const agrees = (exact: number, counted: number) =>
  counted > 0 && Math.abs(exact - counted) / counted < 1e-9;
const percent = (x: number) => `${(100 * x).toFixed(2)}%`;

const comparedRows: [string, () => unknown, unknown][] = [
  // With no skill compared (neither --new nor --ours): old against none.
  [
    "with no skill compared, the screen runs old and none, and confirm old and none",
    () => [
      stageArms("screen", "none"),
      stageArms("confirm", "none"),
      stageArms("control", "none"),
      stageOutcome(screenNone("c1", 7, 5), "c1", "screen", "none"),
    ],
    [
      ["old", "none"],
      ["old", "none"],
      ["old", "minus", "twin"],
      { state: "counted", passes: { old: 7, none: 5 } },
    ],
  ],
  [
    "with no skill compared, a case is informative and flagged at 2 more passes of 10 than none",
    () => [isInformative({ old: 7, none: 5 }), isFlagged({ old: 7, none: 5 }, "none")],
    [true, true],
  ],
  [
    "with no skill compared, a case is neither informative nor flagged at 1 more pass of 10 than none",
    () => [isInformative({ old: 6, none: 5 }), isFlagged({ old: 6, none: 5 }, "none")],
    [false, false],
  ],
  [
    "with no skill compared, a loss at 10 fewer passes of 40 than old",
    () => isLoss({ old: 30, none: 20 }, "none"),
    true,
  ],
  [
    "with no skill compared, no loss at 9 fewer passes of 40 than old",
    () => isLoss({ old: 30, none: 21 }, "none"),
    false,
  ],
  [
    "with no skill compared, six informative cases and a planted-loss control that is a loss give a verdict",
    () => {
      const kept = scoreSkill(sixNone(20, 25), "none");
      const lost = scoreSkill(sixNone(20, 20), "none");
      return [
        kept.informative,
        kept.cases.filter((each) => each.flagged).length,
        kept.outcome,
        lost.cases.filter((each) => each.loss).length,
        lost.outcome,
      ];
    },
    [6, 6, keeps, 6, loss],
  ],
  [
    "with no skill compared, five informative cases give no verdict",
    () =>
      scoreSkill(
        sixNone(20, 25).filter((run) => run.caseId !== "c6"),
        "none"
      ).outcome,
    tooFew,
  ],
  [
    "with ours, flagged and loss compare old with ours",
    () => [
      stageArms("screen", "ours"),
      stageArms("confirm", "ours"),
      isFlagged({ old: 7, ours: 5, none: 5 }, "ours"),
      isFlagged({ old: 7, new: 0, ours: 6, none: 5 }, "ours"),
      isLoss({ old: 30, ours: 20 }, "ours"),
      isLoss({ old: 30, new: 0, ours: 21 }, "ours"),
      stageOutcome(screenOurs("c1", 8, 6, 5), "c1", "screen", "ours"),
      scoreSkill(
        [
          ...screenOurs("c1", 8, 6, 5),
          ...runs("c1", "confirm", "old", 30, 40),
          ...runs("c1", "confirm", "ours", 20, 40),
        ],
        "ours"
      ).cases.map(({ caseId, informative, flagged, loss: lost }) => ({
        caseId,
        informative,
        flagged,
        loss: lost,
      })),
    ],
    [
      ["old", "ours", "none"],
      ["old", "ours"],
      true,
      false,
      true,
      false,
      { state: "counted", passes: { old: 8, ours: 6, none: 5 } },
      [{ caseId: "c1", informative: true, flagged: true, loss: true }],
    ],
  ],

  // The chance that the rule calls a loss: with no skill compared, the screen's gap is one
  // condition (informative and flagged are the same), counted once.
  [
    "lossChance with the compared arm none counts the screen's gap once",
    () => {
      const got = lossChance({ old: 0.9, cut: 0.5, none: 0.5 }, "none");
      const once = ahead(10, 0.9, 0.5, 2) * ahead(40, 0.9, 0.5, 10);
      // The gap counted twice: a flag of o − n and an informative o − z, n and z apart.
      const [o, n, z] = [pmf(10, 0.9), pmf(10, 0.5), pmf(10, 0.5)];
      let both = 0;
      for (let a = 0; a <= 10; a += 1) {
        for (let b = 0; b <= a - 2; b += 1) {
          for (let c = 0; c <= a - 2; c += 1) both += (o[a] ?? 0) * (n[b] ?? 0) * (z[c] ?? 0);
        }
      }
      const twice = both * ahead(40, 0.9, 0.5, 10);
      let refused = "";
      try {
        lossChance({ old: 0.9, cut: 0.5, none: 0.1 }, "none");
      } catch (error) {
        refused = String(error);
      }
      return [agrees(got, once), agrees(got, twice), refused.includes("one arm")];
    },
    [true, false, true],
  ],

  // The smallest drop: in 5-point steps below the case's screened pass rate of old, the first the
  // rule catches with a chance of at least 80%. The report of the cut (INFRA-4676's) printed
  // 87.52% at old 0.9, new 0.5, none 0.1: the same figure, compared with the cut or with no skill.
  [
    "a case at old 0.9, none 0.1 gives 40 points at the 87.5% chance",
    () => {
      const none = smallestDrop({ old: 9, none: 1 }, "none");
      const cut = smallestDrop({ old: 9, none: 1 }, "new");
      return [
        none.old,
        none.points,
        none.reaches,
        percent(none.chance),
        none.chance === lossChance({ old: 0.9, cut: 0.5, none: 0.5 }, "none"),
        lossChance({ old: 0.9, cut: 0.55, none: 0.55 }, "none") < 0.8,
        cut.points,
        cut.reaches,
        cut.chance === lossChance({ old: 0.9, cut: 0.5, none: 0.1 }),
        percent(cut.chance),
      ];
    },
    [90, 40, true, "87.52%", true, true, 40, true, true, "87.52%"],
  ],
  [
    "a case at old 0.1: no drop reaches 80%, and the figure gives the chance at the largest drop",
    () => {
      const drop = smallestDrop({ old: 1, none: 0 }, "none");
      return [
        drop.points,
        drop.reaches,
        drop.chance === lossChance({ old: 0.1, cut: 0, none: 0 }, "none"),
        drop.chance > 0 && drop.chance < 0.8,
      ];
    },
    [10, false, true, true],
  ],
  [
    "a case whose old text passes no screen run has no drop, at no chance",
    () => smallestDrop({ old: 0, none: 0 }, "none"),
    { old: 0, points: 0, chance: 0, reaches: false },
  ],
  [
    "a skill with no informative case gets the median of its screened cases' figures",
    () => {
      // Old 0.9, 0.7 and 1.0: 40, 40 and 35 points; none of the three is informative.
      const score = scoreSkill(
        [...screenNone("c1", 9, 9), ...screenNone("c2", 7, 7), ...screenNone("c3", 10, 10)],
        "none"
      );
      const figures = dropFigures(score, "none");
      return [
        figures.cases.map(({ caseId, informative, drop }) => [caseId, informative, drop.points]),
        figures.screened,
        figures.informative,
      ];
    },
    [
      [
        ["c1", false, 40],
        ["c2", false, 40],
        ["c3", false, 35],
      ],
      40,
      undefined,
    ],
  ],
  [
    "the median falls between two figures, or on a case no drop catches",
    () => {
      const two = dropFigures(
        scoreSkill([...screenNone("c1", 10, 0), ...screenNone("c2", 9, 0)], "none"),
        "none"
      );
      const caught = dropFigures(
        scoreSkill([...screenNone("c1", 1, 0), ...screenNone("c2", 1, 0)], "none"),
        "none"
      );
      return [two.screened, two.informative, caught.screened];
    },
    [37.5, 37.5, Number.POSITIVE_INFINITY],
  ],
];

test.each(comparedRows)("%s", (_name, got, want) => {
  expect(got()).toEqual(want);
});
