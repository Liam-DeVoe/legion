import { afterAll, beforeAll, expect, test } from "bun:test";
import { rmSync } from "node:fs";
import * as path from "node:path";
import { stageSkills } from "@legion/pi-shared/test/skills-guard";
import { collapsed, skillPins } from "./skill-pins";

// The pins of an issue's plan and of where a decision for an agent goes: what the architect skill
// says of giving a plan and a later version of it, and what a phase worker reads for its
// architect's decisions. They read the skills this package stages into dist/skills, staged here as
// skills-guard.test.ts stages them. Each shows the text ships, never that a role follows it.
let staged: string;

beforeAll(() => {
  staged = stageSkills("@sjawhar/pi-legion");
});

afterAll(() => {
  rmSync(path.dirname(staged), { recursive: true, force: true });
});

const { countedOncePerFile, lacks } = skillPins(() => staged);

test("an issue runs the default steps unless its architect registers a plan before its first step", () => {
  expect([
    ...lacks("legion-architect/SKILL.md", [
      /references\/plan\.md`[^.;]*before `register_gate`[^.;]*before `release_children`/,
      /issue with none runs the default steps/,
      /stop a task[^.;]*decide something for an agent[^.;]*references\/plan-changes\.md`/,
    ]),
    ...lacks("legion-architect/references/plan.md", [
      /\*\*A root:\*\* before `register_gate`/,
      /\*\*A child:\*\* before `release_children`/,
    ]),
    ...lacks("legion-worker/SKILL.md", [/default task[^.;]*tester[^.;]*reviewer[^.;]*acceptance/]),
  ]).toEqual([]);
});

// The owner's plan is `owner-plan.md` under one condition, stated once in the plan reference and
// named in each sentence that sends the owner there: an issue that started on the project's
// default steps, where Legion's default plan was in force with no document. A plan given first
// under one condition makes Legion refuse the other's document (`PLAN_DOCUMENT_CHANGED`), so the
// earlier condition, a planner of the default steps having written `plan.md`, is gone.
test("one condition sends the owner to owner-plan.md", () => {
  const condition = "an issue that started on the project's default steps";
  expect([
    ...lacks("legion-architect/references/plan.md", [
      /or `owner-plan\.md` on an issue that started on the project's default steps/,
      /An issue that started on the project's default steps is one whose plan in force, when its work started, was Legion's default, with no document/,
      /On such an issue, write your plan[^.;]*`owner-plan\.md`/,
    ]),
    ...lacks("legion-architect/references/plan-changes.md", [
      /On an issue that started on the project's default steps[^.;]*the task is `default`/,
    ]),
  ]).toEqual([]);
  for (const file of [
    "legion-architect/references/plan.md",
    "legion-architect/references/plan-changes.md",
  ]) {
    const text = collapsed(path.join(staged, file));
    expect(text.includes("a planner of the default steps wrote"), file).toBe(false);
    expect(text.includes("On the project's default steps, the task is"), file).toBe(false);
    expect(text.includes(condition), file).toBe(true);
  }
});

// A decision of the owner for an agent of a task goes into the task's text, in a later version of
// the plan, which every later start of the task holds; the agent at work reads no new start, so
// the owner also messages the agent of the step register_plan's answer names. On an issue that
// started on the project's default steps the planner's `plan.md` stays as it is: the owner gives
// its own `owner-plan.md`, whose one block is the task `default`, with no steps, which Legion takes
// while the task runs, and after a stop of `default` it gives a version of that same document. An
// issue that started before Legion took plans has no task text, so there the decision goes in a
// comment and a message. The rule holds over any place a general skill offers, and the issue and
// its plan are the one record. A later version that changes only a running task's text is taken.
test("a decision for an agent goes into its task's text, and a message reaches the agent that runs", () => {
  expect(
    lacks("legion-architect/references/plan-changes.md", [
      /changes only the text of a running task is taken[^.;]*goes on at its step/,
      /decision for an agent of a task into the task's text[^.;]*later version of the plan/,
      /Every later start of the task holds that text[^.;]*new session/,
      /agent at work now reads no new start/,
      /answer of `register_plan` names the step[^.;]*`running`[^.;]*send that step's agent[^.;]*by message/,
      /one task block is `:::plan_task\{#default\}`[^.;]*no `steps`[^.;]*text holds the decision/,
      /Legion takes that version while the task runs[^.;]*stops nothing/,
      /`plan\.md` is the planner's: leave it as it is/,
      /document of your own on the issue, `owner-plan\.md`[^.;]*one task block is `:::plan_task\{#default\}`/,
      /each later version of yours[^.;]*same `owner-plan\.md`[^.;]*`PLAN_DOCUMENT_CHANGED`/,
      /For the task `default`, give it as a version of your own `owner-plan\.md`/,
      /started its work before Legion took plans has no task text/,
      /such an issue[^.;]*comment on the issue[^.;]*by message/,
      /hold also where a general skill offers another place[^.;]*write no decision for an agent there[^.;]*keep no record of the work there/,
      /issue and its plan are the record of the work to be done/,
    ])
  ).toEqual([]);
});

// A decision in a document beside the issue, the task and the handoffs reaches no worker, because
// no start task names that document. This shows the text ships, never that a role follows it.
test("a worker's first step reads the issue, its task and the handoffs, and no other document for decisions", () => {
  countedOncePerFile(
    "Read the issue, then your task, then each earlier phase's handoff (`dispatch handoff-read --issue <issue> --phase <plan|implement|test|review>`). Your architect's decisions for you come in your task and your messages: read no other document for them.",
    ["skills/legion-worker/SKILL.md"]
  );
});

// A fresh agent that read only the sentence above still read another document of the issue before
// it acted, because one of its items named the agent's role: it took the item for work, not for a
// decision. So the worker skill says it reads no other document for work either, even one that
// names it. The words name no such document, since no skill names it (pi-envoy's skills guard).
test("a worker reads no other document for work, even one that names it", () => {
  countedOncePerFile("Read no other document for work, even one naming you.", [
    "skills/legion-worker/SKILL.md",
  ]);
});
