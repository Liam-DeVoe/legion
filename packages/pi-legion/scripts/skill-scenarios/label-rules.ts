// label-rules.ts: every sentence an ablation label cuts or keeps in the role texts it reads.

import type { Role } from "./role-texts";

export type Tree = "base" | "head";

export interface Entry {
  readonly file: string;
  readonly has?: string;
  readonly fenced?: true;
  readonly at?: Tree;
  readonly cut?: string;
  readonly keep?: string;
}

export interface Rule {
  readonly role: Role;
  readonly rule: string;
  readonly patterns: readonly RegExp[];
  readonly entries: readonly Entry[];
}

const LATER_FAIL: readonly RegExp[] = [
  /pass and (a )?fail|fail and (a )?pass|after a pass|later (fail|pass|result)|fail recorded after/i,
  /retract|not known|do not wait|no wait\b|wait for a later/i,
  /\bfail(s|ed)?\b[^.]{0,80}\bpass(es)?\b|\bpass(es)?\b[^.]{0,80}\bfail(s|ed)?\b/i,
];

const GUIDE_TWO_ROLES: readonly Entry[] = [
  {
    file: "skills/legion-architect/references/acceptance.md",
    has: "`dispatch results --issue <issue>`: each check's approved head's own line",
    cut: "Remove the implementer-and-tester requirement from this guide gate.",
  },
  {
    file: "skills/legion-architect/references/acceptance.md",
    has: "Each check's approved head's own line needs yes",
    cut: "Remove the implementer-and-tester requirement from this guide gate.",
  },
  {
    file: "skills/legion-retro/SKILL.md",
    has: "Each `before_merge` check's approved head's own line",
    cut: "Remove the implementer-and-tester requirement from this guide gate.",
  },
  {
    file: "skills/legion-worker/SKILL.md",
    has: "They override this skill, except",
    cut: "Remove the implementer-and-tester requirement from this guide gate.",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "Approval is gated by each `before_merge` check's reviewed head's own line",
    cut: "Remove the implementer-and-tester requirement from this guide gate.",
  },
  {
    file: "skills/legion-worker/references/pr-body.md",
    has: "A `before_merge` result stands while its commit",
    cut: "Remove the implementer-and-tester requirement from this guide gate.",
  },
];

const GUIDE_LATER_FAIL: readonly Entry[] = [
  {
    file: "skills/legion-architect/SKILL.md",
    has: "a pass and a fail at one head leave the check not known",
    cut: "Remove the architect's pass-and-fail consequence.",
  },
  {
    file: "skills/legion-architect/references/acceptance.md",
    has: "`dispatch results --issue <issue>`: each check's approved head's own line",
    cut: "Remove the reviewer's pass-and-fail consequence.",
  },
  {
    file: "skills/legion-architect/references/acceptance.md",
    has: "Each check's approved head's own line needs yes",
    cut: "Remove the reviewer's pass-and-fail consequence.",
  },
  {
    file: "skills/legion-architect/references/acceptance.md",
    has: "If the pull request merged before its last round",
    cut: "Remove the architect's pass-and-fail consequence.",
  },
  {
    file: "skills/legion-retro/SKILL.md",
    has: "Each `before_merge` check's approved head's own line",
    cut: "Remove the reviewer's pass-and-fail consequence.",
  },
  {
    file: "skills/legion-worker/SKILL.md",
    has: "They override this skill, except",
    cut: "Remove the reviewer's pass-and-fail consequence.",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "Approval is gated by each `before_merge` check's reviewed head's own line",
    cut: "Remove the reviewer's pass-and-fail consequence.",
  },
  {
    file: "skills/legion-worker/references/pr-body.md",
    has: "A `before_merge` result stands while its commit",
    cut: "Remove the reviewer's pass-and-fail consequence.",
  },
];

const ENTRIES_TWO_ROLES: readonly Entry[] = [
  {
    file: "packages/daemon/internal/prompts/go/reviewer.md",
    has: "A completion that names the handoff record you named for your previous round",
    keep: "Each round's handoff, not a Dispatch check.",
  },
  {
    file: "packages/daemon/internal/prompts/go/worker-common.md",
    has: "One write is the exception",
    keep: "A forward merge's writer, not a Dispatch check.",
  },
  {
    file: "packages/daemon/internal/prompts/go/worker-common.md",
    has: "It keeps each role's session",
    keep: "A released pod's state, not a Dispatch check.",
  },
  {
    file: "packages/daemon/internal/prompts/go/worker-common.md",
    has: "It keeps nothing else of the disk",
    keep: "A released pod's disk, not a Dispatch check.",
  },
  {
    file: "packages/daemon/internal/prompts/go/worker-common.md",
    has: "The implementer is asked to merge the base forward",
    keep: "A forward merge's assignment, not a Dispatch check.",
  },
  {
    file: "packages/daemon/internal/prompts/go/worker-common.md",
    has: "The plan is the exception",
    keep: "The roles that work from a plan, not a Dispatch check.",
  },
  {
    file: "packages/daemon/internal/prompts/roles/core/reviewer.md",
    has: "from the implementer and from the tester both",
    cut: "Remove the two-role requirement.",
  },
  {
    file: "packages/daemon/internal/prompts/roles/core/reviewer.md",
    has: "results from CI stand for both roles",
    cut: "Remove the two-role comparison from the CI sentence.",
  },
  {
    file: "packages/daemon/internal/prompts/roles/mechanics/headless.md",
    has: "Under `runtime: kubernetes` the daemon releases an issue pod",
    keep: "A released pod, not a Dispatch check.",
  },
  {
    file: "packages/daemon/internal/prompts/roles/reviewer.md",
    has: "from the implementer and from the tester both",
    cut: "Remove the two-role requirement.",
  },
  {
    file: "skills/legion-controller/references/daily-report.md",
    has: "Phase workers nothing will prompt",
    keep: "A controller's silent-worker report, not a Dispatch check.",
  },
  {
    file: "skills/legion-controller/references/escalation.md",
    has: "The daemon replaces a pod whose configuration moved only after an update round",
    keep: "A pod update, not a Dispatch check.",
  },
  {
    file: "skills/legion-retro/SKILL.md",
    has: "Any other check with no pass standing, or a check with no `ci_check` whose only evidence is a unit suite",
    keep: "The destination of a failure, not who makes a pass.",
  },
  {
    file: "skills/legion-retro/SKILL.md",
    fenced: true,
    keep: "A format or command in a fenced block.",
  },
  {
    file: "skills/legion-worker/SKILL.md",
    has: "name: legion-worker",
    keep: "The skill description.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "What each role does",
    keep: "A section heading, not a Dispatch rule.",
  },
  {
    file: "skills/legion-worker/references/review-threads.md",
    has: "The implementer records each thread in the PR body's `Threads` section",
    keep: "Review-thread recording, not a Dispatch check.",
  },
  ...GUIDE_TWO_ROLES,
];

const ENTRIES_LATER_FAIL: readonly Entry[] = [
  {
    file: "packages/daemon/internal/prompts/roles/core/reviewer.md",
    has: "That answer leaves out a pass at another commit",
    cut: "Restore the old later-fail rule.",
  },
  {
    file: "packages/daemon/internal/prompts/roles/core/reviewer.md",
    has: "Until that run concludes, wait for it by its name, unless that head's line reads not known",
    cut: "Remove the pass-and-fail exception from the wait.",
  },
  {
    file: "packages/daemon/internal/prompts/roles/core/reviewer.md",
    has: "A pass and a fail at one head, from any role, leave that head not known",
    cut: "Restore the prior CI-only rule.",
  },
  {
    file: "packages/daemon/internal/prompts/roles/reviewer.md",
    has: "Wait for a `ci_check` run still going or unread, unless the head's line reads not known",
    cut: "Remove the pass-and-fail exception from the wait.",
  },
  {
    file: "packages/daemon/internal/prompts/roles/reviewer.md",
    has: "A pass and a fail at one head leave it not known, whoever recorded them",
    cut: "Remove the reviewer rule.",
  },
  {
    file: "packages/daemon/internal/prompts/roles/reviewer.md",
    has: "A pass and a fail at any open head make the check not known",
    cut: "Remove the reviewer rule.",
  },
  {
    file: "skills/legion-architect/SKILL.md",
    has: "Act on delivered wakes by the",
    keep: "Wake routing, not a check result.",
  },
  {
    file: "skills/legion-architect/SKILL.md",
    has: "A root's questions go on the root",
    keep: "A root question, not a check result.",
  },
  {
    file: "skills/legion-retro/SKILL.md",
    has: "A `ci_check` check has no pass standing at that head when",
    keep: "A CI-proved check's result, not the no-ci-check rule.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    fenced: true,
    keep: "A format or command in a fenced block.",
  },
  {
    file: "skills/legion-worker/references/pr-body.md",
    fenced: true,
    keep: "A format or command in a fenced block.",
  },
  {
    file: "skills/thermonuclear-deep-review/SKILL.md",
    has: "Order, not coverage: after a pipeline sanitizes",
    keep: "A security-review question, not a Dispatch check.",
  },
  {
    file: "skills/thermonuclear-deep-review/SKILL.md",
    has: "`untrusted-input`",
    keep: "A security-review table row, not a Dispatch check.",
  },
  ...GUIDE_LATER_FAIL,
];

const ENTRIES_ARCHITECT_LATER_FAIL: readonly Entry[] = [
  {
    file: "packages/daemon/internal/prompts/roles/architect-root.md",
    has: "The person's answer, resolution or reply on any ask of the root",
    keep: "A person's answer, not a check result.",
  },
  {
    file: "packages/daemon/internal/prompts/roles/architect-root.md",
    has: "A pass and a fail at any open head make the check not known",
    cut: "Remove the architect rule.",
  },
  {
    file: "packages/daemon/internal/prompts/roles/architect-root.md",
    has: "To give a check to CI, write a new check block under a new id",
    keep: "A CI-check authoring rule, not the pass-and-fail rule.",
  },
  {
    file: "skills/legion-architect/SKILL.md",
    has: "Act on delivered wakes by the",
    keep: "Wake routing, not a check result.",
  },
  {
    file: "skills/legion-architect/SKILL.md",
    has: "A root's questions go on the root",
    keep: "A root question, not a check result.",
  },
  {
    file: "skills/legion-retro/SKILL.md",
    has: "A `ci_check` check has no pass standing at that head when",
    keep: "A CI-proved check's result, not the architect rule.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    fenced: true,
    keep: "A format or command in a fenced block.",
  },
  {
    file: "skills/legion-worker/references/pr-body.md",
    fenced: true,
    keep: "A format or command in a fenced block.",
  },
  {
    file: "skills/thermonuclear-deep-review/SKILL.md",
    has: "Order, not coverage: after a pipeline sanitizes",
    keep: "A security-review question, not a Dispatch check.",
  },
  {
    file: "skills/thermonuclear-deep-review/SKILL.md",
    has: "`untrusted-input`",
    keep: "A security-review table row, not a Dispatch check.",
  },
  ...GUIDE_LATER_FAIL,
];

export const RULES: Record<string, Rule> = {
  "ablate-two-roles": {
    role: "reviewer",
    rule: "A check with no `ci_check` has a pass at a head only from the implementer and from the tester both.",
    patterns: [
      /implementer[^.]*\btester|tester[^.]*\bimplementer/i,
      /\b(both|two|which|each) roles?\b|implementer.s own|from the implementer\b|from the tester\b/i,
      /implementer records|tester records|records? (its|their|your) own/i,
    ],
    entries: ENTRIES_TWO_ROLES,
  },
  "ablate-later-fail": {
    role: "reviewer",
    rule: "A pass and a fail at one head leave it not known, whoever recorded them: request changes, with no wait.",
    patterns: LATER_FAIL,
    entries: ENTRIES_LATER_FAIL,
  },
  "ablate-architect-later-fail": {
    role: "architect",
    rule: "A pass and a fail at one head leave it not known and the check's answer never yes: send the issue back, with no wait.",
    patterns: LATER_FAIL,
    entries: ENTRIES_ARCHITECT_LATER_FAIL,
  },
};
