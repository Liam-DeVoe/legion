import { afterAll, beforeAll, expect, test } from "bun:test";
import { rmSync } from "node:fs";
import * as path from "node:path";
import { stageSkills } from "@legion/pi-shared/test/skills-guard";
import { skillPins } from "./skill-pins";

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

// A decision of the owner for an agent of a task goes into the task's text, in a later version of
// the plan, which every later start of the task holds; the agent at work reads no new start, so
// the owner also messages the agent of the step register_plan's answer names. On the default steps
// that version's one block is the task `default`, with no steps, which Legion takes while the task
// runs. An issue that started before Legion took plans has no task text, so there the decision
// goes in a comment and a message. The rule holds over any place a general skill offers, and the
// issue and its plan are the one record. A later version that changes only a running task's text
// is taken.
test("a decision for an agent goes into its task's text, and a message reaches the agent that runs", () => {
  expect(
    lacks("legion-architect/references/plan-changes.md", [
      /changes only the text of a running task is taken[^.;]*goes on at its step/,
      /decision for an agent of a task into the task's text[^.;]*later version of the plan/,
      /Every later start of the task holds that text[^.;]*new session/,
      /agent at work now reads no new start/,
      /answer of `register_plan` names the step[^.;]*`running`[^.;]*send that step's agent[^.;]*by message/,
      /On the project's default steps, the task is `default`/,
      /one task block is `:::plan_task\{#default\}`[^.;]*no `steps`[^.;]*text holds the decision/,
      /Legion takes that version while the task runs[^.;]*stops nothing/,
      /refuses a version that removes the task `default`/,
      /started its work before Legion took plans has no task text/,
      /such an issue[^.;]*comment on the issue[^.;]*by message/,
      /hold also where a general skill offers another place/,
      /A list of open items, such as an issue's `open-items\.md`, is such a place/,
      /Write no decision for an agent in it, and do not keep it as the record of the work/,
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

// A fresh agent that read only the sentence above still read a list of open items before it acted,
// because one of its items named the agent's role: it took the item for work, not for a decision.
// So the worker skill names that list and says the agent reads it not at all.
test("a worker reads no list of open items, even one with an item for its role", () => {
  countedOncePerFile(
    "Read no list of open items, even one naming your role.",
    ["skills/legion-worker/SKILL.md"]
  );
});
