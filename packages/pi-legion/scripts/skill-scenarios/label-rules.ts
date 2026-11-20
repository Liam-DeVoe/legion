// label-rules.ts: the ablation data of rule-search.ts. For each ablate label, the rule it removes,
// the role whose texts state that rule, the patterns that find each sentence that may state it, and
// the entries that classify each of those sentences: `cut` (the label deletes it or undoes T5.12's
// change of it) or `keep` (the reason it does not state the rule).

import type { Role } from "./role-texts";

export type Tree = "base" | "head";

export interface Entry {
  /** The text, relative to the checkout. */
  readonly file: string;
  /** Words of the sentence, with runs of white space as one space; absent on a `fenced` entry. */
  readonly has?: string;
  /** Keeps each hit in a fenced block of the file: a format, a template or a command. It may match
   * any number of hits, none included. */
  readonly fenced?: true;
  /** Present only on a sentence that is a hit in one of the two trees alone: one that T5.12 or T5.13
   * adds (head) or removes (base), or one whose words main, or a later round of the pull request,
   * changed after them, so that the head holds it as a hit (head) or no longer does (base). */
  readonly at?: Tree;
  /** What the label does to this sentence. */
  readonly cut?: string;
  /** Why the sentence does not state the label's rule. */
  readonly keep?: string;
}

export interface Rule {
  readonly role: Role;
  /** The rule the label removes, in one sentence. */
  readonly rule: string;
  readonly patterns: readonly RegExp[];
  readonly entries: readonly Entry[];
}

const LATER_FAIL: readonly RegExp[] = [
  /pass and (a )?fail|fail and (a )?pass|after a pass|later (fail|pass|result)|fail recorded after/i,
  /retract|not known|do not wait|no wait\b|wait for a later/i,
  /\bfail(s|ed)?\b[^.]{0,80}\bpass(es)?\b|\bpass(es)?\b[^.]{0,80}\bfail(s|ed)?\b/i,
];

const ENTRIES_TWO_ROLES: readonly Entry[] = [
  {
    file: "packages/daemon/internal/prompts/go/reviewer.md",
    has: "A completion that names the handoff record you named for your previous round",
    keep: "Each round's own handoff. It names no check of Dispatch.",
  },
  {
    file: "packages/daemon/internal/prompts/go/worker-common.md",
    has: "It keeps each role's session, phase",
    keep: "A released pod. It names no check of Dispatch.",
  },
  {
    file: "packages/daemon/internal/prompts/go/worker-common.md",
    has: "It keeps nothing else of the disk",
    keep: "A released pod's disk. It names no check of Dispatch.",
  },
  {
    file: "packages/daemon/internal/prompts/go/worker-common.md",
    has: "The plan is the exception, and the rest of this paragraph",
    keep: "The roles that work from a plan. It names no check of Dispatch.",
  },
  {
    file: "packages/daemon/internal/prompts/go/worker-common.md",
    has: "One write is the exception: the forward merge the daemon asks of the implementer",
    keep: "Who writes the workspace during a merge of the base the daemon asks for. It names no check of Dispatch.",
    at: "head",
  },
  {
    file: "packages/daemon/internal/prompts/go/worker-common.md",
    has: "The implementer is asked to merge the base forward in the issue workspace and push",
    keep: "The daemon's ask of a merge of the base, and the assignments it leaves standing. It names no check of Dispatch.",
    at: "head",
  },
  {
    file: "packages/daemon/internal/prompts/roles/core/reviewer.md",
    has: "Verify as forge facts, never from reports",
    cut: "It asks a pass from both roles. The label deletes `from the implementer and from the tester both,` and `: a line that names one of the two roles alone is no pass for your approval`.",
  },
  {
    file: "packages/daemon/internal/prompts/roles/core/reviewer.md",
    has: "A check carrying `ci_check` is CI's, and its results from CI stand for both roles",
    cut: "`stand for both roles` assumes the rule on every other check. The label makes those words `come from CI`. The case of this label has no `ci_check`.",
  },
  {
    file: "packages/daemon/internal/prompts/roles/mechanics/headless.md",
    has: "Under `runtime: kubernetes` the daemon releases an issue pod",
    keep: "A released pod. It names no check of Dispatch.",
  },
  {
    file: "packages/daemon/internal/prompts/roles/reviewer.md",
    has: "Your approval is gated by the `before_merge` checks",
    keep: "`from which roles` names what the line shows, not which roles a pass needs.",
    at: "base",
  },
  {
    file: "packages/daemon/internal/prompts/roles/reviewer.md",
    has: "Your approval is gated by the `before_merge` checks",
    cut: "T5.13's gate text asks a pass from both roles. The label deletes ` from the implementer and from the tester both`, and makes `: a line that names one of the two roles alone is no pass for your approval, and a pass at another commit is none` read `: a pass at another commit is none`.",
    at: "head",
  },
  {
    file: "packages/daemon/internal/prompts/roles/tester.md",
    has: "A `before_merge` check reads yes at the head you test only with a pass from the implementer and a pass from the tester there",
    cut: "INFRA-4303 moved the two-role rule into the tester's role prompt. The label deletes it.",
    at: "head",
  },
  {
    file: "skills/legion-retro/SKILL.md",
    has: "and the pull request body's `Checks` block carries each of those lines",
    cut: "The retro's gate asks a pass from both roles. The label deletes `from the implementer and from the tester alike,`.",
  },
  {
    file: "skills/legion-retro/SKILL.md",
    has: "When any other check has no pass standing at that head",
    keep: "It names the role that a missing result goes back to. The line of the head says whether a pass stands, and the sentence sets no role that a pass needs.",
  },
  {
    file: "skills/legion-retro/SKILL.md",
    fenced: true,
    keep: "A line of a fenced block: a format, a template or a command, which states no rule.",
  },
  {
    file: "skills/legion-worker/SKILL.md",
    has: "name: legion-worker",
    keep: "The skill's description.",
  },
  {
    file: "skills/legion-worker/SKILL.md",
    has: "The plan is the exception to it, and the rest of this item",
    keep: "The roles that work from a plan. It names no check of Dispatch.",
  },
  {
    file: "skills/legion-worker/SKILL.md",
    has: "Every handoff body holds `head`",
    keep: "The head that each role's handoff holds. It sets no condition on a pass.",
  },
  {
    file: "skills/legion-worker/SKILL.md",
    has: "It keeps nothing else of the disk: `$TMPDIR`",
    keep: "A released pod's disk. It names no check of Dispatch.",
  },
  {
    file: "skills/legion-worker/SKILL.md",
    has: "Deployment instructions in your prompt override this skill",
    cut: "The third of the four rules that no deployment overrides is the implementer's own result. The label deletes that rule and its links, and makes `four rules` `three rules`.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "A run of the named job on a push of the change's branch also records its own result",
    keep: "A CI job's own record. It names no role.",
    at: "base",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "A check with `ci_check` needs no pass from the implementer.",
    cut: "By contrast, it says that a check with no `ci_check` needs a pass of the implementer. The label deletes it.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "The tester writes in its `test` handoff's `implementerProof.how`",
    keep: "The tester's note of a run still going. It sets no condition on a pass.",
  },
  {
    file: "skills/legion-worker/references/conflicts-and-rewrites.md",
    has: "This is what lets each role compare against",
    keep: "Fingerprints of heads. It names no check of Dispatch.",
    at: "base",
  },
  {
    file: "skills/legion-worker/references/conflicts-and-rewrites.md",
    has: "Where each role gets its two heads",
    keep: "Fingerprints of heads. It names no check of Dispatch.",
    at: "base",
  },
  {
    file: "skills/legion-worker/references/default-branch-check.md",
    has: "The troubleshooting section on which role made a commit",
    keep: "Which role made a commit. It names no check of Dispatch.",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "Approval is refused while any `before_merge` check",
    keep: "`from which roles` names what the line shows, not which roles a pass needs.",
    at: "base",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "Approval is refused while any `before_merge` check",
    cut: "T5.13's gate text asks a pass from both roles. The label deletes ` from the implementer and from the tester both` and `: a line that names one of the two roles alone is no pass for the approval`.",
    at: "head",
  },
  {
    file: "skills/legion-worker/references/pr-body.md",
    has: "- **The implementer records a result for every `before_merge` check",
    keep: "The implementer's own step. It sets no condition on a pass, an approval or a test round.",
  },
  {
    file: "skills/legion-worker/references/pr-body.md",
    has: "- **The tester judges the implementer's results and records its own.**",
    keep: "The tester's own step. It sets no condition on a pass, an approval or a test round.",
  },
  {
    file: "skills/legion-worker/references/pr-body.md",
    has: "A `before_merge` check the implementer left with no pass standing at the head is a test failure",
    cut: "It makes a test round need the implementer's own pass. The label deletes it.",
  },
  {
    file: "skills/legion-worker/references/pr-body.md",
    has: "A check whose `run` is a whole test suite is the exception",
    keep: "`implementerProof` is the name of a field. The sentence sets no role that a pass needs.",
  },
  {
    file: "skills/legion-worker/references/pr-body.md",
    has: "A code path whose first execution is after merge",
    keep: "An `after_merge` check, which this label's cases do not have.",
  },
  {
    file: "skills/legion-worker/references/review-threads.md",
    has: "The implementer records each thread it answered",
    keep: "Review threads. It names no check of Dispatch.",
  },
  {
    file: "skills/legion-worker/references/tools.md",
    has: "The DSN's own database is owned by your login",
    keep: "A test database. It names no check of Dispatch.",
  },
  {
    file: "packages/daemon/internal/prompts/roles/tester.md",
    has: "It needs no pass from the implementer, and you do not run or record it.",
    cut: "It exempts a check carrying `ci_check` from the implementer's pass, so by contrast it asks that pass of a check with none, as `ci-checks.md:67` does. The label deletes it.",
  },
  {
    file: "packages/daemon/internal/prompts/roles/tester.md",
    has: "is a test failure, not a gap for you to fill",
    cut: "It fails a check whose head holds no pass of the implementer, half of the rule. The label deletes it.",
  },
  {
    file: "skills/legion-architect/SKILL.md",
    has: "You start or re-assign no worker and change no phase order",
    keep: "Which role owns a task. It names no result.",
  },
  {
    file: "skills/legion-architect/SKILL.md",
    has: "The checks are the gate every later role reads",
    cut: "`the implementer and the tester record a result against each check with no ci_check, and` and `, from the implementer and from the tester` give each such check a result of both roles. The label deletes those words.",
  },
  {
    file: "skills/legion-architect/SKILL.md",
    has: "`dispatch results --issue <issue>`: each check, every result recorded",
    keep: "What the command prints. It says which roles passed, not that a pass needs both.",
  },
  {
    file: "skills/legion-architect/references/design-gate.md",
    has: "The human approves both documents in one request",
    cut: "`the implementer and the tester record a result against each check with no ci_check, and` and `, from the implementer and from the tester` give each such check a result of both roles. The label deletes those words.",
  },
  {
    file: "skills/legion-architect/references/design-gate.md",
    has: "`dispatch results` derives a result from the named run",
    keep: "What CI's job records. It names no result of a role.",
    at: "base",
  },
  {
    file: "skills/legion-architect/references/design-gate.md",
    has: "Each step records its own document",
    keep: "The documents of the design gate. It names no result.",
  },
  {
    file: "skills/legion-architect/references/wake-routing.md",
    has: "A `reviewer` notice whose GitHub review is `CHANGES_REQUESTED`",
    keep: "The route of a request for changes. It names no result of a check.",
  },
  {
    file: "skills/legion-architect/references/wake-routing.md",
    has: 'A `tester` notice with `verdict: "fail"`',
    keep: "The architect's routing of a tester's fail. It forbids a role to record another's result, and does not say that a pass needs both roles.",
  },
  {
    file: "skills/legion-controller/references/escalation.md",
    has: "It publishes one message to each role's own topic",
    keep: "The controller's round before an update. It names no result.",
  },
  {
    file: "skills/legion-controller/references/escalation.md",
    has: "Past the bound no answer holds the round",
    keep: "The controller's round before an update. It names no result.",
  },
  {
    file: "skills/legion-controller/references/escalation.md",
    has: "The round's message tells each role to push its work",
    keep: "The controller's round before an update. It names no result.",
  },
  {
    file: "skills/legion-controller/references/wakes.md",
    has: "So read the issue's record for every entry",
    keep: "The controller's read of a claim. It names no result.",
  },
  {
    file: "skills/legion-architect/references/wake-routing.md",
    has: "it records your own `escalate`",
    keep: "An escalation of the architect's. It names no result of a check.",
  },
  {
    file: "skills/legion-worker/references/pr-body.md",
    has: "Otherwise record your own result for each `before_merge` check before you complete",
    cut: "It asks the tester's own result of each `before_merge` check, half of the rule. The label deletes `record your own result for each before_merge check before you complete, and`.",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "Approval is gated by the `before_merge` checks of the issue's own Verification document at its head",
    cut: "The coordinator's ruling puts the two-role rule in this line. The label deletes ` from the implementer and from the tester`.",
    at: "head",
  },
  {
    file: "skills/legion-worker/references/pr-body.md",
    has: "Approval is gated by the `before_merge` checks of its own Verification document, read at its own head",
    cut: "The coordinator's ruling puts the two-role rule in this line. The label deletes ` from the implementer and from the tester`.",
    at: "head",
  },
];
// The guide lines the coordinator's ruling changes to state INFRA-4303's rule, which each later-fail
// label cuts, in the reviewer's texts and in the architect's alike.
const RULED_LINES_LATER_FAIL: readonly Entry[] = [
  {
    file: "skills/legion-architect/SKILL.md",
    has: "The checks are the gate every later role reads",
    cut: "The coordinator's ruling puts the rule in this line. The label deletes `; a pass and a fail at one open head leave the check not known, which the reviewer refuses at once, requesting changes naming the check, with no wait for a later result`.",
    at: "head",
  },
  {
    file: "skills/legion-architect/SKILL.md",
    has: "A pass and a fail at any open head make the check not known, whoever recorded them",
    cut: "The coordinator's ruling puts the architect's rule in this line. The label deletes it.",
    at: "head",
  },
  {
    file: "skills/legion-architect/SKILL.md",
    has: "A `ci_check` run still going, or GitHub unread, is waited for and moves nothing back",
    cut: "The coordinator's ruling takes from the architect's wait a check that a pass and a fail make not known. The label deletes `, except where a pass and a fail at an open head make the check not known: then send the issue back at once, naming the check`.",
    at: "head",
  },
  {
    file: "skills/legion-architect/references/acceptance.md",
    has: "A pass and a fail at one open head make a check not known: send the issue back at once",
    cut: "The coordinator's ruling puts the architect's rule in the last round. The label deletes it.",
    at: "head",
  },
  {
    file: "skills/legion-architect/references/design-gate.md",
    has: "The human approves both documents in one request",
    cut: "The coordinator's ruling puts the rule in this line. The label deletes `; a pass and a fail at one open head leave the check not known, which the reviewer refuses at once, requesting changes naming the check, with no wait for a later result`.",
    at: "head",
  },
  {
    file: "skills/legion-retro/SKILL.md",
    has: "Every `before_merge` check of the issue reads yes on the line of the approved head",
    cut: "The coordinator's ruling puts the rule in the retro's gate. The label deletes `, and a pass and a fail at one head leave it not known`.",
    at: "head",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "A pass and a fail at one open head leave the check not known, whoever recorded them",
    cut: "The coordinator's ruling puts the reviewer's rule in this line. The label deletes it.",
    at: "head",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "While that run has not concluded, wait for it by its name, and do not approve or request changes for that check",
    cut: "The coordinator's ruling takes from the reviewer's wait a head that a pass and a fail make not known. The label deletes `, unless the line of the head being reviewed reads not known because a pass and a fail count there, which is the refusal above`.",
    at: "head",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "Approval is gated by the `before_merge` checks of the issue's own Verification document at its head",
    cut: "The coordinator's ruling puts the rule in this line. The label deletes `, and a pass and a fail at one open head leave a check not known, which the reviewer refuses at once, requesting changes naming the check, with no wait for a later result`.",
    at: "head",
  },
  {
    file: "skills/legion-worker/references/pr-body.md",
    has: "Approval is gated by the `before_merge` checks of its own Verification document, read at its own head",
    cut: "The coordinator's ruling puts the rule in this line. The label deletes `, and a pass and a fail at one open head leave a check not known, which the reviewer refuses at once, requesting changes naming the check, with no wait for a later result`.",
    at: "head",
  },
];
const ENTRIES_LATER_FAIL: readonly Entry[] = [
  {
    file: "packages/daemon/internal/prompts/roles/core/reviewer.md",
    has: "That answer leaves out a pass at another commit",
    cut: "T5.12's second pair. The label restores `, and on a check with no ci_check a fail recorded after a pass leaves that pass standing until someone retracts it`.",
  },
  {
    file: "packages/daemon/internal/prompts/roles/core/reviewer.md",
    has: "A pass and a fail from CI at one head leave no pass standing there.",
    cut: "T5.12 replaces it with its third pair. The label restores it.",
    at: "base",
  },
  {
    file: "packages/daemon/internal/prompts/roles/core/reviewer.md",
    has: "A pass and a fail at one head, from any role, leave that head not known, and a later pass there does not end it: refuse the approval at once, naming the check, and do not wait for a later result, for a CI run still going or for GitHub.",
    cut: "T5.12's third pair, which item 13 makes a refusal at once, with no wait for a CI run or for GitHub. The label restores `A pass and a fail from CI at one head leave no pass standing there.`",
    at: "head",
  },
  {
    file: "packages/daemon/internal/prompts/roles/core/reviewer.md",
    has: "Until that run concludes, wait for it by its name, unless that head's line reads not known because a pass and a fail count there.",
    cut: "Item 13 takes from the wait a head that reads not known for a pass and a fail. The label deletes `, unless that head's line reads not known because a pass and a fail count there`.",
    at: "head",
  },
  {
    file: "packages/daemon/internal/prompts/roles/reviewer.md",
    has: "Wait for a `ci_check` run still going or unread, unless the head's line reads not known because a pass and a fail count there, which is a request for changes naming the check, at once.",
    cut: "Item 13 takes from the wait a head that reads not known for a pass and a fail. The label deletes `, unless the head's line reads not known because a pass and a fail count there, which is a request for changes naming the check, at once`.",
    at: "head",
  },
  {
    file: "packages/daemon/internal/prompts/roles/reviewer.md",
    has: "A pass and a fail at any open head make the check not known: request changes at once, naming the check, and do not wait for a later result, a CI run still going or GitHub that Dispatch could not read.",
    cut: "INFRA-4303 moved the reviewer's no-wait rule into its role prompt. The label deletes it.",
    at: "head",
  },
  {
    file: "packages/daemon/internal/prompts/roles/tester.md",
    has: "A pass and a fail at any open head make the check not known; count it as a test failure",
    cut: "The tester's form of the rule, in a text that the reviewer reads. INFRA-4303 puts it in the tester's role prompt, and the label deletes it.",
    at: "head",
  },
  {
    file: "skills/legion-retro/SKILL.md",
    has: "A `ci_check` check has no pass standing at that head when",
    keep: "A pass and a fail from CI on a check carrying `ci_check`. The sentence that the label restores gives the other rule for a check with none, which the scenario's `k1` is.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "a retraction does not lift the rule",
    keep: "A retraction and the field `ci_check`. It says nothing of a pass and a fail at one head.",
    at: "base",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    fenced: true,
    keep: "A line of a fenced block: a format, a template or a command, which states no rule.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "That read is neither a pass nor a fail",
    keep: "The case `github_unreadable`.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "It is an entry in `failures` when no pass of it stands at that head",
    keep: "A pass and a fail from CI on a check carrying `ci_check`, as above.",
    at: "base",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "That answer leaves out a pass at another commit",
    keep: "A pass at another commit, under an older wording, or retracted. It says nothing of a pass and a fail at one head.",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "On a check with no `ci_check`, a fail recorded after a pass",
    cut: "T5.12 deletes it. The label restores it.",
    at: "base",
  },
  {
    file: "packages/daemon/internal/prompts/roles/reviewer.md",
    has: "A pass and a fail at one head leave it not known, whoever recorded them, and a later pass there does not end it: refuse the approval",
    cut: "T5.13 adds it. The label deletes it.",
    at: "head",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "A `wake_at` that no look follows is refused",
    keep: "The looks of the production check after the close.",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "A `pass` or `fail` there closes the check as any look's would",
    keep: "The looks of the production check after the close.",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "`pass` or `fail` closes the check: your claim is retired",
    keep: "The looks of the production check after the close.",
  },
  {
    file: "skills/legion-worker/references/pr-body.md",
    fenced: true,
    keep: "A line of a fenced block: a format, a template or a command, which states no rule.",
  },
  {
    file: "skills/legion-worker/references/pr-body.md",
    has: "A `ci_check` check with no pass standing at the head",
    keep: "A pass and a fail from CI on a check carrying `ci_check`, as above.",
    at: "base",
  },
  {
    file: "skills/thermonuclear-deep-review/SKILL.md",
    has: "Order, not coverage: after a pipeline sanitizes",
    keep: "A question of a security review. It names no check of Dispatch.",
  },
  {
    file: "skills/thermonuclear-deep-review/SKILL.md",
    has: "`untrusted-input`",
    keep: "A row of a security review's table. It names no check of Dispatch.",
  },
  {
    file: "packages/daemon/internal/prompts/roles/tester.md",
    has: "A `wake_at` no look follows is refused",
    keep: "The looks of a check after the close. It says nothing of a pass and a fail at one head.",
  },
  {
    file: "skills/legion-architect/SKILL.md",
    has: "Act on delivered wakes and the live artifacts they name",
    keep: "Wakes. It names no check of Dispatch.",
  },
  {
    file: "skills/legion-architect/SKILL.md",
    has: "Open an ordinary ask on the root issue itself, never a child",
    keep: "Asks to a person. It names no check of Dispatch.",
  },
  {
    file: "skills/legion-architect/SKILL.md",
    has: "A pass stands is the check's own answer",
    keep: "A pass at another commit, under an older wording, or retracted, is no pass. It says nothing of a pass and a fail at one head.",
  },
  {
    file: "skills/legion-architect/SKILL.md",
    has: "On a check with no `ci_check`, a fail recorded after a pass",
    cut: "The architect's rule, in a text that the reviewer reads (the session's list of skills names `legion-architect`, and `findings.md:44` links it). T5.12 deletes it, and the label restores it.",
    at: "base",
  },
  {
    file: "skills/legion-architect/references/acceptance.md",
    has: "That registration's approval request waits with the person",
    keep: "The wait of an approval request. It names no check of Dispatch.",
  },
  {
    file: "skills/legion-architect/references/design-gate.md",
    has: "The human approves both documents in one request",
    keep: "The results that the roles record and the reviewer's gate. It says nothing of a pass and a fail at one head.",
    at: "base",
  },
  {
    file: "skills/legion-architect/references/design-gate.md",
    has: "Under `Design gate steps: none.` write the spec",
    keep: "No wait for an approval of the design. It names no check of Dispatch.",
  },
  {
    file: "skills/legion-controller/references/consolidation.md",
    has: "Finding issues that are the same work, or that fail for one cause",
    keep: "Issues that fail for one cause. It names no check of Dispatch.",
  },
  {
    file: "skills/legion-controller/references/wakes.md",
    has: "A failed claim holds no role",
    keep: "A claim that failed. It names no check of Dispatch.",
    at: "head",
  },
  {
    file: "skills/legion-worker/references/withheld-capability.md",
    has: "Moving exactly these writes off that filesystem",
    keep: "A condition that an install fails. It names no check of Dispatch.",
  },
  {
    file: "skills/simplify-settled-diff/SKILL.md",
    has: "Where a dispatch fails for a reason that survives correcting the call",
    keep: "A subagent that fails. It names no check of Dispatch.",
  },
  ...RULED_LINES_LATER_FAIL,
];
const ENTRIES_ARCHITECT_LATER_FAIL: readonly Entry[] = [
  {
    file: "packages/daemon/internal/prompts/roles/architect-root.md",
    has: "The person's answer, resolution or reply on any ask of the root",
    keep: "A person's answer gives a task. It names no check of Dispatch.",
  },
  {
    file: "packages/daemon/internal/prompts/roles/architect-root.md",
    has: "To give a check to CI, write a new check block under a new id",
    keep: "A check given to CI, and a retraction. It says nothing of a pass and a fail at one head.",
    at: "head",
  },
  {
    file: "packages/daemon/internal/prompts/roles/tester.md",
    has: "A pass and a fail at any open head make the check not known; count it as a test failure",
    cut: "The tester's form of the rule, in a text that the architect reads. INFRA-4303 puts it in the tester's role prompt, and the label deletes it.",
    at: "head",
  },
  {
    file: "skills/legion-architect/SKILL.md",
    has: "Act on delivered wakes and the live artifacts they name",
    keep: "Wakes. It names no check of Dispatch.",
  },
  {
    file: "skills/legion-architect/SKILL.md",
    has: "Open an ordinary ask on the root issue itself, never a child",
    keep: "Asks to a person. It names no check of Dispatch.",
  },
  {
    file: "skills/legion-architect/SKILL.md",
    has: "A pass stands is the check's own answer",
    keep: "A pass at another commit, under an older wording, or retracted, is no pass. It says nothing of a pass and a fail at one head.",
  },
  {
    file: "skills/legion-architect/SKILL.md",
    has: "On a check with no `ci_check`, a fail recorded after a pass",
    cut: "T5.12 deletes it. The label restores it.",
    at: "base",
  },
  {
    file: "skills/legion-architect/references/acceptance.md",
    has: "That registration's approval request waits with the person",
    keep: "The wait of an approval request. It names no check of Dispatch.",
  },
  {
    file: "skills/legion-architect/references/design-gate.md",
    has: "The human approves both documents in one request",
    keep: "The results that the roles record and the reviewer's gate. It says nothing of a pass and a fail at one head.",
    at: "base",
  },
  {
    file: "skills/legion-architect/references/design-gate.md",
    has: "Under `Design gate steps: none.` write the spec",
    keep: "No wait for an approval of the design. It names no check of Dispatch.",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "That answer leaves out a pass at another commit",
    keep: "A pass at another commit, under an older wording, or retracted. It says nothing of a pass and a fail at one head.",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "On a check with no `ci_check`, a fail recorded after a pass",
    cut: "The reviewer's rule, in a text that the architect reads (`go/architect-common.md` links `merge-gate.md`). T5.12 deletes it, and the label restores it.",
    at: "base",
  },
  {
    file: "packages/daemon/internal/prompts/roles/architect-root.md",
    has: "A pass and a fail at any open head make the check not known: send the issue back at once, naming the check, do not accept it, and do not wait for a later result, a CI run still going or GitHub that Dispatch could not read.",
    cut: "INFRA-4303 moved the architect's no-wait rule into its role prompt. The label deletes it.",
    at: "head",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "A `wake_at` that no look follows is refused",
    keep: "The looks of the production check after the close.",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "A `pass` or `fail` there closes the check as any look's would",
    keep: "The looks of the production check after the close.",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "`pass` or `fail` closes the check: your claim is retired",
    keep: "The looks of the production check after the close.",
  },
  {
    file: "packages/daemon/internal/prompts/roles/tester.md",
    has: "A `wake_at` no look follows is refused",
    keep: "The looks of a check after the close. It says nothing of a pass and a fail at one head.",
  },
  {
    file: "skills/legion-controller/references/consolidation.md",
    has: "Finding issues that are the same work, or that fail for one cause",
    keep: "Issues that fail for one cause. It names no check of Dispatch.",
  },
  {
    file: "skills/legion-controller/references/wakes.md",
    has: "A failed claim holds no role",
    keep: "A claim that failed. It names no check of Dispatch.",
    at: "head",
  },
  {
    file: "skills/legion-retro/SKILL.md",
    has: "A `ci_check` check has no pass standing at that head when",
    keep: "A pass and a fail on a check carrying `ci_check`. The sentence that the label restores in `legion-architect/SKILL.md:72` gives the rule for a check with none, which the scenario's `k1` is.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "a retraction does not lift the rule",
    keep: "A retraction and the field `ci_check`. It says nothing of a pass and a fail at one head.",
    at: "base",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    fenced: true,
    keep: "A line of a fenced block: a format, a template or a command, which states no rule.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "That read is neither a pass nor a fail",
    keep: "The case `github_unreadable`.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "It is an entry in `failures` when no pass of it stands at that head",
    keep: "A pass and a fail from CI on a check carrying `ci_check`, as above.",
    at: "base",
  },
  {
    file: "skills/legion-worker/references/pr-body.md",
    fenced: true,
    keep: "A line of a fenced block: a format, a template or a command, which states no rule.",
  },
  {
    file: "skills/legion-worker/references/pr-body.md",
    has: "A `ci_check` check with no pass standing at the head",
    keep: "A pass and a fail from CI on a check carrying `ci_check`, as above.",
    at: "base",
  },
  {
    file: "skills/legion-worker/references/withheld-capability.md",
    has: "Moving exactly these writes off that filesystem",
    keep: "A condition that an install fails. It names no check of Dispatch.",
  },
  {
    file: "skills/simplify-settled-diff/SKILL.md",
    has: "Where a dispatch fails for a reason that survives correcting the call",
    keep: "A subagent that fails. It names no check of Dispatch.",
  },
  {
    file: "skills/thermonuclear-deep-review/SKILL.md",
    has: "Order, not coverage: after a pipeline sanitizes",
    keep: "A question of a security review. It names no check of Dispatch.",
  },
  {
    file: "skills/thermonuclear-deep-review/SKILL.md",
    has: "`untrusted-input`",
    keep: "A row of a security review's table. It names no check of Dispatch.",
  },
  ...RULED_LINES_LATER_FAIL,
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
