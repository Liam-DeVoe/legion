// rule-search.ts: every sentence of an ablate label's rule in the texts its role reads, each one
// classified. A label measures its rule only if no text its role reads still states that rule
// (rig.sh's header: "A new scenario is an instrument only once an ablate arm has scored lower on
// it"), so a label's list of cuts comes from this search, never from a hand-made list.
//
//   bun rule-search.ts <checkout> <base|head> <label>...
//   bun rule-search.ts <checkout> texts <reviewer|architect>
//
// <checkout> is a tree of cybermonklabs/legion. <base> says it is #4230 before T5.12 of its plan
// addendum (621d1378273e), <head> that it holds T5.12's changes: an
// entry marked for one of the two is not expected in the other. `texts` lists the texts of a role,
// each with its depth and the chain of links that reaches it first.
//
// The texts of a role are the closure of its links. Its entry texts are its prompt parts, as
// prompts.Compose gives them (packages/daemon/internal/prompts/prompts.go, Compose), and its
// skill. Then each file that a link in a text names is a text too, and so on, until no new text
// appears. The depth of a text is the fewest links from an entry text. A link is one of three:
//   - `skill://<name>` is skills/<name>/SKILL.md, and `skill://<name>/<path>` is skills/<name>/<path>;
//   - a relative `](<file>.md)` is read from the linking file's folder;
//   - a path in backticks that ends `.md` and names a file of the checkout, read from the linking
//     file's folder, from its skill's folder (skills/<name>/), or, for a path that starts
//     `skills/` or `packages/daemon/internal/prompts/`, from the checkout's root.
// A file of the first two kinds that the checkout does not hold is a FAULT. A path in backticks
// that names no file is no link (`plan.md`, `AGENTS.md`). The session's list of skills is a link
// too: Oh My Pi names each installed skill to the session, and the two plugins install every folder
// of skills/ (scripts/pi-plugin-prepack.sh), so each skills/<name>/SKILL.md is at depth 1 at most.
// A text that no chain of links reaches, such as another role's prompt part, is not read. The rig's
// architect is a tree's root architect (architect-root.md).
//
// Each text is cut into sentences: a heading, a list item, a table row, a line of a fenced block,
// or a paragraph, each split after `.`, `?` or `!` where the next word starts with a capital, a
// backtick or a quote. A sentence that one of the label's patterns matches is a hit. Each hit must
// match exactly one entry of the label: `cut` (the label deletes it or undoes T5.12's change of it)
// or `keep` (the reason it does not state the rule). Each entry that applies to the tree must match
// exactly one hit. A `fenced` entry is the one exception: it keeps each hit in a fenced block of its
// file (a format, a template or a command, which states no rule), and it may match none.
//
// Exit 0: every hit is classified and every entry matched once. Exit 1: one FAULT line for each
// hit with no entry, hit with two, entry with no hit or two, missing text, and for a run that read
// no text or found no hit (it could not see). Exit 2: a usage error.

import { existsSync, readdirSync, readFileSync } from "node:fs";
import path from "node:path";

type Tree = "base" | "head";
type Role = "reviewer" | "architect";

interface Entry {
  /** The text, relative to the checkout. */
  readonly file: string;
  /** Words of the sentence, with runs of white space as one space; absent on a `fenced` entry. */
  readonly has?: string;
  /** Keeps each hit in a fenced block of the file: a format, a template or a command. It may match
   * any number of hits, none included. */
  readonly fenced?: true;
  /** Present only on a sentence that T5.12 adds (head) or removes (base). */
  readonly at?: Tree;
  /** What the label does to this sentence. */
  readonly cut?: string;
  /** Why the sentence does not state the label's rule. */
  readonly keep?: string;
}

interface Rule {
  readonly role: Role;
  /** The rule the label removes, in one sentence. */
  readonly rule: string;
  readonly patterns: readonly RegExp[];
  readonly entries: readonly Entry[];
}

const PROMPTS = "packages/daemon/internal/prompts";
const ROLE_TEXTS: Record<Role, { readonly parts: readonly string[]; readonly skill: string }> = {
  reviewer: {
    parts: [
      "roles/core/common.md",
      "roles/core/reviewer.md",
      "roles/mechanics/headless.md",
      "roles/reviewer.md",
      "go/reviewer.md",
      "go/worker-common.md",
    ].map((part) => `${PROMPTS}/${part}`),
    skill: "skills/legion-worker/SKILL.md",
  },
  architect: {
    parts: ["roles/architect-root.md", "go/architect-root.md", "go/architect-common.md"].map(
      (part) => `${PROMPTS}/${part}`
    ),
    skill: "skills/legion-architect/SKILL.md",
  },
};

const LATER_FAIL: readonly RegExp[] = [
  /pass and (a )?fail|fail and (a )?pass|after a pass|later (fail|pass|result)|fail recorded after/i,
  /retract|not known|do not wait|no wait\b|wait for a later/i,
  /\bfail(s|ed)?\b[^.]{0,80}\bpass(es)?\b|\bpass(es)?\b[^.]{0,80}\bfail(s|ed)?\b/i,
];

const ENTRIES_HEAD: readonly Entry[] = [
  {
    file: "packages/daemon/internal/prompts/roles/core/reviewer.md",
    has: "Verify as forge facts, never from reports",
    cut: "T5.12's first pair sends the reviewer to `that head's line`. The label restores `prints under each check`.",
  },
  {
    file: "packages/daemon/internal/prompts/roles/core/reviewer.md",
    has: "That answer leaves out a pass at another commit",
    keep: "The rule of a fail recorded after a pass, which `ablate-later-fail` owns. It names no line to read.",
    at: "base",
  },
  {
    file: "packages/daemon/internal/prompts/roles/core/reviewer.md",
    has: "A pass and a fail from CI at one head leave no pass standing there.",
    keep: "The rule of a fail recorded after a pass, which `ablate-later-fail` owns. It names no line to read.",
    at: "base",
  },
  {
    file: "packages/daemon/internal/prompts/roles/reviewer.md",
    has: "Approval is refused while any `before_merge` check",
    cut: "T5.12's pair of `reviewer.md:12` sends the reviewer to the line of that head. The label restores `the check's own answer`.",
  },
  {
    file: "packages/daemon/internal/prompts/roles/reviewer.md",
    has: "Your approval is gated by the `before_merge` checks",
    cut: "T5.12's pair of `reviewer.md:38`. The label restores `under the check's own line saying whether a pass stands and from which roles`, and of the base's `read at this issue's own head` and `at that head` nothing: with two open pull requests of one issue, a reviewer reads `this issue's own head` as the head it reviews.",
  },
  {
    file: "packages/daemon/internal/prompts/roles/core/reviewer.md",
    has: "The gate is this issue's own checks at this issue's own head",
    cut: "A reviewer of one of two open pull requests reads `this issue's own head` as the head it reviews (`sibling-fails`, ablate-head-2: \"My role text says the gate is this issue's own checks at its own head\"). The label deletes ` at this issue's own head`.",
  },
  {
    file: "packages/daemon/internal/prompts/roles/tester.md",
    has: "Your issue is proved by its own checks, read at its own head",
    cut: "Where the gate reads, in the words the core sentence above uses. The label deletes `, read at its own head`.",
  },
  {
    file: "skills/legion-worker/references/pr-body.md",
    has: "Approval is gated by the `before_merge` checks of its own Verification document, read at its own head",
    cut: "Where approval reads, in the same words. The label deletes `, read at its own head`.",
  },
  {
    file: "skills/legion-worker/references/pr-body.md",
    has: "A root's own checks are proven at its own head",
    keep: "A root against its child, after the sentence above. The case has no child.",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "A child that carries part of a root's requirement proves it with a check on the child's own Verification document",
    keep: "A child's own check at the child's own head. The case has no child.",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "A root's own checks are proven at the root's own head",
    keep: "A root against its child. The case has no child.",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "Approval is gated by the `before_merge` checks of the issue's own Verification document at its head",
    keep: "The architect's acceptance of a root against its child, which no reviewer gives.",
  },
  {
    file: "packages/daemon/internal/prompts/go/worker-common.md",
    has: "This read is of the pull request's own head and of no check",
    keep: "GitHub's own checks of the head, which gate the merge apart from the checks of Dispatch.",
  },
  {
    file: "skills/legion-worker/SKILL.md",
    has: "This read is of the pull request's own head and of no check",
    keep: "GitHub's own checks of the head, which gate the merge apart from the checks of Dispatch.",
  },
  {
    file: "skills/legion-retro/SKILL.md",
    has: "A check with no pass standing at the head goes back as step 1 says.",
    keep: "The retro's own move back. It names no line.",
  },
  {
    file: "skills/legion-retro/SKILL.md",
    has: "**Read the evidence:**",
    keep: "It names the command, and no line of it.",
  },
  {
    file: "skills/legion-retro/SKILL.md",
    has: "and the pull request body's `Checks` block carries each of those lines",
    cut: "T5.12's change of `legion-retro/SKILL.md:49` names the line of the approved head. The label restores `A pass stands for every ... at the approved head`.",
  },
  {
    file: "skills/legion-retro/SKILL.md",
    has: "A check carrying `ci_check` is CI's: a pass from CI is the pass it needs",
    keep: "A check carrying `ci_check`, whose rule `ablate-ci-pass` owns. It names no line.",
  },
  {
    file: "skills/legion-retro/SKILL.md",
    has: "A `ci_check` check has no pass standing at that head when",
    cut: "T5.12's change of `legion-retro/SKILL.md:57` names the line of that head. The label restores `(its A pass stands: line)`, and keeps `a pass and a fail are there`.",
  },
  {
    file: "skills/legion-retro/SKILL.md",
    has: "When any other check has no pass standing at that head",
    keep: "The retro's move back. It names no line.",
  },
  {
    file: "skills/legion-worker/SKILL.md",
    has: "It arrives as a message on your role topic",
    keep: "The daemon's line in a hook's message. It names no check of Dispatch.",
  },
  {
    file: "skills/legion-worker/SKILL.md",
    has: "The step applies once the branch has an open pull request",
    keep: "The task's `Pull request:` line and GitHub's head. It names no check of Dispatch.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "reads the named check run at the head of",
    keep: "What Dispatch reads for a check carrying `ci_check`. The case `sibling-fails` has none, and the sentence names no line to judge by.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "The command prints the derived result",
    keep: "What the command prints, which the run's Dispatch prints under every label.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "Then it prints one line for each head, each with the line for that pull request's body.",
    keep: "What the command prints, which the run's Dispatch prints under every label.",
    at: "head",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    fenced: true,
    keep: "A line of a fenced block: a format, a template or a command, which states no rule.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "An issue with no pull request (`no_head`)",
    keep: "The case of an issue with no pull request. The scenarios have two.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "Read `dispatch results --issue <issue>` first.",
    keep: "The tester's first read. It names no line.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "It is an entry in `failures` when no pass of it stands at that head",
    cut: "T5.12's change of `ci-checks.md:68` names the line of that head. The label restores `(its A pass stands: line)`.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "It copies the `Pull request line` that `dispatch results` prints",
    keep: "The body's `Checks` line: which line the implementer copies into the body, under every label. It judges no check.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "The next role that writes the body reads `dispatch results` again",
    keep: "The body's `Checks` line.",
  },
  {
    file: "skills/legion-worker/references/findings.md",
    has: "`envoy_publish`'s own answer tells you",
    keep: "An answer of `envoy_publish`. It names no check of Dispatch.",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "- The reviewer reads CI on GitHub and verifies the `Threads` facts",
    keep: "It names the command, and no line of it.",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "A required item with no run at the head at all",
    keep: "GitHub's own checks of the head, which gate the merge apart from the checks of Dispatch.",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "Approval is refused while any `before_merge` check",
    cut: "T5.12's first pair of `merge-gate.md:118`. The label restores `under the check's own line saying whether a pass stands and from which roles`.",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "On a check with no `ci_check`, a fail recorded after a pass",
    keep: "The rule of a fail recorded after a pass, which `ablate-later-fail` owns. It names no line to read.",
    at: "base",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "A check carrying `ci_check` is CI's: the result that",
    cut: "T5.12's pair of `merge-gate.md:119`. The label restores `through the check's A pass stands: line`.",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "A check whose `run` is a whole test suite and has no pass standing",
    keep: "A check whose words are wrong. It names no line.",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "One that has a pass standing at the head is a finding",
    keep: "A check whose words are wrong. It names no line.",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "Submit **one review per round**",
    keep: "The review's footer names the head. It names no check of Dispatch.",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    fenced: true,
    keep: "A line of a fenced block: a format, a template or a command, which states no rule.",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "The daemon moves the issue once all three are in",
    keep: "GitHub's answer on the approved head. It names no check of Dispatch.",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "- Between the reviewer's approval and retro",
    keep: "The architect's step after the review.",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "Wait for what GitHub waits for",
    keep: "The merger's wait on GitHub. It names no check of Dispatch.",
  },
  {
    file: "skills/legion-worker/references/pr-body.md",
    fenced: true,
    keep: "A line of a fenced block: a format, a template or a command, which states no rule.",
  },
  {
    file: "skills/legion-worker/references/pr-body.md",
    has: "`dispatch results --issue <issue>` prints each check with the results",
    keep: "What the command prints, which the run's Dispatch prints under every label.",
  },
  {
    file: "skills/legion-worker/references/pr-body.md",
    has: "- **The implementer records a result for every `before_merge` check",
    keep: "The implementer's step. It names no line.",
  },
  {
    file: "skills/legion-worker/references/pr-body.md",
    has: "The `implement` handoff carries none",
    keep: "Where the tester reads the results. It names no line.",
  },
  {
    file: "skills/legion-worker/references/pr-body.md",
    has: "- **The tester judges the implementer's results and records its own.**",
    keep: "The tester's step. It names no line.",
  },
  {
    file: "skills/legion-worker/references/pr-body.md",
    has: "A `ci_check` check with no pass standing at the head",
    cut: "T5.12's pair of `pr-body.md:187`. The label restores `(its A pass stands: line)`.",
  },
  {
    file: "skills/legion-worker/references/pr-body.md",
    has: "A `before_merge` check the implementer left with no pass standing",
    keep: "The tester's rule on the implementer's result. It names no line.",
  },
  {
    file: "skills/legion-worker/references/review-threads.md",
    has: "The implementer records each thread it answered",
    keep: "Review threads. It names no check of Dispatch.",
  },
  {
    file: "skills/outside-message/SKILL.md",
    has: "Only the operator's own answer names such a project",
    keep: "An operator's answer. It names no check of Dispatch.",
  },
  {
    file: "packages/daemon/internal/prompts/roles/tester.md",
    has: "Read the issue's checks with `dispatch results --issue <issue>`",
    keep: "The tester's read of the checks. It names no line.",
  },
  {
    file: "packages/daemon/internal/prompts/roles/tester.md",
    has: "is a test failure, not a gap for you to fill",
    cut: "T5.12's change of `tester.md:7` names the line of the head the tester tests. The label restores `with no pass standing from the implementer at the head you are testing`.",
  },
  {
    file: "packages/daemon/internal/prompts/roles/tester.md",
    has: "Record it with `dispatch handoff-write --issue <issue> --phase test",
    keep: "The tester's handoff record. It names no line of a check.",
  },
  {
    file: "skills/legion-architect/SKILL.md",
    has: "Check each claim against the code as fetched now",
    keep: "The code a claim is read against. It names no check of Dispatch.",
  },
  {
    file: "skills/legion-architect/SKILL.md",
    has: "The checks are the gate every later role reads",
    keep: "The architect's account of the gate: a pass standing at the head. It names neither the line of a head nor the check's answer, as `roles/architect.md:105` does not.",
  },
  {
    file: "skills/legion-architect/SKILL.md",
    has: "`dispatch results --issue <issue>`: each check, every result recorded",
    keep: "The architect's read, which is the answer of the whole check (the spec's Design). It sends no reader to the line of a head.",
  },
  {
    file: "skills/legion-architect/SKILL.md",
    has: "Accept only when a pass stands for every `before_merge` check of this issue",
    keep: "The architect's acceptance. It names no line.",
  },
  {
    file: "skills/legion-architect/SKILL.md",
    has: "A pass stands is the check's own answer",
    keep: "The architect reads the answer of the whole check, as the spec's Design says. It sends no reader to the line of a head.",
  },
  {
    file: "skills/legion-architect/SKILL.md",
    has: "On a check with no `ci_check`, a fail recorded after a pass",
    keep: "The rule of a fail after a pass, which `ablate-later-fail` owns. It names no line.",
    at: "base",
  },
  {
    file: "skills/legion-architect/SKILL.md",
    has: "When no pass stands for a check at that head, or a result contradicts",
    keep: "The architect's move back. It names no line.",
  },
  {
    file: "skills/legion-architect/references/acceptance.md",
    has: "While the approved head does not carry those merges",
    keep: "The architect's wait for the merges of children. It names no line.",
  },
  {
    file: "skills/legion-architect/references/acceptance.md",
    has: "Read each `ci_check` check's results from CI in `dispatch results`",
    keep: "The architect's read of CI's results. It names no line.",
  },
  {
    file: "skills/legion-architect/references/design-gate.md",
    has: "The human approves both documents in one request",
    cut: "T5.12's change of `design-gate.md:27-28` sends the reviewer to the line of the head. The label restores `which is the check's own answer`.",
  },
  {
    file: "skills/legion-architect/references/design-gate.md",
    has: "`dispatch results` derives a result from the named run",
    keep: "What Dispatch derives for a check carrying `ci_check`. It names no line.",
  },
  {
    file: "skills/legion-architect/references/design-gate.md",
    has: "The exceptions are the version a human's own answer",
    keep: "The approval of a document. It names no check of Dispatch.",
  },
  {
    file: "skills/legion-architect/references/wake-routing.md",
    has: "One whose `version` is the version a human's own answer",
    keep: "The approval of a document. It names no check of Dispatch.",
  },
  {
    file: "skills/legion-architect/references/wake-routing.md",
    has: 'A `tester` notice with `verdict: "fail"`',
    keep: "The architect's routing of a tester's fail. It names no line.",
  },
  {
    file: "skills/legion-controller/references/weekly-audit.md",
    has: "Report, per filer, how many creates forced past a refusal",
    keep: "A line of the controller's report. It names no check of Dispatch.",
  },
  {
    file: "skills/legion-retro/references/pr-body-content.md",
    has: "The parentheses keep `pipefail` to this line",
    keep: "A line of a shell. It names no check of Dispatch.",
  },
  {
    file: "skills/thermonuclear-code-quality/SKILL.md",
    has: "The first line is the verdict, with the short sha of the head you reviewed",
    keep: "The verdict line of a code-quality review. It names no check of Dispatch.",
  },
  {
    file: "skills/thermonuclear-code-quality/SKILL.md",
    has: "Before you report a finding, quote its line at the reviewed head.",
    keep: "A line of code. It names no check of Dispatch.",
  },
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
  },
  {
    file: "skills/legion-worker/references/conflicts-and-rewrites.md",
    has: "Where each role gets its two heads",
    keep: "Fingerprints of heads. It names no check of Dispatch.",
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
    cut: "`the implementer and the tester record a result against each check with no ci_check, and` gives each such check a result of both roles. The label deletes those words.",
  },
  {
    file: "skills/legion-architect/SKILL.md",
    has: "`dispatch results --issue <issue>`: each check, every result recorded",
    keep: "What the command prints. It says which roles passed, not that a pass needs both.",
  },
  {
    file: "skills/legion-architect/references/design-gate.md",
    has: "The human approves both documents in one request",
    cut: "`the implementer and the tester record a result against each check with no ci_check, and` gives each such check a result of both roles. The label deletes those words.",
  },
  {
    file: "skills/legion-architect/references/design-gate.md",
    has: "`dispatch results` derives a result from the named run",
    keep: "What CI's job records. It names no result of a role.",
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
];
const ENTRIES_CI_PASS: readonly Entry[] = [
  {
    file: "packages/daemon/internal/prompts/go/reviewer.md",
    has: "One `completed` as `cancelled`, or absent from the list",
    keep: "GitHub's own checks of the head, which gate the merge apart from the checks of Dispatch.",
  },
  {
    file: "packages/daemon/internal/prompts/go/reviewer.md",
    has: "The run and job are among the head's runs",
    keep: "GitHub's own checks of the head, which gate the merge apart from the checks of Dispatch.",
  },
  {
    file: "packages/daemon/internal/prompts/go/reviewer.md",
    has: "The round's order: read CI as above",
    keep: "GitHub's own checks of the head, which gate the merge apart from the checks of Dispatch.",
  },
  {
    file: "packages/daemon/internal/prompts/go/reviewer.md",
    has: "So once you have read the head's checks there is nothing of CI",
    keep: "GitHub's own checks of the head, which gate the merge apart from the checks of Dispatch.",
  },
  {
    file: "packages/daemon/internal/prompts/go/worker-common.md",
    has: "A CI settlement that the daemon records",
    keep: "A CI settlement wakes the role. It names no check of Dispatch.",
  },
  {
    file: "packages/daemon/internal/prompts/go/worker-common.md",
    has: "A secret `agent-secrets` lends exists only",
    keep: "A secret's environment. It names no check of Dispatch.",
  },
  {
    file: "packages/daemon/internal/prompts/go/worker-common.md",
    has: "No commit message carries a line or keyword",
    keep: "Commit messages. It names no check of Dispatch.",
  },
  {
    file: "packages/daemon/internal/prompts/go/worker-common.md",
    has: "A check carrying `ci_check` is CI's, so skip it.",
    cut: "It exempts such a check from the roles' results at a new head. The label deletes it.",
  },
  {
    file: "packages/daemon/internal/prompts/roles/core/reviewer.md",
    has: "That answer leaves out a pass at another commit",
    keep: "The rule of a fail after a pass on a check with no `ci_check`, which `ablate-later-fail` owns.",
    at: "base",
  },
  {
    file: "packages/daemon/internal/prompts/roles/core/reviewer.md",
    has: "A check carrying `ci_check` is CI's, and its results from CI stand for both roles",
    cut: "It makes CI's results stand for both roles. The label deletes it.",
  },
  {
    file: "packages/daemon/internal/prompts/roles/core/reviewer.md",
    has: "A pass and a fail from CI at one head leave no pass standing there.",
    keep: "It says when CI's results leave no pass. It gives a pass from CI no standing.",
    at: "base",
  },
  {
    file: "packages/daemon/internal/prompts/roles/core/reviewer.md",
    has: "Until that run concludes, wait for it by its name.",
    cut: "Its `that run` is the CI run of the sentence that the label deletes, and the wait makes that run decide the check. The label deletes it.",
  },
  {
    file: "packages/daemon/internal/prompts/roles/reviewer.md",
    has: "Wait for a `ci_check` run still going or unread",
    keep: "The wait for a named run that has not concluded. The run of `ci-only` has concluded, and the sentence does not say that its pass is the evidence.",
  },
  {
    file: "packages/daemon/internal/prompts/roles/reviewer.md",
    has: "When the checks the base branch requires are green",
    keep: "GitHub's own checks of the head, which gate the merge apart from the checks of Dispatch.",
  },
  {
    file: "packages/daemon/internal/prompts/roles/reviewer.md",
    has: "A `ci_check` check with no pass takes the end-of-round rule above",
    keep: "It sends such a check with no pass to the waits above. It does not say where a pass comes from.",
  },
  {
    file: "skills/agent-secrets/SKILL.md",
    has: "Make every call inside the `agent-secrets NAME -- command` run",
    keep: "A call through the secrets broker. It names no check of Dispatch.",
  },
  {
    file: "skills/legion-retro/SKILL.md",
    has: "A check carrying `ci_check` is CI's: a pass from CI is the pass it needs",
    cut: "The retro's gate takes a pass from CI as the pass. The label deletes it.",
  },
  {
    file: "skills/legion-retro/SKILL.md",
    has: "A `ci_check` check has no pass standing at that head when",
    cut: "It makes CI's results decide such a check. The label deletes it.",
  },
  {
    file: "skills/legion-retro/SKILL.md",
    has: "When any other check has no pass standing at that head",
    cut: "`a check with no ci_check has a unit suite as its only evidence` lets a CI job be the evidence of a check that carries one. The label deletes `, or a check with no ci_check has a unit suite as its only evidence,` and ` or a ci_check check`.",
  },
  {
    file: "skills/legion-worker/SKILL.md",
    has: "A CI settlement that the daemon records for the head",
    keep: "A CI settlement wakes the role. It names no check of Dispatch.",
  },
  {
    file: "skills/legion-worker/SKILL.md",
    has: "A check carrying `ci_check` is CI's, so skip it.",
    cut: "It exempts such a check from the roles' results at a new head. The label deletes it.",
  },
  {
    file: "skills/legion-worker/SKILL.md",
    has: "- **No deferrals.**",
    keep: "The cost of a later round. It names no check of Dispatch.",
  },
  {
    file: "skills/legion-worker/SKILL.md",
    has: "Deployment instructions in your prompt override this skill",
    cut: "`with no ci_check` exempts such a check from the implementer's own result. The label deletes those words.",
  },
  {
    file: "skills/legion-worker/SKILL.md",
    has: "A file's `mtime` proves little",
    keep: "A file's `mtime`. It names no check of Dispatch.",
  },
  {
    file: "skills/legion-worker/SKILL.md",
    has: "A pod replacement takes the `mtime` evidence",
    keep: "A file's `mtime`. It names no check of Dispatch.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "# A check CI proves",
    cut: "The heading says that CI proves the check. The label deletes it.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "Read it when a check of the issue's Verification document carries `ci_check`",
    keep: "When to read the file. It says nothing of a pass.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "A `check` block can carry `ci_check`: the name of the GitHub check run that proves the check at the head.",
    cut: "The label cuts it to `the name of a GitHub check run`.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "Through the documents API, Dispatch refuses a write",
    keep: "The rules of a write of the field. It says nothing of what a run proves.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "A write in the live editor lands",
    keep: "The rules of a write of the field. It says nothing of what a run proves.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "To give such a check to CI, write a new check block",
    keep: "How a check gets the field. It says nothing of what a run proves.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "A check carrying `ci_check` is CI's.",
    cut: "It gives the check to CI. The label deletes it.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "No role runs its work in its own pod, and no role records a result for it",
    cut: "It says that no role can record a result of such a check, so that the two-role rule cannot be met, and that Dispatch takes the CI job's result. The label deletes it.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "A `ci_check` that a person adds or removes in the browser",
    keep: "When an edit of the field takes effect. It says nothing of what a run proves.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "reads the named check run at the head of",
    cut: "It makes the named run's result a result of the check, in place of a role's, as `design-gate.md:53` does. The label deletes it.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "A run of the named job on a push of the change's branch also records its own result",
    cut: "It makes the job's own result a result of the check, in place of a role's, as `design-gate.md:53` does. The label deletes it.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "Then it prints each result that the job recorded",
    keep: "What the command prints, which the run's Dispatch prints under every label.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    fenced: true,
    keep: "A line of a fenced block: a format, a template or a command, which states no rule.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "- `neutral`, `skipped` or `cancelled`: the run concluded and proved nothing.",
    keep: "A case that the command prints.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "Then run and record only the checks that carry no `ci_check`",
    cut: "It exempts such a check from the tester's own results. The label deletes `that carry no ci_check and`.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "A check with `ci_check` needs no pass from the implementer.",
    cut: "It exempts such a check from the implementer's pass. The label deletes it.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "It is an entry in `failures` when no pass of it stands at that head",
    cut: "It makes CI's results decide such a check for the tester. The label deletes it.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "While the named run gives no result, that line is the no-result line.",
    keep: "The body's line while no result exists.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "A named run that has not concluded is no test failure",
    keep: "The wait for a named run that has not concluded. The run of `ci-only` has concluded, and the sentence does not say that its pass is the evidence.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "The reviewer waits for that run by its name",
    keep: "The wait for a named run that has not concluded. The run of `ci-only` has concluded, and the sentence does not say that its pass is the evidence.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "The named run is usually not one the base branch requires",
    keep: "The wait for a named run that has not concluded. The run of `ci-only` has concluded, and the sentence does not say that its pass is the evidence.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "A `ci_check` can name a job that the repository runs on a pull request only on request",
    keep: "Which job the field can name. It says nothing of what a run proves.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "Such a check is proved in CI too, never in the pod.",
    cut: "It says that CI proves the check. The label deletes it.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "Without the label, such a job is at the head and reads `skipped`",
    cut: "It makes the job's run decide the check. The label deletes it.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "The implementer adds that label and then pushes",
    cut: "`the run at that head is the check's proof`. The label deletes it.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "The label stays on for as long as a `ci_check` of the issue names a held-back job",
    cut: "Each later head needs its own run as the proof of the check. The label deletes it.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "The tester takes the check's result from that run, as from any other.",
    cut: "It makes the run's result the check's result. The label deletes it.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "The CI job that `ci_check` names is the proof of that check at the head",
    cut: "It makes the job the proof. The label deletes it.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "This is true also when the job is a unit or integration suite",
    cut: "It makes the job the proof. The label deletes it.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "The limit: such a check claims only that the named job passed at that head.",
    cut: "It says what a pass of the job proves. The label deletes it.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "The tester continues to prove the surface with the checks that CI cannot run.",
    cut: "It says that CI proves the other checks. The label deletes it.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "The architect names the job and its `negative_control` by three rules",
    keep: "How the architect writes the field. It says nothing of a pass.",
  },
  {
    file: "skills/legion-worker/references/conflicts-and-rewrites.md",
    has: "After any forward merge, build the branch and record the issue's checks again",
    cut: "`each check with no ci_check (a check carrying one is read again from its CI run)` exempts such a check from the roles. The label deletes those words.",
  },
  {
    file: "skills/legion-worker/references/conflicts-and-rewrites.md",
    has: "Read `base` again and merge the bookmark onto it in one command",
    keep: "The command of a forward merge. It names no check of Dispatch.",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "- The reviewer reads CI on GitHub and verifies the `Threads` facts",
    keep: "GitHub's own checks of the head, which gate the merge apart from the checks of Dispatch.",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "One `completed` as `cancelled`, or absent from a run at the head",
    keep: "GitHub's own checks of the head, which gate the merge apart from the checks of Dispatch.",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "GitHub refuses a re-run while an attempt of that run is still in progress",
    keep: "GitHub's own checks of the head, which gate the merge apart from the checks of Dispatch.",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "When no run at the head holds the job, or nothing here will run it",
    keep: "GitHub's own checks of the head, which gate the merge apart from the checks of Dispatch.",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "A commit status is no check run",
    keep: "GitHub's own checks of the head, which gate the merge apart from the checks of Dispatch.",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "A required item in either list is reported",
    keep: "GitHub's own checks of the head, which gate the merge apart from the checks of Dispatch.",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "A job reports its `name:`",
    keep: "GitHub's own checks of the head, which gate the merge apart from the checks of Dispatch.",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "A head whose required checks are still running is waited for",
    keep: "GitHub's own checks of the head, which gate the merge apart from the checks of Dispatch.",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "A run `waiting` on an environment's approval",
    keep: "GitHub's own checks of the head, which gate the merge apart from the checks of Dispatch.",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "The daemon's own count in the issue's `mergeWait`",
    keep: "GitHub's own checks of the head, which gate the merge apart from the checks of Dispatch.",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "On a check with no `ci_check`, a fail recorded after a pass",
    keep: "The rule of a pass and a fail, which `ablate-later-fail` owns. Its half on `ci_check` gives a pass from CI no standing.",
    at: "base",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "A check carrying `ci_check` is CI's: the result that",
    cut: "It makes the derived result and the job's results decide the check. The label deletes it.",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "While that run has not concluded, wait for it by its name",
    keep: "The wait for a named run that has not concluded. The run of `ci-only` has concluded, and the sentence does not say that its pass is the evidence.",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "Every round records a handoff and takes this order: read CI as above",
    keep: "GitHub's own checks of the head, which gate the merge apart from the checks of Dispatch.",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "A request for changes does not wait, since it stands whatever CI says",
    keep: "A request for changes waits on no CI.",
  },
  {
    file: "skills/legion-worker/references/pr-body.md",
    fenced: true,
    keep: "A line of a fenced block: a format, a template or a command, which states no rule.",
  },
  {
    file: "skills/legion-worker/references/pr-body.md",
    has: "A check with `ci_check` names the CI check run that proves it in place of a `run`",
    cut: "The label cuts it to `A check with ci_check names a GitHub check run in place of a run`.",
  },
  {
    file: "skills/legion-worker/references/pr-body.md",
    has: "The surface is **production-like**",
    keep: "The surface that a result names. It names no CI run.",
  },
  {
    file: "skills/legion-worker/references/pr-body.md",
    has: "A unit or integration test is a regression lock, never a check's result, except as the CI job",
    cut: "The label deletes `, except as the CI job that a check's ci_check names`.",
  },
  {
    file: "skills/legion-worker/references/pr-body.md",
    has: "A push moves the head, with no Dispatch write of anyone's",
    cut: "`with no ci_check` exempts such a check from the roles' results. The label deletes those words.",
  },
  {
    file: "skills/legion-worker/references/pr-body.md",
    has: "- **The implementer records a result for every `before_merge` check",
    cut: "Its second sentence, `A check carrying ci_check is CI's: the implementer records nothing for it ...`, exempts such a check. The label deletes that sentence.",
  },
  {
    file: "skills/legion-worker/references/pr-body.md",
    has: "- **The tester judges the implementer's results and records its own.**",
    cut: "Its second sentence exempts such a check, whose results come from CI. The label deletes that sentence.",
  },
  {
    file: "skills/legion-worker/references/pr-body.md",
    has: "It runs no check whose `run` is a whole test suite in the pod",
    keep: "How a check with wrong words is rewritten. It says nothing of a pass.",
  },
  {
    file: "skills/legion-worker/references/pr-body.md",
    has: "A `ci_check` check with no pass standing at the head",
    cut: "It makes CI's results decide such a check for the tester. The label deletes it.",
  },
  {
    file: "skills/legion-worker/references/pr-body.md",
    has: "Try to break the changed surface",
    cut: "`except a ci_check job` makes such a job a step's evidence. The label deletes those words.",
  },
  {
    file: "skills/legion-worker/references/pr-body.md",
    has: "Equal: re-run only the bare gates",
    cut: "`with no ci_check` exempts such a check. The label deletes those words.",
  },
  {
    file: "skills/legion-worker/references/pr-body.md",
    has: "- **No deferrals** is the body's rule",
    keep: "The cost of a later round. It names no check of Dispatch.",
  },
  {
    file: "skills/legion-worker/references/pr-body.md",
    has: "- **GitHub refuses a pull request body over 65,536 characters.**",
    keep: "The size of a body. It names no check of Dispatch.",
  },
  {
    file: "skills/legion-worker/references/pr-body.md",
    has: "Completing with a unit suite as the only evidence, where no `ci_check` names it",
    cut: "`where no ci_check names it` lets a unit suite be a check's evidence. The label deletes those words.",
  },
  {
    file: "skills/legion-worker/references/tools.md",
    has: "Their evidence is CI's run at the head",
    keep: "A package that the pod could not run, which a role cites in its handoff. It names no check of Dispatch.",
  },
  {
    file: "skills/legion-worker/references/tools.md",
    has: "- **The rest, by package**, for a named run the pod does make.",
    keep: "A run that the pod makes. It names no check of Dispatch.",
  },
  {
    file: "skills/legion-worker/references/tools.md",
    has: "browser-parity tests need `bun install`",
    keep: "A test's setup. It names no check of Dispatch.",
  },
  {
    file: "packages/daemon/internal/prompts/go/worker-common.md",
    has: "A refusal records nothing, so the corrected call is applied.",
    keep: "A refused call. It names no check of Dispatch.",
  },
  {
    file: "packages/daemon/internal/prompts/roles/tester.md",
    has: "A check carrying `ci_check` is CI's: that read shows its results from CI",
    cut: "It gives the check to CI. The label deletes it.",
  },
  {
    file: "packages/daemon/internal/prompts/roles/tester.md",
    has: "It needs no pass from the implementer, and you do not run or record it.",
    cut: "It exempts such a check from the roles' results. The label deletes it.",
  },
  {
    file: "packages/daemon/internal/prompts/roles/tester.md",
    has: "Everything you post — check runs, PR comments",
    keep: "Who posts a check run. It names no check of Dispatch.",
  },
  {
    file: "skills/dispatch-writing-specs/SKILL.md",
    has: "For a whole test suite, name its CI job in `ci_check`",
    cut: "`: a check carrying ci_check is CI's and takes no run` gives the check to CI. The label deletes those words.",
  },
  {
    file: "skills/envoy/SKILL.md",
    has: "Settlement waits until the commit has had no check event",
    keep: "Envoy's settlement of CI. It names no check of Dispatch.",
  },
  {
    file: "skills/envoy/references/check-settlement.md",
    has: "GitHub's read is complete",
    keep: "Envoy's settlement of CI. It names no check of Dispatch.",
  },
  {
    file: "skills/envoy/references/check-settlement.md",
    has: "Keep commit statuses apart from check-run failures",
    keep: "Envoy's settlement of CI. It names no check of Dispatch.",
  },
  {
    file: "skills/envoy/references/check-settlement.md",
    has: "A check run with a status's name cannot retire that status",
    keep: "Envoy's settlement of CI. It names no check of Dispatch.",
  },
  {
    file: "skills/legion-architect/SKILL.md",
    has: "A check carrying `ci_check` is CI's: it names the CI check run that proves it",
    cut: "It gives the check to CI and makes the run its proof. The label deletes it.",
  },
  {
    file: "skills/legion-architect/SKILL.md",
    has: "Where a CI job that runs on an ordinary pull request already makes a check's proof",
    cut: "It makes a CI job the proof of a check. The label deletes it.",
  },
  {
    file: "skills/legion-architect/SKILL.md",
    has: "A check that a whole test suite proves names the CI job that runs that suite",
    cut: "It makes the CI job of a suite the proof of a check. The label deletes it.",
  },
  {
    file: "skills/legion-architect/SKILL.md",
    has: "The checks are the gate every later role reads",
    cut: "`with no ci_check` exempts such a check from the roles' results. The label deletes those words.",
  },
  {
    file: "skills/legion-architect/SKILL.md",
    has: "On a check with no `ci_check`, a fail recorded after a pass",
    keep: "The rule of a pass and a fail, which `ablate-later-fail` owns. Its half on `ci_check` gives a pass from CI no standing.",
    at: "base",
  },
  {
    file: "skills/legion-architect/SKILL.md",
    has: "A `ci_check` run still going, or GitHub unread, is waited for",
    keep: "The wait for a named run that has not concluded. The run of `ci-only` has concluded, and the sentence does not say that its pass is the evidence.",
  },
  {
    file: "skills/legion-architect/references/acceptance.md",
    has: "The next round of <KEY> is its last round",
    cut: "` with no ci_check` exempts such a check from the tester's round. The label deletes those words.",
  },
  {
    file: "skills/legion-architect/references/acceptance.md",
    has: "have a fresh `task` subagent run each check with no `ci_check` on the default branch",
    cut: "` with no ci_check` exempts such a check from the roles' results. The label deletes those words.",
  },
  {
    file: "skills/legion-architect/references/acceptance.md",
    has: "Read each `ci_check` check's results from CI in `dispatch results`",
    cut: "It makes CI's results the results of such a check, which no role records. The label deletes it.",
  },
  {
    file: "skills/legion-architect/references/design-gate.md",
    has: "Mark each requirement of the spec as a `requirement` block",
    keep: "The fields of a check block: `ci_check` in place of `run`. It says nothing of what proves the check.",
  },
  {
    file: "skills/legion-architect/references/design-gate.md",
    has: "The human approves both documents in one request",
    cut: "`with no ci_check` exempts such a check from the roles' results. The label deletes those words.",
  },
  {
    file: "skills/legion-architect/references/design-gate.md",
    has: "## A check CI proves",
    cut: "The heading says that CI proves the check. The label deletes it.",
  },
  {
    file: "skills/legion-architect/references/design-gate.md",
    has: "A check carrying `ci_check` is CI's: `ci_check` names the GitHub check run that proves the check",
    cut: "It gives the check to CI and makes the run its proof. The label deletes it.",
  },
  {
    file: "skills/legion-architect/references/design-gate.md",
    has: "No role runs it or records a result for it.",
    cut: "It says that no role records a result of such a check, so that the two-role rule cannot be met. The label deletes it.",
  },
  {
    file: "skills/legion-architect/references/design-gate.md",
    has: "`dispatch results` derives a result from the named run",
    cut: "It makes the named run's result, and the job's own, the results of the check in place of a role's. The label deletes it.",
  },
  {
    file: "skills/legion-architect/references/design-gate.md",
    has: "Where a CI job that runs on an ordinary pull request already makes a check's proof",
    cut: "It makes a CI job the proof of a check. The label deletes it.",
  },
  {
    file: "skills/legion-architect/references/design-gate.md",
    has: "A check that a whole test suite proves names the CI job that runs that suite as its `ci_check`",
    cut: "It makes the CI job of a suite the proof of a check, and says that CI proves it. The label deletes it.",
  },
  {
    file: "skills/legion-architect/references/design-gate.md",
    has: "Write a `ci_check` check by three rules:",
    keep: "The heading of the architect's rules for the field. It says nothing of a pass.",
  },
  {
    file: "skills/legion-architect/references/design-gate.md",
    has: "Name the check run of the job that runs the proof",
    cut: "It makes the job's run the proof of the check. The label deletes it.",
  },
  {
    file: "skills/legion-architect/references/design-gate.md",
    has: "Name a job that the repository holds back from an ordinary pull request too",
    cut: "It makes CI's run the only place of the check's proof. The label deletes it.",
  },
  {
    file: "skills/legion-architect/references/design-gate.md",
    has: "The worker adds the label that runs the held-back jobs at the head",
    cut: "`the tester takes the result from that run` makes the run's result the check's result. The label deletes it.",
  },
  {
    file: "skills/legion-architect/references/design-gate.md",
    has: "Without the label such a job reads `skipped`",
    cut: "Each later head needs its own run as the proof of the check. The label deletes it.",
  },
  {
    file: "skills/legion-architect/references/design-gate.md",
    has: "On cybermonklabs/legion: `ci-ok` passes when the jobs it did not select were skipped",
    keep: "Which run the field must not name. It says nothing of a pass of the named run.",
  },
  {
    file: "skills/legion-architect/references/wake-routing.md",
    has: "Read what the head carries in two lists, every page",
    keep: "GitHub's own checks of the head, which gate the merge apart from the checks of Dispatch.",
  },
  {
    file: "skills/legion-architect/references/wake-routing.md",
    has: "A required item in either list is reported",
    keep: "GitHub's own checks of the head, which gate the merge apart from the checks of Dispatch.",
  },
  {
    file: "skills/legion-architect/references/wake-routing.md",
    has: "A round whose head has no settled run gets no `review-stuck` notice",
    keep: "GitHub's own checks of the head, which gate the merge apart from the checks of Dispatch.",
  },
  {
    file: "skills/legion-architect/references/wake-routing.md",
    has: "A run still running there is waited for",
    keep: "GitHub's own checks of the head, which gate the merge apart from the checks of Dispatch.",
  },
  {
    file: "skills/legion-architect/references/wake-routing.md",
    has: "Every run concluded while `checkRuns` stays 0",
    keep: "GitHub's own checks of the head, which gate the merge apart from the checks of Dispatch.",
  },
  {
    file: "skills/legion-controller/references/parked-decisions.md",
    has: "When that run reaches the oldest such line",
    keep: "The controller's read of events. It names no check of Dispatch.",
  },
  {
    file: "skills/legion-controller/references/slack-mentions.md",
    has: "**A question about state.**",
    keep: "A question about Legion's state. It names no check of Dispatch.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "## No role runs it",
    cut: "The heading states the exemption that the sentences under it state. The label deletes it.",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "One refusal is about the turn rather than the report",
    keep: "A refused report of a look. It says nothing of a pass from CI.",
  },
  {
    file: "skills/legion-worker/references/withheld-capability.md",
    has: "The change's proof is CI's run at the head, never the pod's.",
    keep: "A capability the pod lacks, as `tools.md`'s `Their evidence is CI's run at the head`. It names no check of Dispatch and no pass that stands for one.",
  },
  {
    file: "skills/legion-worker/references/withheld-capability.md",
    has: "So a `@pr` row is proved by the pull-request lane",
    cut: "It makes the CI lane that `ci_check` names the proof of the check. The label deletes it.",
  },
  {
    file: "skills/plain-words/SKILL.md",
    has: "The check runs on the text going to the reader",
    keep: "A check of prose. It names no check of Dispatch.",
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
    has: "A pass and a fail",
    cut: "T5.12's third pair. The label restores `A pass and a fail from CI at one head leave no pass standing there.`",
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
    file: "skills/legion-worker/references/merge-gate.md",
    has: "A pass and a fail at one head leave it not known, whoever recorded them, and a later pass",
    cut: "T5.12 adds it. The label deletes it.",
    at: "head",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "That is a refusal: `REQUEST_CHANGES` naming the check",
    cut: "T5.12 adds it. The label deletes it.",
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
    file: "skills/legion-architect/SKILL.md",
    has: "A pass and a fail at one head leave it not known, whoever recorded them, and so the check's answer.",
    cut: "The architect's rule, in a text that the reviewer reads. T5.12 adds it, and the label deletes it.",
    at: "head",
  },
  {
    file: "skills/legion-architect/SKILL.md",
    has: "Such a check has no pass: send the issue back",
    cut: "The architect's rule, in a text that the reviewer reads. T5.12 adds it, and the label deletes it.",
    at: "head",
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
    file: "skills/legion-worker/references/withheld-capability.md",
    has: "Moving exactly these writes off that filesystem",
    keep: "A condition that an install fails. It names no check of Dispatch.",
  },
  {
    file: "skills/simplify-settled-diff/SKILL.md",
    has: "Where a dispatch fails for a reason that survives correcting the call",
    keep: "A subagent that fails. It names no check of Dispatch.",
  },
];
const ENTRIES_ARCHITECT_LATER_FAIL: readonly Entry[] = [
  {
    file: "packages/daemon/internal/prompts/roles/architect-root.md",
    has: "The person's answer, resolution or reply on any ask of the root",
    keep: "A person's answer gives a task. It names no check of Dispatch.",
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
    file: "skills/legion-architect/SKILL.md",
    has: "A pass and a fail at one head leave it not known, whoever recorded them, and so the check's answer.",
    cut: "T5.12 adds it. The label deletes it.",
    at: "head",
  },
  {
    file: "skills/legion-architect/SKILL.md",
    has: "Such a check has no pass: send the issue back",
    cut: "T5.12 adds it. The label deletes it.",
    at: "head",
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
    file: "skills/legion-worker/references/merge-gate.md",
    has: "A pass and a fail at one head leave it not known, whoever recorded them, and a later pass",
    cut: "The reviewer's rule, in a text that the architect reads. T5.12 adds it, and the label deletes it.",
    at: "head",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "That is a refusal: `REQUEST_CHANGES` naming the check",
    cut: "The reviewer's rule, in a text that the architect reads. T5.12 adds it, and the label deletes it.",
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
    file: "skills/legion-retro/SKILL.md",
    has: "A `ci_check` check has no pass standing at that head when",
    keep: "A pass and a fail on a check carrying `ci_check`. The sentence that the label restores in `legion-architect/SKILL.md:72` gives the rule for a check with none, which the scenario's `k1` is.",
  },
  {
    file: "skills/legion-worker/references/ci-checks.md",
    has: "a retraction does not lift the rule",
    keep: "A retraction and the field `ci_check`. It says nothing of a pass and a fail at one head.",
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
];

export const RULES: Record<string, Rule> = {
  "ablate-head": {
    role: "reviewer",
    rule: "The reviewer judges each check by the line of the head it reviews, not by the check's answer.",
    patterns: [
      /dispatch results|pass(es)? stands?\b|pass standing|own (line|answer)\b/i,
      /\bline\b[^.]{0,60}\bhead\b|\bhead\b[^.]{0,60}\bline\b/i,
      // A reviewer reads "this issue's own head" as the head it reviews when two pull requests of
      // the issue are open, so a sentence that names its own head states this rule too.
      /\bown head\b/i,
    ],
    entries: ENTRIES_HEAD,
  },
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
  "ablate-ci-pass": {
    role: "reviewer",
    rule: "A pass from the CI run that `ci_check` names is the evidence of that check, in place of the two roles.",
    patterns: [
      /ci_check|check run|named (run|job)|such a (check|job)|that run\b/,
      /\b(CI|ci)\b[^.]{0,80}\b(proof|proves?|proved|evidence|results?|pass|passes|derives?|records?|stands?|decides?)\b/,
      /\b(proof|proves?|proved|evidence|results?|pass|derives?|records?|stands?|decides?)\b[^.]{0,80}\b(CI|ci)\b/,
      /\b(run|job)\b[^.]{0,60}\b(proof|proves?|proved|evidence)\b|\b(proof|proves?|proved|evidence)\b[^.]{0,60}\b(run|job)\b/,
      /no role (runs|records)|needs no pass|do not run or record|records nothing|runs and records no/i,
    ],
    entries: ENTRIES_CI_PASS,
  },
  "ablate-later-fail": {
    role: "reviewer",
    rule: "A pass and a fail at one head leave it not known, whoever recorded them: request changes, with no wait.",
    patterns: LATER_FAIL,
    entries: ENTRIES_LATER_FAIL,
  },
  "ablate-architect-later-fail": {
    role: "architect",
    rule: "A pass and a fail at one head leave it and the check's answer not known: send the issue back, with no wait.",
    patterns: LATER_FAIL,
    entries: ENTRIES_ARCHITECT_LATER_FAIL,
  },
};

/** The texts of a role in `checkout`: the closure of its links. */
export interface RoleTexts {
  /** Each text, relative to the checkout, in byte order. */
  readonly files: string[];
  /** Each link of the first two kinds that names no file of the checkout, with the text that names it. */
  readonly missing: string[];
  /** The fewest links from an entry text to each text: 0 for an entry text. */
  readonly depth: ReadonlyMap<string, number>;
  /** The text whose link first reached each text; none for an entry text. */
  readonly via: ReadonlyMap<string, string>;
}

/** The files that the links of `text`, the text `file`, name. `strict` links must name a file. */
export function linksOf(
  checkout: string,
  file: string,
  text: string
): { strict: string[]; mentions: string[] } {
  const strict: string[] = [];
  for (const match of text.matchAll(/skill:\/\/([A-Za-z0-9_.-]+)((?:\/[A-Za-z0-9_./-]+)?)/g)) {
    const rest = (match[2] ?? "").replace(/[./]+$/, "");
    strict.push(rest === "" ? `skills/${match[1]}/SKILL.md` : `skills/${match[1]}${rest}`);
  }
  for (const match of text.matchAll(/\]\((?!https?:|skill:|dispatch:|#)([^)#\s]+\.md)/g))
    strict.push(path.normalize(path.join(path.dirname(file), match[1] ?? "")));
  const mentions: string[] = [];
  const skillDir = /^skills\/[^/]+/.exec(file)?.[0];
  for (const match of text.matchAll(/`([A-Za-z0-9_.][A-Za-z0-9_./-]*\.md)`/g)) {
    const named = match[1] ?? "";
    const places = [path.join(path.dirname(file), named)];
    if (skillDir !== undefined) places.push(path.join(skillDir, named));
    if (named.startsWith("skills/") || named.startsWith(`${PROMPTS}/`)) places.push(named);
    const found = places
      .map((place) => path.normalize(place))
      .find((place) => existsSync(path.join(checkout, place)));
    if (found !== undefined) mentions.push(found);
  }
  return { strict, mentions };
}

/** What names a skill that no link of an entry text names: the list of skills a session gets. */
export const SKILL_LIST = "(the session's list of skills)";

/** Each installed skill's SKILL.md: each folder of skills/ that holds one, as the plugins stage
 * them (scripts/pi-plugin-prepack.sh, whose two lists partition skills/). */
export function installedSkills(checkout: string): string[] {
  const dir = path.join(checkout, "skills");
  if (!existsSync(dir)) return [];
  return readdirSync(dir, { withFileTypes: true })
    .filter((entry) => entry.isDirectory() && existsSync(path.join(dir, entry.name, "SKILL.md")))
    .map((entry) => `skills/${entry.name}/SKILL.md`)
    .sort();
}

/** The texts a role reads in `checkout`: its entry texts, then each text a link names, to closure. */
export function roleTexts(checkout: string, role: Role): RoleTexts {
  const { parts, skill } = ROLE_TEXTS[role];
  const depth = new Map<string, number>();
  const via = new Map<string, string>();
  const missing: string[] = [];
  let frontier: string[] = [];
  const reach = (file: string, from: string | undefined, at: number, strict: boolean) => {
    if (depth.has(file)) return;
    if (!existsSync(path.join(checkout, file))) {
      if (strict && !missing.some((m) => m.startsWith(`${file} `)))
        missing.push(`${file} (named in ${from ?? "the role's prompt parts and skill"})`);
      return;
    }
    depth.set(file, at);
    if (from !== undefined) via.set(file, from);
    frontier.push(file);
  };
  for (const root of [...parts, skill]) reach(root, undefined, 0, true);
  for (let at = 1; frontier.length > 0; at++) {
    const level = frontier;
    frontier = [];
    for (const file of level) {
      const { strict, mentions } = linksOf(
        checkout,
        file,
        readFileSync(path.join(checkout, file), "utf8")
      );
      for (const named of strict) reach(named, file, at, true);
      for (const named of mentions) reach(named, file, at, false);
    }
    if (at === 1)
      for (const installed of installedSkills(checkout)) reach(installed, SKILL_LIST, at, false);
  }
  return { files: [...depth.keys()].sort(), missing, depth, via };
}

/** The chain of links from an entry text to `file`, the entry text first. */
export function chainOf(texts: RoleTexts, file: string): string[] {
  const chain = [file];
  for (let from = texts.via.get(file); from !== undefined; from = texts.via.get(from))
    chain.unshift(from);
  return chain;
}

export interface Sentence {
  readonly file: string;
  readonly line: number;
  readonly text: string;
  /** A line of a fenced block. */
  readonly fenced: boolean;
}

/** Each sentence of `text`, with the line it starts on. */
export function sentences(file: string, text: string): Sentence[] {
  // A unit is a heading, a list item, a table row, a line of a fenced block or a paragraph: its
  // lines, each trimmed and with its runs of white space as one space, joined by one space, and
  // the offset in the joined text where each line starts.
  type Unit = { starts: { line: number; at: number }[]; text: string; fenced: boolean };
  const units: Unit[] = [];
  let fenced = false;
  let open: Unit | undefined;
  const close = () => {
    if (open !== undefined) units.push(open);
    open = undefined;
  };
  text.split("\n").forEach((raw, index) => {
    const line = index + 1;
    const words = raw.trim().replace(/\s+/g, " ");
    if (words.startsWith("```")) {
      close();
      fenced = !fenced;
      return;
    }
    if (words === "") {
      close();
      return;
    }
    if (fenced || open === undefined || /^(#{1,6} |[-*+] |\d+\. |\|)/.test(words)) {
      close();
      open = { starts: [{ line, at: 0 }], text: words, fenced };
      if (fenced || words.startsWith("#") || words.startsWith("|")) close();
      return;
    }
    open.starts.push({ line, at: open.text.length + 1 });
    open.text += ` ${words}`;
  });
  close();
  const found: Sentence[] = [];
  for (const unit of units) {
    let from = 0;
    const ends = [...unit.text.matchAll(/(?<=[.?!])\s+(?=[A-Z`"*(])/g)].map((m) => [
      m.index,
      m.index + m[0].length,
    ]);
    for (const [end, next] of [...ends, [unit.text.length, unit.text.length]]) {
      const at = from;
      const line =
        unit.starts.filter((start) => start.at <= at).at(-1)?.line ?? unit.starts[0]?.line ?? 0;
      found.push({ file, line, text: unit.text.slice(from, end), fenced: unit.fenced });
      from = next as number;
    }
  }
  return found;
}

export interface Outcome {
  readonly lines: string[];
  readonly faults: string[];
  readonly hits: number;
  readonly cut: number;
  readonly keep: number;
  readonly texts: number;
  /** How many texts at each depth, as `depth 0: 7, depth 1: 16`. */
  readonly depths: string;
  /** The texts that hold a cut, each once. */
  readonly cutTexts: number;
}

/** How many texts of `texts` sit at each depth. */
export function depthText(texts: RoleTexts): string {
  const count = new Map<number, number>();
  for (const at of texts.depth.values()) count.set(at, (count.get(at) ?? 0) + 1);
  return [...count]
    .sort((a, b) => a[0] - b[0])
    .map(([at, n]) => `depth ${at}: ${n}`)
    .join(", ");
}

/** The classified search of one label in `checkout`. */
export function search(checkout: string, tree: Tree, label: string): Outcome {
  const rule = RULES[label];
  if (rule === undefined) throw new Error(`no rule for the label ${label}`);
  const texts = roleTexts(checkout, rule.role);
  const { files, missing } = texts;
  const cutFiles = new Set<string>();
  const faults = missing.map(
    (file) => `${label}: a text the ${rule.role} reads is not in the checkout: ${file}`
  );
  const entries = rule.entries.filter((entry) => entry.at === undefined || entry.at === tree);
  const matched = new Map<Entry, number>();
  const lines: string[] = [];
  let cut = 0;
  let keep = 0;
  let hits = 0;
  for (const file of files) {
    for (const sentence of sentences(file, readFileSync(path.join(checkout, file), "utf8"))) {
      if (!rule.patterns.some((pattern) => pattern.test(sentence.text))) continue;
      hits++;
      const owners = entries.filter(
        (entry) =>
          entry.file === file &&
          (entry.fenced === true
            ? sentence.fenced
            : !sentence.fenced && sentence.text.includes(entry.has ?? "\u0000"))
      );
      for (const owner of owners) matched.set(owner, (matched.get(owner) ?? 0) + 1);
      const where = `${file}:${sentence.line} (depth ${texts.depth.get(file)})`;
      const words =
        sentence.text.length > 140 ? `${sentence.text.slice(0, 137)}...` : sentence.text;
      if (owners.length === 1) {
        const owner = owners[0] as Entry;
        if (owner.cut !== undefined) {
          cut++;
          cutFiles.add(file);
        } else keep++;
        lines.push(
          `${where} ${owner.cut !== undefined ? "CUT " : "KEEP"} ${words}\n    ${owner.cut ?? owner.keep}`
        );
      } else {
        faults.push(
          owners.length === 0
            ? `${label}: ${where} states the rule, or may, and no entry classifies it: ${words}`
            : `${label}: ${where} matches ${owners.length} entries: ${owners.map((o) => o.has ?? "fenced").join(" | ")}`
        );
      }
    }
  }
  for (const entry of entries) {
    const count = matched.get(entry) ?? 0;
    if (entry.fenced === true) continue;
    if (count !== 1)
      faults.push(
        `${label}: the entry "${entry.has}" of ${entry.file} matches ${count} hits at ${tree}, want 1`
      );
  }
  if (files.length === 0) faults.push(`${label}: UNKNOWN: read no text of the ${rule.role}`);
  if (hits === 0)
    faults.push(`${label}: UNKNOWN: no hit in ${files.length} texts, so the patterns see nothing`);
  return {
    lines,
    faults,
    hits,
    cut,
    keep,
    texts: files.length,
    depths: depthText(texts),
    cutTexts: cutFiles.size,
  };
}

function main(argv: string[]): number {
  const [checkout, tree, ...labels] = argv;
  if (checkout !== undefined && tree === "texts") {
    const role = labels[0];
    if (labels.length !== 1 || (role !== "reviewer" && role !== "architect")) {
      console.error("usage: bun rule-search.ts <checkout> texts <reviewer|architect>");
      return 2;
    }
    const texts = roleTexts(checkout, role);
    for (const file of texts.files)
      console.log(
        `${texts.depth.get(file)} ${file} <= ${chainOf(texts, file).slice(0, -1).join(" > ")}`
      );
    console.log(`${role}: ${texts.files.length} texts, ${depthText(texts)}`);
    for (const file of texts.missing)
      console.error(`FAULT: a text the ${role} reads is not in the checkout: ${file}`);
    return texts.missing.length > 0 || texts.files.length === 0 ? 1 : 0;
  }
  if (checkout === undefined || (tree !== "base" && tree !== "head") || labels.length === 0) {
    console.error("usage: bun rule-search.ts <checkout> <base|head> <label>...");
    console.error("       bun rule-search.ts <checkout> texts <reviewer|architect>");
    console.error(`labels: ${Object.keys(RULES).join(", ")}`);
    return 2;
  }
  const unknown = labels.filter((label) => RULES[label] === undefined);
  if (unknown.length > 0) {
    console.error(`no rule for ${unknown.join(", ")}; labels: ${Object.keys(RULES).join(", ")}`);
    return 2;
  }
  let failed = false;
  for (const label of labels) {
    const outcome = search(checkout, tree, label);
    console.log(`## ${label}: ${RULES[label]?.rule}`);
    for (const line of outcome.lines) console.log(line);
    console.log(
      `${label} at ${tree}: ${outcome.hits} hits in ${outcome.texts} texts of the ${RULES[label]?.role} (${outcome.depths}), ${outcome.cut} cut in ${outcome.cutTexts} texts, ${outcome.keep} kept with a reason, ${outcome.faults.length} faults`
    );
    for (const fault of outcome.faults) console.error(`FAULT: ${fault}`);
    failed ||= outcome.faults.length > 0;
  }
  return failed ? 1 : 0;
}

if (import.meta.main) process.exit(main(Bun.argv.slice(2)));
