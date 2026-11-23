/*
 * The steps of a command-bar run (design v2, states 3 and 4), ported from the tool section of
 * slopus/happy-desktop at commit 533c9a1: `packages/happy-desktop-ui/src/AgentToolSection.tsx`, a
 * run of tool calls drawn as grey lines, live while it runs and summed up once it is over by what
 * it was made of and how long it took; `conversationToolSections.ts`, where the calls between two
 * pieces of the agent's text are one section; and `elapsedTimeFormat.ts`, an elapsed time in its
 * smallest useful units. The port keeps those rules in Dispatch's words and its Tailwind classes
 * (`theme/classes`), not Happy's CSS variables. It draws each call as a line of its own, as the
 * bar's design asks, and leaves out what the bar does not use: the letter-by-letter arrival
 * (`ArrivingText`), the tool icons and the detail view a call opens. `package.json` and `LICENSE`
 * beside this file list the code in the build's THIRD_PARTY_NOTICES.txt.
 *
 * The code it is ported from is under happy-desktop's MIT license (its `LICENSE`):
 *
 * MIT License
 *
 * Copyright (c) 2026 Happy (2) Contributors
 *
 * Permission is hereby granted, free of charge, to any person obtaining a copy
 * of this software and associated documentation files (the "Software"), to deal
 * in the Software without restriction, including without limitation the rights
 * to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
 * copies of the Software, and to permit persons to whom the Software is
 * furnished to do so, subject to the following conditions:
 *
 * The above copyright notice and this permission notice shall be included in all
 * copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
 * IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
 * FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
 * AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
 * LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
 * OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
 * SOFTWARE.
 */

import { type ReactNode, useLayoutEffect, useRef, useState } from "react";

import { dangerText, linkHoverText, linkText, textMutedOnSurface } from "../../../theme/classes";
import { buildReferencePath } from "../../refs/routes";
import type { RunStep } from "../run-frames";

/** How many finished steps show as lines while the run works; two or more older ones fold into
 *  one line. */
export const FINISHED_LINES = 3;
/** How many calls that run at once show as lines; the rest fold into "and 2 more…". */
export const RUNNING_LINES = 3;

/** The shimmer of a line that runs. It stops where the person asked for less motion. */
const SHIMMER = "motion-safe:animate-pulse";

/** An issue key, as the server names the items of a step (`going/agent/labels.go`). */
const ISSUE_KEY = /^[A-Z][A-Z0-9]{1,9}-[1-9][0-9]*$/;

/**
 * An elapsed time in its smallest useful units: "48 s", "1 min 48 s", "2 h 5 min". It is Happy's
 * `elapsedTimeFormat`, in the words the bar writes a time in.
 */
export function elapsedTimeWords(elapsedMs: number): string {
  const totalSeconds = Math.max(0, Math.floor(elapsedMs / 1_000));
  if (totalSeconds < 60) return `${totalSeconds} s`;
  const totalMinutes = Math.floor(totalSeconds / 60);
  if (totalMinutes < 60) return `${totalMinutes} min ${totalSeconds % 60} s`;
  return `${Math.floor(totalMinutes / 60)} h ${totalMinutes % 60} min`;
}

/** How often something was done, in words: "once", "twice", "3 times". */
function times(count: number): string {
  if (count === 1) return "once";
  return count === 2 ? "twice" : `${count} times`;
}

/** `count` of `noun`, counted: "1 item", "14 items". */
function counted(count: number, noun: string): string {
  return `${count} ${count === 1 ? noun : `${noun}s`}`;
}

/** What each kind of step adds to a fold's words, in the order a fold names them. A read that
 *  answered a list counts its results as the items it read, and any other read the one thing it
 *  read. */
const KIND_WORDS: readonly (readonly [string, (steps: readonly RunStep[]) => string])[] = [
  [
    "read",
    (steps) =>
      `read ${counted(
        steps.reduce((sum, step) => sum + Math.max(step.count, 1), 0),
        "item"
      )}`,
  ],
  ["search", (steps) => `searched ${times(steps.length)}`],
  ["look", (steps) => `looked for a way ${times(steps.length)}`],
  ["chart", (steps) => (steps.length === 1 ? "drew a chart" : `drew ${steps.length} charts`)],
  ["plan", (steps) => (steps.length === 1 ? "proposed a plan" : `proposed ${steps.length} plans`)],
  [
    "review",
    (steps) =>
      steps.length === 1 ? "reviewed the plan" : `reviewed the plan ${times(steps.length)}`,
  ],
];

/** The kinds `KIND_WORDS` names. */
const NAMED_KINDS: ReadonlySet<string> = new Set(KIND_WORDS.map(([kind]) => kind));

/**
 * What `steps` were made of, in words joined with ", ": "read 14 items, searched twice, drew a
 * chart". It is Happy's `sectionKinds`, which counts each kind of step, with four changes: each
 * kind has its own words; the kinds come in one fixed order, not the most frequent first; a
 * failed step counts as "1 step failed", not with its kind, since it did not do what its kind
 * says; and a slip counts nowhere, since it did nothing and its person has nothing to do about
 * it. A step of a kind this page does not know counts among "other steps". Steps that were all
 * slips are said in no words at all: "".
 */
export function foldWords(steps: readonly RunStep[]): string {
  const counts = steps.filter((step) => !step.slip);
  const done = counts.filter((step) => !step.refused);
  const parts: string[] = [];
  for (const [kind, words] of KIND_WORDS) {
    const ofKind = done.filter((step) => step.kind === kind);
    if (ofKind.length > 0) parts.push(words(ofKind));
  }
  const other = done.filter((step) => !NAMED_KINDS.has(step.kind)).length;
  if (other > 0) parts.push(counted(other, "other step"));
  const failed = counts.length - done.length;
  if (failed > 0) parts.push(`${counted(failed, "step")} failed`);
  return parts.join(", ");
}

/** How long a run worked, for the line its steps fold into: "1 min 48 s"; undefined, as in
 *  Happy, for a run that worked for less than a second, and for one whose time the page does not
 *  know. */
function workedTime(workedMs: number | undefined): string | undefined {
  return workedMs === undefined || workedMs < 1_000 ? undefined : elapsedTimeWords(workedMs);
}

/** What the line steps fold into says after how long the run worked: what the steps were made
 *  of (`foldWords`), with its first letter a capital where no time comes before it, and "Steps"
 *  for steps that were all slips and no time to say. */
function workedRest(steps: readonly RunStep[], time: string | undefined): string {
  const words = foldWords(steps);
  if (time !== undefined) return words;
  return words === "" ? "Steps" : words.charAt(0).toUpperCase() + words.slice(1);
}

/**
 * The one line steps fold into. Once the run's answer or its plan came, or the run ended (design
 * v2, state 4), it is every step's, with how long the run worked: "Worked for 1 min 48 s · read
 * 14 items, searched twice". A run whose time `workedTime` does not say, and older steps folded
 * while the run works (state 3), say what the steps were made of alone: "Read 6 items, searched
 * twice". Steps that were all slips say only the time, "Worked for 12 s", or with no time to
 * say, "Steps".
 */
export function workedWords(steps: readonly RunStep[], workedMs: number | undefined): string {
  const time = workedTime(workedMs);
  const rest = workedRest(steps, time);
  if (time === undefined) return rest;
  return rest === "" ? `Worked for ${time}` : `Worked for ${time} · ${rest}`;
}

/** `workedWords` as the folded line draws them, the time in an element of its own
 *  (`going-steps-fold-time`), so that a screenshot can mask the one part that moves with the
 *  run's pace. */
function WorkedLine({
  steps,
  workedMs,
}: {
  readonly steps: readonly RunStep[];
  readonly workedMs: number | undefined;
}): ReactNode {
  const time = workedTime(workedMs);
  const rest = workedRest(steps, time);
  if (time === undefined) return rest;
  return (
    <>
      Worked for <span data-testid="going-steps-fold-time">{time}</span>
      {rest === "" ? null : ` · ${rest}`}
    </>
  );
}

/** Which steps draw as lines while the run works (design v2, state 3). */
export interface StepLines {
  /** The finished steps that fold into one line above the rest: all but the newest
   *  `FINISHED_LINES`. */
  readonly older: readonly RunStep[];
  /** The steps drawn as lines, in their order: the newest finished ones and the first
   *  `RUNNING_LINES` of those that run. */
  readonly shown: readonly RunStep[];
  /** How many running steps past `RUNNING_LINES` fold into "and N more…". */
  readonly more: number;
}

/** Which of `steps` draw as lines while the run works, and which fold. The finished steps past
 *  the newest `FINISHED_LINES` fold into one line only where there are two or more of them: one
 *  such step draws as its own line, in its own words ("Looked for a way to list an issue's
 *  messages"), which takes the room the line folding it would. */
export function stepLines(steps: readonly RunStep[]): StepLines {
  const finished = steps.filter((step) => !step.running);
  const running = steps.filter((step) => step.running);
  const past = finished.length - FINISHED_LINES;
  const older = past > 1 ? finished.slice(0, past) : [];
  const folded = new Set([...older, ...running.slice(RUNNING_LINES)]);
  return {
    more: Math.max(0, running.length - RUNNING_LINES),
    older,
    shown: steps.filter((step) => !folded.has(step)),
  };
}

/**
 * The time since `since`, in this page's clock, counted each second (`elapsedTimeWords`). It
 * writes the time into the page itself, so a tick draws nothing else again, and it is hidden from
 * a screen reader, which would hear every second.
 */
export function RunTimer({
  className = "",
  since,
  testId,
}: {
  readonly className?: string;
  readonly since: number;
  readonly testId?: string;
}): ReactNode {
  const shown = useRef<HTMLSpanElement>(null);
  useLayoutEffect(() => {
    const write = () => {
      if (shown.current !== null) {
        shown.current.textContent = elapsedTimeWords(Date.now() - since);
      }
    };
    write();
    const timer = window.setInterval(write, 1000);
    return () => window.clearInterval(timer);
  }, [since]);
  return (
    <span
      aria-hidden="true"
      className={`shrink-0 tabular-nums ${className}`}
      data-testid={testId}
      ref={shown}
    />
  );
}

/** A step's words, each issue key among the items it named a plain link to its issue. */
function LabelWords({
  items,
  label,
}: {
  readonly items: readonly string[];
  readonly label: string;
}): ReactNode {
  const keys = items.filter((item) => ISSUE_KEY.test(item));
  if (keys.length === 0) return label;
  const parts: ReactNode[] = [];
  let at = 0;
  for (const match of label.matchAll(new RegExp(`\\b(?:${keys.join("|")})\\b`, "g"))) {
    parts.push(label.slice(at, match.index));
    parts.push(
      <a
        className={`${linkText} ${linkHoverText}`}
        href={buildReferencePath({ key: match[0], kind: "issue" })}
        key={match.index}
      >
        {match[0]}
      </a>
    );
    at = match.index + match[0].length;
  }
  parts.push(label.slice(at));
  return <>{parts}</>;
}

/** One step's line: its words in the present tense with a shimmer while it runs, in the past
 *  tense with its result once it ended, or the sentence of a call that failed, in red; a slip's
 *  line is grey, as any ended call's, with what the model tried. The newest line of a run that
 *  works carries the run's timer on the right. */
function StepLine({
  step,
  timer,
}: {
  readonly step: RunStep;
  readonly timer: number | undefined;
}): ReactNode {
  return (
    <li
      className={`flex min-w-0 items-baseline gap-2 text-xs ${step.refused ? dangerText : textMutedOnSurface}`}
      data-kind={step.kind}
      data-running={String(step.running)}
      data-testid="going-step"
      data-tool={step.tool}
    >
      <span
        className={`min-w-0 truncate ${step.running ? SHIMMER : ""}`}
        data-testid="going-step-label"
      >
        <LabelWords items={step.items} label={step.label} />
        {step.running ? "…" : null}
      </span>
      {timer === undefined ? null : (
        <RunTimer className="ml-auto" since={timer} testId="going-step-timer" />
      )}
    </li>
  );
}

/** A run of steps one model call made, under the prose it wrote beside them. */
interface HeadedSteps {
  readonly header: string;
  readonly first: number;
  readonly steps: RunStep[];
}

/** `steps` in runs that share a header, in their order: the calls between two pieces of the
 *  model's text are one run, as Happy's `conversationToolSections` groups them. */
function headed(steps: readonly RunStep[]): readonly HeadedSteps[] {
  const groups: HeadedSteps[] = [];
  for (const step of steps) {
    const last = groups.at(-1);
    if (last !== undefined && last.header === step.header) {
      last.steps.push(step);
    } else {
      groups.push({ first: step.id, header: step.header, steps: [step] });
    }
  }
  return groups;
}

/** The lines of `steps`: each run of calls under its grey header line where the model wrote one,
 *  then "and N more…" for the running calls past `RUNNING_LINES`. The newest line carries the
 *  run's timer, counted from `timer`, where one is given. */
function StepList({
  more,
  steps,
  timer,
}: {
  readonly more: number;
  readonly steps: readonly RunStep[];
  readonly timer: number | undefined;
}): ReactNode {
  const newest = more > 0 ? undefined : steps.at(-1)?.id;
  return (
    <div className="space-y-0.5" data-testid="going-steps">
      {headed(steps).map((group) => (
        <div className="space-y-0.5" key={group.first}>
          {group.header === "" ? null : (
            <p className={`truncate text-xs ${textMutedOnSurface}`} data-testid="going-step-words">
              {group.header}
            </p>
          )}
          <ol aria-label="Steps" className={`space-y-0.5 ${group.header === "" ? "" : "pl-3"}`}>
            {group.steps.map((step) => (
              <StepLine key={step.id} step={step} timer={step.id === newest ? timer : undefined} />
            ))}
          </ol>
        </div>
      ))}
      {more === 0 ? null : (
        <p
          className={`flex items-baseline gap-2 text-xs ${textMutedOnSurface}`}
          data-testid="going-steps-more"
        >
          <span className={SHIMMER}>and {more} more…</span>
          {timer === undefined ? null : (
            <RunTimer className="ml-auto" since={timer} testId="going-step-timer" />
          )}
        </p>
      )}
    </div>
  );
}

/** A grey line that stands for steps folded under it, and opens them on a click. */
function FoldLine({
  onToggle,
  open,
  testId,
  words,
}: {
  readonly onToggle: () => void;
  readonly open: boolean;
  readonly testId: string;
  readonly words: ReactNode;
}): ReactNode {
  return (
    <button
      aria-expanded={open}
      className={`flex min-h-6 max-w-full items-center gap-1 text-left text-xs hover:underline ${textMutedOnSurface}`}
      data-testid={testId}
      onClick={onToggle}
      type="button"
    >
      <span className="min-w-0 truncate">{words}</span>
      <span aria-hidden="true" className={`shrink-0 ${open ? "rotate-90" : ""}`}>
        ›
      </span>
    </button>
  );
}

/**
 * The steps of one run, as the bar over the page and the panel at the side both draw them.
 *
 * While the run works (design v2, state 3), each call has a line, as Happy's section is live:
 * the newest finished ones, at most `FINISHED_LINES`, under one grey line that folds the older
 * ones where there are two or more ("Read 6 items, searched twice ›", which opens them), then the
 * calls that run, at most `RUNNING_LINES` of them and "and 2 more…" for the rest. The prose the
 * model wrote beside its calls heads them as a grey line and folds with them. The newest line
 * carries the run's timer, counted from `since`.
 *
 * Once the run no longer works - its answer or its plan came, or it stopped or failed (`folded`,
 * design v2, states 4 and 9) - and while the rows of an approved plan run (state 7), every step
 * folds into one grey line, "Worked for 1 min 48 s · read 14 items, searched twice ›", as Happy's
 * section is summed up once it is over; a click opens them, and its mark turns to say so. The
 * line counts what the end of a stopped run counts ("read 14 items so far").
 */
export function StepSection({
  folded,
  live,
  since,
  steps,
  workedMs,
}: {
  /** The steps that draw a line: each call with words, without the changes of an approved plan,
   *  which the plan's card shows. */
  readonly steps: readonly RunStep[];
  /** Whether the run works, so that its newest line carries its timer. */
  readonly live: boolean;
  /** When the person asked for the run, in this page's clock: what its timer counts from. */
  readonly since: number;
  /** Whether the run no longer works, or its approved plan's rows run, so that every step folds
   *  into one line. */
  readonly folded: boolean;
  /** How long the run worked, for its folded line; undefined where the page does not know. */
  readonly workedMs: number | undefined;
}): ReactNode {
  const [olderOpen, setOlderOpen] = useState(false);
  const [workedOpen, setWorkedOpen] = useState(false);
  if (steps.length === 0) {
    return null;
  }
  if (folded) {
    return (
      <div className="space-y-1" data-testid="going-steps-section">
        <FoldLine
          onToggle={() => setWorkedOpen((open) => !open)}
          open={workedOpen}
          testId="going-steps-fold"
          words={<WorkedLine steps={steps} workedMs={workedMs} />}
        />
        {workedOpen ? <StepList more={0} steps={steps} timer={undefined} /> : null}
      </div>
    );
  }
  const lines = stepLines(steps);
  const timer = live ? since : undefined;
  return (
    <div className="space-y-0.5" data-testid="going-steps-section">
      {lines.older.length === 0 ? null : (
        <FoldLine
          onToggle={() => setOlderOpen((open) => !open)}
          open={olderOpen}
          testId="going-steps-older"
          words={workedWords(lines.older, undefined)}
        />
      )}
      {olderOpen ? (
        <StepList more={0} steps={steps} timer={timer} />
      ) : (
        <StepList more={lines.more} steps={lines.shown} timer={timer} />
      )}
    </div>
  );
}
