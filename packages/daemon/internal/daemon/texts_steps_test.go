package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/sjawhar/legion/daemon/internal/texts"
)

// goldenStep is one step this change took out of a daemon text, for one set of its readers: the
// kind, main's sentence as the Go literal of its builder at b4a566cb, or at 237f31c7 for a
// sentence main wrote since (a format verb, %s or %d, and … stand for any text), the readers, and
// the passage of the readers' prompt
// (packages/daemon/internal/prompts) or of a skill they load (skills/) that gives each of them its
// own rule for the step, quoted word for word from one line of the file.
type goldenStep struct {
	kind     texts.Kind
	sentence string
	readers  string
	file     string
	passage  string
}

const (
	goPrompts   = "packages/daemon/internal/prompts/go/"
	rolePrompts = "packages/daemon/internal/prompts/roles/"
)

// goldenSteps is the table of every step taken out of a daemon text, for each of its readers.
var goldenSteps = []goldenStep{
	{texts.PhaseAssignment, " Handoff: %s version %d (dispatch handoff-read --issue %s --phase %s --version %d).", "planner, implementer, tester, reviewer, merger",
		goPrompts + "worker-common.md", "Your task names the predecessor's handoff in two fields, `handoff_phase` (its `<word>`) and `handoff_version` (its `<n>`): read it with `dispatch handoff-read --issue <issue> --phase <word> --version <n>`."},
	{texts.PhaseAssignment, "Continue %s. Issue: %s. Phase: %s.", "planner, implementer, tester, reviewer, merger",
		goPrompts + "worker-common.md", "A `phase-assignment` task names the issue's `title` and the `phase` you start: do that phase's work."},
	{texts.PhaseAssignment, "the head conflicts with %s: GitHub runs no checks on it; merge %s forward", "implementer",
		goPrompts + "implementer.md", "A task whose `conflicts_with` names a base says that the head it names conflicts with that base, so GitHub runs no check on it: merge that base forward, as `skill://legion-worker/references/conflicts-and-rewrites.md` says, then push."},
	{texts.PhaseAssignment, "the reviewer completed its round on pull request #%d with no review that decides it: only an APPROVE of head %s or a REQUEST_CHANGES ends the round", "reviewer",
		goPrompts + "reviewer.md", "`stuck_round: undecided` means that no review decided the round."},
	{texts.PhaseAssignment, "; a review that would otherwise have decided this round was set aside - ", "reviewer",
		goPrompts + "reviewer.md", "`set_aside` says why the daemon set aside a review that would otherwise have decided the round."},
	{texts.PhaseAssignment, "the reviewer approved %s on pull request #%d, which does not approve head %s: a push since may have changed code, so only an APPROVE of %s or a REQUEST_CHANGES ends the round", "reviewer",
		goPrompts + "reviewer.md", "`stuck_round: approved-other-code` means that you approved a head whose code the current head may not carry, and `reviewed_head` names that approved head."},
	{texts.PhaseAssignment, "the reviewer approved head %s on pull request #%d, and GitHub reports that head %s rather than one it would merge.", "reviewer",
		goPrompts + "reviewer.md", "`stuck_round: merge-state` means that GitHub will not merge the head you approved, as an `approved-round-stuck` message says."},
	{texts.PhaseResume, " Resume the existing phase work.", "planner, implementer, tester, reviewer, merger",
		goPrompts + "worker-common.md", "A `phase-resume` task names a phase that is already under way: resume the work of that phase where it stands."},
	{texts.CheckLook, "unknown, read it from the pull request on GitHub", "tester",
		goPrompts + "tester.md", "Where the task names no merge commit, read it from the pull request on GitHub."},
	{texts.CheckLook, "unknown, read it from the pull request on GitHub", "implementer",
		goPrompts + "implementer.md", "Where the task names no merge commit, read it from the pull request on GitHub."},
	{texts.CheckLook, " This is the last look: no look follows.", "tester",
		goPrompts + "tester.md", "The task of the bound's look carries `last: true`"},
	{texts.CheckLook, " This is the last look: no look follows.", "implementer",
		goPrompts + "implementer.md", "whether it is the last (`last: true`)"},
	{texts.CheckLook, "Do what the deployment instructions say to reach production and read whether production carries the merge. ", "tester",
		goPrompts + "tester.md", "Do what the deployment instructions say to reach production, read whether production carries the merge, then report with `handoff_complete` and a `verdict`, committing nothing and writing no handoff"},
	{texts.CheckLook, "Do what the deployment instructions say to reach production and read whether production carries the merge. ", "implementer",
		goPrompts + "implementer.md", "Each look reaches production as the deployment instructions say and reads whether production carries the merge."},
	{texts.CheckLook, "Where it does, drive the changed path and observe each requirement of the issue that only the deploy can show, read from the spec version that bound the review, each as the requirement names it: observing something else is never that requirement observed. ", "tester",
		rolePrompts + "tester.md", "`pass` covers the changed path and each requirement of the issue that only the deploy can show, read from the spec version that bound the review, and `observed` names each and what was seen."},
	{texts.CheckLook, "Where it does, drive the changed path and observe each requirement of the issue that only the deploy can show, read from the spec version that bound the review, each as the requirement names it: observing something else is never that requirement observed. ", "implementer",
		rolePrompts + "implementer.md", "`pass` covers the changed path and each requirement of the issue that only the deploy can show, read from the spec version that bound the review, and `observed` names each and what was seen."},
	{texts.CheckLook, "pass only once you drove the changed path in production and observed every one of those requirements, and recorded the issue's after_merge check with dispatch result at the merge commit, its observed naming each requirement and what you saw of it and what woke this look, and put the line that command prints in the pull request body; ", "tester",
		goPrompts + "tester.md", "`pass` once you drove the changed path in production and recorded the result against the issue's `after_merge` check with `dispatch result` at the merge commit, naming what woke this look, with the line it prints in the pull request body's `Checks` block"},
	{texts.CheckLook, "pass only once you drove the changed path in production and observed every one of those requirements, and recorded the issue's after_merge check with dispatch result at the merge commit, its observed naming each requirement and what you saw of it and what woke this look, and put the line that command prints in the pull request body; ", "implementer",
		goPrompts + "implementer.md", "Put the line that command prints in the pull request body's `Checks` block."},
	{texts.CheckLook, "pending when production does not carry the merge yet, or carries it with one of those requirements still unobserved and a look still to follow, the summary naming what is missing and what you will look for next, with no result recorded and the pull request body saying the check is still open on the merge commit; ", "tester",
		goPrompts + "tester.md", "nothing recorded and that line left at `- Check <block id> (after_merge): not yet run`, which says that the check is still open on the merge commit"},
	{texts.CheckLook, "pending when production does not carry the merge yet, or carries it with one of those requirements still unobserved and a look still to follow, the summary naming what is missing and what you will look for next, with no result recorded and the pull request body saying the check is still open on the merge commit; ", "implementer",
		goPrompts + "implementer.md", "Record nothing for it, and leave the check's line in the pull request body at `- Check <block id> (after_merge): not yet run`, which says that the check is still open on the merge commit."},
	{texts.CheckLook, "fail for a defect, never an issue you file (skill://legion-worker/references/findings.md), …; record the after_merge check as a fail with dispatch result at the merge commit, and name the message or the ask in its observed, in the line you put in the pull request body and in the summary.", "tester",
		goPrompts + "tester.md", "never an issue you file (`skill://legion-worker/references/findings.md`)"},
	{texts.CheckLook, "record the after_merge check as a fail with dispatch result at the merge commit, and name the message or the ask in its observed, in the line you put in the pull request body and in the summary.", "tester",
		goPrompts + "tester.md", "The result's `observed`, the line you put in the pull request body and the summary name the message or the ask."},
	{texts.CheckLook, "fail for a defect, never an issue you file (skill://legion-worker/references/findings.md), …; record the after_merge check as a fail with dispatch result at the merge commit, and name the message or the ask in its observed, in the line you put in the pull request body and in the summary.", "implementer",
		goPrompts + "implementer.md", "Report `fail` for a defect, never an issue you file (`skill://legion-worker/references/findings.md`)."},
	{texts.CheckLook, "record the after_merge check as a fail with dispatch result at the merge commit, and name the message or the ask in its observed, in the line you put in the pull request body and in the summary.", "implementer",
		goPrompts + "implementer.md", "Record the `after_merge` check as a fail with `dispatch result` at the merge commit, and name the message or the ask in its `observed`, in the line you put in the pull request body and in the summary."},
	{texts.CheckLook, " This last look's pending carries no wake_at and closes the check as not run, so it is for production not carrying the merge alone: before reporting it, write on the pull request that the check did not run because production did not carry the merge within the bound.", "tester",
		goPrompts + "tester.md", "so before reporting it write on the pull request that the check did not run because production did not carry the merge within the bound."},
	{texts.CheckLook, " This last look's pending carries no wake_at and closes the check as not run, so it is for production not carrying the merge alone: before reporting it, write on the pull request that the check did not run because production did not carry the merge within the bound.", "implementer",
		goPrompts + "implementer.md", "before you report it, write on the pull request that the check did not run because production did not carry the merge within the bound."},
	{texts.CheckLook, "At that report the daemon stops this process and keeps this session, and your next look resumes it with a task naming the summary you gave as what woke it.", "tester",
		goPrompts + "tester.md", "the next look brings the pod back and resumes this session with a new task that names the summary you sent as what woke it."},
	{texts.CheckLook, " Wait for nothing in this session: end the turn with the report.", "tester",
		goPrompts + "tester.md", "So wait for nothing in this session: end the turn with the report."},
	{texts.CheckLook, " Wait for nothing in this session: end the turn with the report.", "implementer",
		goPrompts + "implementer.md", "Wait for nothing in the session: end the turn with the report."},
	{texts.RootResume, "Your ask %s on %s was retracted when its decision block left the spec, so no person will answer it. Read the spec as it stands; if you still need the decision, ask again.", "architect (root)",
		rolePrompts + "architect-root.md", "After a retraction (`act: retracted`), no person will answer that ask, because its decision block left the spec: read the spec as it stands, and ask again only if you still need the decision."},
	{texts.RootResume, "Your ask %s on %s was closed by %s, so no person will answer it. Read its thread; if you still need the decision, ask again.", "architect (root)",
		rolePrompts + "architect-root.md", "After a close by anyone who is not a person (`act: closed`, where `closed_by` names who closed it), no person will answer that ask either: read its thread, and ask again only if you still need the decision."},
	{texts.RootResume, " Waiting on you: %s; read each thread and reply.", "architect (root)",
		rolePrompts + "architect-root.md", "Each ask that the task lists as waiting on you (`waiting_on_you`) is yours to answer: read its thread and reply in it."},
	{texts.RootResume, " Still with the person: %s. Hand each one back once you have done what the answer asks — a reply in its thread that leaves the turn with the person", "architect (root)",
		rolePrompts + "architect-root.md", "Hand an item back with a reply in its thread that leaves the turn with the person, or, while the gate is armed and closed, with a new approval request and `register_gate` of that version."},
	{texts.RootResume, " — or close it if you no longer need it. Handing one back starts your next wait; then end your turn.", "architect (root)",
		rolePrompts + "architect-root.md", "Close an item that you no longer need instead of handing it back."},
	{texts.RootResume, " — or close it if you no longer need it. Handing one back starts your next wait; then end your turn.", "architect (root)",
		rolePrompts + "architect-root.md", "Handing an item back starts your next wait, so end your turn after it."},
	{texts.RootResume, " When the %s is ready, hand the request back to the person: request approval again and register that version with register_gate", "architect (root)",
		rolePrompts + "architect-root.md", "When nothing is still with the person and the gate is armed and closed, hand the request back once the document is ready: request approval again and register that version with `register_gate`, or answer in the request's thread while that thread is open (`request_open`)."},
	{texts.RootResume, " The design gate is closed at version %d, as no registration of that version has opened it: when the %s is ready, register it at its current version, which opens the gate with no approval.", "architect (root)",
		rolePrompts + "architect-root.md", "Where the gate is not armed (`gate_armed: false`), no person approves the document: when it is ready, register it at its current version, which opens the gate with no approval."},
	{texts.RootResume, " Read the review, and revise the %s as they ask.", "architect (root)",
		rolePrompts + "architect-root.md", "A changes request asks you to revise the document as the person asks in its reason, except where the gate is open and armed: a person then approved the document after that review, so it asks for no revision now."},
	{texts.RootResume, " A person approved the %s after that review, so it asks for no revision now; read it.", "architect (root)",
		rolePrompts + "architect-root.md", "A changes request asks you to revise the document as the person asks in its reason, except where the gate is open and armed: a person then approved the document after that review, so it asks for no revision now."},
	{texts.RootResume, " A person then requested changes to your %s at %s. Read the review, and revise the %s as they ask.", "architect (root)",
		rolePrompts + "architect-root.md", "A changes request made after the wait began that did not answer it (`later_changes_version`, with its reason in the `later_reason` block) asks the same revision."},
	{texts.RootResume, "A newer version of your %s on %s, %s, handed the approval request back to you. Read it and the request's thread.", "architect (root)",
		rolePrompts + "architect-root.md", "A newer version that handed the approval request back to you (`act: version`) asks you to read that version and the request's thread."},
	{texts.RootResume, " Continue.", "architect (root)",
		rolePrompts + "architect-root.md", "Where nothing is with the person, nothing waits on you and the gate is open, continue the tree's work."},
	{texts.RootResume, "; read it.", "architect (root)",
		rolePrompts + "architect-root.md", "carries the person's words in a block, so read those words in the task."},
	{texts.RootResume, "; read that thread.", "architect (root)",
		rolePrompts + "architect-root.md", "carries the person's words in a block, so read those words in the task."},
	{texts.RootResume, "; read its thread.", "architect (root)",
		rolePrompts + "architect-root.md", "carries the person's words in a block, so read those words in the task."},
	{texts.RootResume, "; read the issue's asks and its spec.", "architect (root)",
		rolePrompts + "architect-root.md", "carries the person's words in a block, so read those words in the task."},
	{texts.KeptDecisions, "While nobody in %s's tree could hear it, a person decided on the tree: %s. Read each before you continue.", "architect (root)",
		goPrompts + "architect-root.md", "Act on each one before you carry on."},
	{texts.KeptDecisions, "The %s is approved at version %d and is now at version %d: request approval of version %d, as skill://legion-architect/references/design-gate.md (\"The armed gate\") says.", "architect (root)",
		goPrompts + "architect-root.md", "request approval of that latest version, as `skill://legion-architect/references/design-gate.md` (\"The armed gate\") says."},
	{texts.ArchitectStart, "Take it up in this turn, as your role says: claim it in Dispatch", "architect (root)",
		goPrompts + "architect-root.md", "Take it up in this turn."},
	{texts.ArchitectStart, " Nothing else starts this tree, and no message is coming.", "architect (root)",
		goPrompts + "architect-root.md", "`nothing_else_starts_it: true` means that nothing else starts this tree and no message is coming."},
	{texts.ArchitectStart, "Decompose nothing and release no child.", "architect (root)",
		goPrompts + "architect-root.md", "`decompose: false` means that you decompose nothing and release no child."},
	{texts.ArchitectStart, " Its pull request has merged, so the daemon starts no worker for that phase, and the root stands there until the rest of its tree has left the workflow, when the daemon closes it.", "architect (root)",
		goPrompts + "architect-root.md", "which stands in its phase until the rest of its tree has left the workflow and the daemon closes it."},
	{texts.PodWake, "Your issue's pod had been released while it was idle, and this wake brought it back. Your session, your workspace and any work you had not pushed are as you left them. ", "planner, implementer, tester, reviewer, merger",
		goPrompts + "worker-common.md", "Your session, your workspace and the work you had not pushed are as you left them."},
	{texts.PodWake, "Your issue's pod had been released while it was idle, and this wake brought it back. Your session, your workspace and any work you had not pushed are as you left them. ", "architect",
		goPrompts + "architect-common.md", "Your session and your workspace are as you left them."},
	{texts.PodWake, ". Read the checks before you decide anything.", "planner, implementer, tester, reviewer, merger",
		goPrompts + "worker-common.md", "read the checks before you decide anything."},
	{texts.PodWake, ". Read the checks before you decide anything.", "architect",
		goPrompts + "architect-common.md", "read the checks before you decide anything."},
	{texts.PodWake, "A message sent with keep: false is not kept, and its sender sends it again.", "planner, implementer, tester, reviewer, merger",
		goPrompts + "worker-common.md", "A message sent with `keep: false` is not kept, and its sender sends it again."},
	{texts.PodWake, "A message sent with keep: false is not kept, and its sender sends it again.", "architect",
		goPrompts + "architect-common.md", "A message sent with `keep: false` is not kept, and its sender sends it again."},
	{texts.PodWake, ") while your pod was gone. A message that Envoy or Dispatch stored for your session reaches you in this session. ", "planner, implementer, tester, reviewer, merger",
		goPrompts + "worker-common.md", "A message that Envoy or Dispatch stored for your session reaches you in this session."},
	{texts.PodWake, ") while your pod was gone. A message that Envoy or Dispatch stored for your session reaches you in this session. ", "architect",
		goPrompts + "architect-common.md", "A message that Envoy or Dispatch stored for your session reaches you in this session."},
	{texts.PodWake, "A direct Envoy message that Envoy refused reaches you when its sender sends it again.", "planner, implementer, tester, reviewer, merger",
		goPrompts + "worker-common.md", "A direct Envoy message that Envoy refused reaches you when its sender sends it again."},
	{texts.PodWake, "A direct Envoy message that Envoy refused reaches you when its sender sends it again.", "architect",
		goPrompts + "architect-common.md", "A direct Envoy message that Envoy refused reaches you when its sender sends it again."},
	{texts.PodWake, ". Nothing else asked for you, so take up your phase where you left it.", "planner, implementer, tester, reviewer, merger",
		goPrompts + "worker-common.md", "`wake: backstop` means that nothing else asked for you: take up your phase where you left it."},
	{texts.RelaunchNote, "Before you do any of that work again, check what it already did.", "planner, implementer, tester, reviewer, merger",
		goPrompts + "worker-common.md", "Before you do any of that work again, check what it already did in your workspace and on the issue's branch."},
	{texts.RelaunchNote, "Before you do any of that work again, check what it already did.", "architect",
		goPrompts + "architect-common.md", "Before you do any of that work again, check what it already did."},
	{texts.RelaunchNote, "Before you do any of that work again, check what it already did.", "controller",
		rolePrompts + "controller-root.md", "Before you do any of that work again, check what it already did, with `read_state` and in Dispatch."},
	{texts.RelaunchNote, "Then continue the task.", "planner, implementer, tester, reviewer, merger",
		goPrompts + "worker-common.md", "Before you do any of that work again, check what it already did in your workspace and on the issue's branch."},
	{texts.RelaunchNote, "No result of that work will come to you.", "planner, implementer, tester, reviewer, merger",
		goPrompts + "worker-common.md", "Your tool calls in flight, and the subagents and background jobs that stopped, give you no result, and your transcript does not say so."},
	{texts.RelaunchNote, "No result of that work will come to you.", "architect",
		goPrompts + "architect-common.md", "Your tool calls in flight, and the subagents and background jobs that stopped, give you no result, and your transcript does not say so."},
	{texts.RelaunchNote, "No result of that work will come to you.", "controller",
		rolePrompts + "controller-root.md", "Your tool calls in flight, and the subagents and background jobs that stopped, give you no result, and your transcript does not say so."},
	{texts.RelaunchNote, "Your transcript does not say so.", "planner, implementer, tester, reviewer, merger",
		goPrompts + "worker-common.md", "Your tool calls in flight, and the subagents and background jobs that stopped, give you no result, and your transcript does not say so."},
	{texts.RelaunchNote, "Your transcript does not say so.", "architect",
		goPrompts + "architect-common.md", "Your tool calls in flight, and the subagents and background jobs that stopped, give you no result, and your transcript does not say so."},
	{texts.RelaunchNote, "Your transcript does not say so.", "controller",
		rolePrompts + "controller-root.md", "Your tool calls in flight, and the subagents and background jobs that stopped, give you no result, and your transcript does not say so."},
	{texts.AbandonedSessionNote, "Before you repeat any of that work, %s, and tell the architect your Legion addressing line names what you found, unless that is you or it names none.", "planner, implementer, tester, reviewer, merger",
		goPrompts + "worker-common.md", "Before you repeat any of that work, read the issue, then the `branch` the note names and the handoff records on the issue (`dispatch handoff-read --issue <issue> --phase <word>`, for `plan`, `implement`, `test` and `review`)."},
	{texts.AbandonedSessionNote, "read the issue, then your branch %s and the handoff records its phases wrote on the issue, which `dispatch handoff-read --issue %s --phase <word>` reads (%s)", "planner, implementer, tester, reviewer, merger",
		goPrompts + "worker-common.md", "Before you repeat any of that work, read the issue, then the `branch` the note names and the handoff records on the issue (`dispatch handoff-read --issue <issue> --phase <word>`, for `plan`, `implement`, `test` and `review`)."},
	{texts.AbandonedSessionNote, ", and tell the architect your Legion addressing line names what you found, unless that is you or it names none.", "planner, implementer, tester, reviewer, merger",
		goPrompts + "worker-common.md", "Then tell the architect that your `Legion addressing` line names what you found."},
	{texts.AbandonedSessionNote, ", and tell the architect your Legion addressing line names what you found, unless that is you or it names none.", "architect",
		goPrompts + "architect-common.md", "Where your `Legion addressing` line names an architect above you, tell it what you found."},
	{texts.AbandonedSessionNote, "Before you repeat any of that work, %s, and tell the architect", "controller",
		rolePrompts + "controller-root.md", "Before you repeat any of that work, read the issue."},
	{texts.OutOfMemoryNote, "The work that filled the pod can be another role's, not yours. An issue pod runs no heavy build and no whole test suite, so read skill://legion-worker/references/tools.md before you run such work again, and send it to CI on the pull request or where your deployment's instructions say.", "planner, implementer, tester, reviewer, merger",
		goPrompts + "worker-common.md", "After an `out-of-memory-note`, remember that the work that filled the pod can be another role's: read `skill://legion-worker/references/tools.md` before you run heavy work again, and send such work to CI on the pull request or where your deployment's instructions say."},
	{texts.OutOfMemoryNote, "The work that filled the pod can be another role's, not yours.", "architect",
		goPrompts + "architect-common.md", "which can be another role's work."},
	{texts.ProviderErrorNote, "Your work did not cause it. ", "planner, implementer, tester, reviewer, merger",
		goPrompts + "worker-common.md", "A `provider-error-note` says that a provider error stopped your turn, and your work did not cause it."},
	{texts.ProviderErrorNote, "Read what that turn already did in your workspace and on the issue's branch. Then continue the task.", "architect",
		goPrompts + "architect-common.md", "A `provider-error-note` says that a provider error stopped your turn, and your work did not cause it."},
	{texts.ProviderErrorNote, "Read what that turn already did in your workspace and on the issue's branch. Then continue the task.", "controller",
		rolePrompts + "controller-root.md", "Before you do any of that work again, check what it already did, with `read_state` and in Dispatch."},
	{texts.ChecksRed, "the head conflicts with %s: GitHub runs no checks on it; merge %s forward", "architect",
		goPrompts + "architect-common.md", "the head takes a merge of that base forward."},
	{texts.CatchUp, "; start or resume it as your role says", "architect (root)",
		goPrompts + "architect-root.md", "Start or resume your tree from it as your role says."},
	{texts.CatchUp, "; start or resume your issue as your role says", "architect (child)",
		goPrompts + "architect.md", "Start or resume your issue from it as your role says."},
	{texts.CatchUp, "; in accepting, waiting for your acceptance, which no worker gives: %s (read the evidence, then sign_off or request_backward_move)", "architect (root)",
		goPrompts + "architect-root.md", "so read each one's evidence, then `sign_off` a parent only on an approved head that carries the merges of every child its criteria need"},
	{texts.CatchUp, "; in accepting, waiting for your acceptance, which no worker gives: %s (read the evidence, then sign_off or request_backward_move)", "architect (child)",
		goPrompts + "architect.md", "No worker runs there and the notice that moved it there may never have reached you, so read each one's evidence."},
	{texts.PRMerged, "Park one with park_child %s, and say in a comment which of those it is", "architect (root)",
		goPrompts + "architect-root.md", "Park one with `park_child` only when a person asked for that move, or when the work is done, folded into another issue or no longer needed, and say in a comment which of those it is."},
	{texts.PRMerged, "Park one with park_child %s, and say in a comment which of those it is", "architect (child)",
		goPrompts + "architect.md", "Park one with `park_child` only when a person asked for that move, or when the work is done, folded into another issue or no longer needed, and say in a comment which of those it is."},
	{texts.PullRequestMissing, "Tell its implementer (envoy_publish to its role topic) to open the pull request from %s; the daemon records it and moves %s to testing once it is open. ", "architect",
		goPrompts + "architect-common.md", "Tell the implementer with `envoy_publish` to its role topic to open the pull request from that branch; the daemon records it and moves the issue to `testing` once it is open."},
	{texts.PullRequestMissing, "When the work should not go on, decide %s's phase as for a pull request closed without merging.", "architect",
		goPrompts + "architect-common.md", "When the work should not go on, decide the issue's phase as for a `pr-closed-unmerged` notice."},
	{texts.MessageRefused, " settling that is yours — ask a person with `dispatch ask` to set the status on the issue.", "architect",
		goPrompts + "architect-common.md", "ask a person with `dispatch ask` to set that status on the issue, saying what the record reads"},
	{texts.MessageRefused, "; post what a person still needs yourself.", "architect",
		goPrompts + "architect-common.md", "what a person still needs is yours to post on the issue with `dispatch comment`."},
	{texts.ChildStatus, "Its own architect starts with it and owns it while that architect holds its role: its decomposition, its children and its acceptance. You hear its child-status and child-closed notices", "architect",
		goPrompts + "architect-common.md", "The child's own architect starts with it and owns it while that architect holds its role: its decomposition, its children and its acceptance."},
	{texts.ChildStatus, "Take %s back with a backward move to run %s under it now, or leave %s where it is and it starts once %s has closed.", "architect",
		goPrompts + "architect-common.md", "take the ancestor back with `request_backward_move` to run it under the ancestor now, or leave it to start at the ancestor's close."},
	{texts.ChildStatus, "It runs under its own architect, and its work reaches the tree of %s only through that tree's own issues.", "architect",
		goPrompts + "architect-common.md", "It runs under its own architect, and its work reaches your tree only through your tree's own issues."},
	{texts.ChildrenFirst, "Move a child to icebox %s, and cite that reason in a comment on the child. ", "architect",
		goPrompts + "architect-common.md", "move a child to `icebox` only when a person asked for that move, or when the work is done, folded into another issue or no longer needed, and cite that reason in a comment on the child."},
	{texts.ChildrenFirst, "%s is blocked_by %s, which stands above it on its own Dispatch parent chain, so neither can start until a person takes that link off", "architect",
		goPrompts + "architect-common.md", "neither can start until that link is gone: ask a person with `dispatch ask` to take it off, naming both issues."},
	{texts.CarriedGateOpened, "; any child waiting on it starts", "architect (root)",
		"skills/legion-architect/references/wake-routing.md", "the daemon has started any child waiting on it."},
	{texts.ChildNotRecorded, " Set its repository to one of the project's (dispatch issue-update --repo owner/name), then release it again.", "architect",
		"skills/legion-architect/references/wake-routing.md", "Set the child's repository (`dispatch issue-update --repo owner/name`), or get the named App installed on the repository it has, then release it again with `release_children`."},
	{texts.ChildNotRecorded, " Set a repository both Legion Apps are installed on, spelled as GitHub spells it (dispatch issue-update --repo owner/name), then release it again.", "architect",
		"skills/legion-architect/references/wake-routing.md", "Set the child's repository (`dispatch issue-update --repo owner/name`), or get the named App installed on the repository it has, then release it again with `release_children`."},
	{texts.ChildReleaseStalled, " Release %s first, then release %s again.", "architect",
		"skills/legion-architect/references/wake-routing.md", "release the parent first where it is not recorded, then run `release_children` for the child again once the parent is recorded in your tree"},
	{texts.ChildReleaseStalled, "; inspect the tree before trying again", "architect",
		"skills/legion-architect/references/wake-routing.md", "so inspect the tree before you try again."},
	{texts.ChildReleaseStalled, "its parent %s is not recorded: release %s first, then %s", "architect",
		"skills/legion-architect/references/wake-routing.md", "release the parent first where it is not recorded, then run `release_children` for the child again once the parent is recorded in your tree"},
	{texts.ChildReleaseStalled, "; install the App on it, or set a repository both Legion Apps are installed on, then release %s again)", "architect",
		"skills/legion-architect/references/wake-routing.md", "Where `refusal` is set and `for_now` is false, install the App the refusal names on the repository, or set a repository both Legion Apps are installed on, then release the child again."},
	{texts.ChildReleaseStalled, "; the daemon recovers the issues on it once GitHub answers, and records %s then if it is todo)", "architect",
		"skills/legion-architect/references/wake-routing.md", "Where `for_now` is true, the daemon records the child once GitHub answers for its repository and the child is `todo`."},
	{texts.ChildReleaseStalled, " in Dispatch now: release it again once it is todo)", "architect",
		"skills/legion-architect/references/wake-routing.md", "Otherwise, release the child again once it is `todo`."},
	{texts.WorkerDied, "%s's own architect failed; its notices come to you until it runs again, and the daemon starts it again at %s's next phase start", "architect",
		"skills/legion-architect/references/wake-routing.md", "The child's notices come to you until that architect runs again, and the daemon starts it again, with fresh budgets, at the child's next phase start."},
	{texts.WorkerDied, "the %s does not work %s's phase %s; nothing is held", "architect",
		goPrompts + "architect-common.md", "nothing is held and the phase runs on, so there is nothing to `retry_or_escalate`"},
	{texts.WorkerStalled, "The arguments of a call are never recorded, so they are in no part of this notice.", "architect",
		"skills/legion-architect/references/wake-routing.md", "and never a call's arguments, whose text the daemon keeps in no column."},
	{texts.WorkerSilent, ". A suspension an operator holds over the claim refuses that start when the outbox runs it", "architect",
		"skills/legion-architect/references/wake-routing.md", "The one thing that still refuses the start is a suspension an operator holds over the claim, which the outbox reads when it runs the row"},
	{texts.WorkerBackgroundStalled, "The daemon sent that worker nothing and will send it nothing for this: nothing can tell a background job that hung from a long one, so read what its session says it is waiting on and decide whether that work needs a decision of its own", "architect",
		"skills/legion-architect/references/wake-routing.md", "Your part is to read what the session says it is waiting on and to judge whether that work needs a decision of its own"},
	{texts.SessionLost, "It is running a fresh agent, which has read none of its own earlier turns; only what it pushed and committed survives.", "architect",
		"skills/legion-architect/references/wake-routing.md", "The daemon runs the role in a fresh agent, which has read none of its own earlier turns, and only what it pushed and committed survives."},
	{texts.SessionLost, "It is running a fresh agent, which has read none of its own earlier turns; only what it pushed and committed survives.", "controller",
		"skills/legion-controller/references/wakes.md", "read none of its own earlier turns, and only what it pushed and committed survives."},
	{texts.SessionLost, "It is running a fresh agent, which has read none of its own earlier turns; only what it pushed and committed survives.", "controller",
		"skills/legion-controller/SKILL.md", "Nothing: the role runs on in a fresh agent, which has read none of its own earlier turns, and only what it pushed and committed survives"},
	{texts.Unslotted, "; the daemon set its phase to done and starts nothing for it; todo with the legion label queues it again", "controller",
		"skills/legion-controller/references/wakes.md", "`todo` with the `legion` label queues it again"},
	{texts.AwaitingPerson, "%s waits on a person (%s): it keeps its admission slot until they answer", "controller",
		"skills/legion-controller/SKILL.md", "take nothing for its slot, which it keeps while it waits."},
	{texts.TreeStalled, "Read the tree and decide what it needs. You can ask a role what it waits on. You can move an issue back a phase. You can park the tree.", "controller",
		"skills/legion-controller/SKILL.md", "Read the tree and decide: ask a role what it waits on, move an issue back a phase, or park the tree only as [Rules](#rules) allows"},
	{texts.CISettled, " Nothing moved on it: decide whether to re-run the checks, wait for them, or fix the code.", "implementer",
		goPrompts + "implementer.md", "That message is yours to act on while the issue is still in a phase you work: re-run a check that failed on itself rather than on your code"},
	{texts.CISettled, " Nothing moved on it: decide whether to re-run the checks, wait for them, or fix the code.", "planner, tester, reviewer, merger",
		goPrompts + "worker-common.md", "whether it is re-run, waited for or fixed is a decision for the roles it reached"},
	{texts.CISettled, " Nothing moved on it: decide whether to re-run the checks, wait for them, or fix the code.", "architect",
		"skills/legion-architect/references/wake-routing.md", "the worker that holds the phase re-runs a red that failed on itself."},
	{texts.CISettled, " Nothing moved on it: decide whether to re-run the checks, wait for them, or fix the code.", "architect",
		goPrompts + "architect-common.md", "A red is yours to decide on — re-run it, wait for it, or have the implementer fix the code —"},
	{texts.GateMoved, "and every step the deployment gates now stands open, so the design you are working to is the approved one. A merge the closed gate held is released.", "planner, implementer, tester, reviewer",
		goPrompts + "worker-common.md", "so the design you work to is the approved one, and a merge that the closed gate held is released."},
	{texts.GateMoved, "so the design you are working to may change. Your phase goes on; the merge waits until every gated step stands open again.", "planner, implementer, tester, reviewer",
		goPrompts + "worker-common.md", "the design you work to may change, your phase goes on, and the merge waits until every gated step stands open again."},
	{texts.GateMoved, "so the design you are working to may change. Your phase goes on; the merge waits until every gated step stands open again.", "merger",
		rolePrompts + "merger.md", "so the design this head implements may change: merge nothing, enqueue nothing, and do not complete while the gate is closed."},
	{texts.GateMoved, "so the design you are working to may change. Your phase goes on; the merge waits until every gated step stands open again.", "architect (root)",
		goPrompts + "architect-common.md", "the design may change, the phases go on, and the merge waits until every gated step stands open again."},
	{texts.ApprovedRoundStuck, " The issue waits in reviewing until GitHub reports the head clean, unstable or has_hooks", "reviewer",
		goPrompts + "reviewer.md", "The issue waits in `reviewing` until GitHub reports that head `clean`, `unstable` or `has_hooks`."},
	{texts.ApprovedRoundStuck, "The daemon reads no ruleset and so names no required check: …, which makes the missing check the thing to look for.", "reviewer",
		goPrompts + "reviewer.md", "The daemon reads no ruleset, so a required check that never ran is absent from it, and that missing check is the thing to look for."},
	{texts.MemoryKill, "The pod's other processes and your session go on. ", "planner, implementer, tester, reviewer, merger",
		rolePrompts + "mechanics/headless.md", "With `cause: guard` or `cause: kernel`, the pod's other processes and your session go on."},
	{texts.MemoryKill, "The pod's other processes and your session go on. ", "architect",
		goPrompts + "architect-common.md", "With `cause: guard` or `cause: kernel`, the pod's other processes and your session go on."},
	{texts.MemoryKill, "Heavy work — a build, a whole test suite, anything that needs much memory — runs where your deployment's instructions say, not in this pod: read skill://legion-worker/references/tools.md before you run it again.", "planner, implementer, tester, reviewer, merger",
		rolePrompts + "mechanics/headless.md", "Before you run heavy work again, read `skill://legion-worker/references/tools.md`: heavy work runs where your deployment's instructions say, not in this pod."},
	{texts.MemoryKill, "Heavy work — a build, a whole test suite, anything that needs much memory — runs where your deployment's instructions say, not in this pod: read skill://legion-worker/references/tools.md before you run it again.", "architect",
		goPrompts + "architect-common.md", "Before you or a subagent of yours runs heavy work again, read `skill://legion-worker/references/tools.md`."},
	{texts.MemoryKill, "It was one of the processes that run your session, not a command you ran. If it was your agent, your turn ended with it, and so did every other process of your role. The daemon then starts the same session again and sends again any task that the turn was running. It counts each such exit of a role's process as a failed launch, and at the limit of failed launches it fails your claim instead. ", "planner, implementer, tester, reviewer, merger",
		rolePrompts + "mechanics/headless.md", "It counts each such exit as a failed launch, and at the limit of failed launches it fails your claim."},
	{texts.MemoryKill, "It was one of the processes that run your session, not a command you ran. If it was your agent, your turn ended with it, and so did every other process of your role. The daemon then starts the same session again and sends again any task that the turn was running. It counts each such exit of a role's process as a failed launch, and at the limit of failed launches it fails your claim instead. ", "architect",
		goPrompts + "architect-common.md", "The daemon then starts the same session again, sends again any task that the turn was running, and counts the exit as a failed launch."},
	{texts.PhaseEnded, " Nothing of yours is stopped: your turn, your jobs, your process and your session run on.", "tester, reviewer, merger, implementer",
		goPrompts + "worker-common.md", "Nothing of yours is stopped: your turn, your tool calls, your background jobs, your process and your session all run on."},
	{texts.PhaseEnded, " The workspace is the %s phase's now, so stop writing it at once and leave whatever you have uncommitted.", "tester, reviewer, merger, implementer",
		goPrompts + "worker-common.md", "so stop writing the workspace the moment that message reaches you and leave whatever you have uncommitted"},
	{texts.PhaseEnded, " Your %s task follows.", "implementer",
		goPrompts + "worker-common.md", "your implementing task follows, and it arrives as your next prompt once this turn ends."},
	{texts.UpdateAsk, "2. If you need a scratch file as evidence, put it on Dispatch.", "planner, implementer, tester, reviewer, merger",
		goPrompts + "worker-common.md", "If you need a scratch file as evidence, put it on Dispatch, because the disk goes with the pod."},
	{texts.UpdateAsk, "2. If you need a scratch file as evidence, put it on Dispatch.", "architect",
		goPrompts + "architect-common.md", "If you need a scratch file as evidence, put it on Dispatch, because the disk goes with the pod."},
	{texts.UpdateAsk, "These things go with the pod when the round ends:", "planner, implementer, tester, reviewer, merger",
		goPrompts + "worker-common.md", "When the round ends, these go with the pod, and the message's `goes_with_pod` names them: the turn in progress, the background jobs of that turn, each commit and each file that you did not push, the pod's test database and the pod's caches."},
	{texts.UpdateAsk, "These things go with the pod when the round ends:", "architect",
		goPrompts + "architect-common.md", "When the round ends, these go with the pod, and the message's `goes_with_pod` names them: the turn in progress, the background jobs of that turn, each commit and each file that you did not push, the pod's test database and the pod's caches."},
	{texts.UpdateAsk, "Do not start new work now.", "planner, implementer, tester, reviewer, merger",
		goPrompts + "worker-common.md", "Start no new work after you answer."},
	{texts.UpdateAsk, "Do not start new work now.", "architect",
		goPrompts + "architect-common.md", "Start no new work after you answer."},
	{texts.MergeQueueRemoval, " Read the pull request and its removals on GitHub, as step 5 of your role says.", "merger",
		rolePrompts + "merger.md", "Read the entries to the queue and the removals from it with this command."},
	{texts.MergeQueueRemoval, " The daemon could not read the pull request from GitHub. So it does not know if GitHub merged it.", "merger",
		rolePrompts + "merger.md", "`unread: true` means that the daemon could not read the pull request from GitHub, so it does not know if GitHub merged it."},
}

// goldenRepositoryRoot is the repository's root, from this package's directory.
const goldenRepositoryRoot = "../../../.."

// goldenDeploymentWords is what a passage may not name: a number, a date or a time (a digit), an
// issue key, or a month.
var goldenDeploymentWords = regexp.MustCompile(`[0-9]|\b(January|February|March|April|May|June|July|August|September|October|November|December)\b`)

// The check c-daemon-texts-are-facts, fourth test: each sentence this change took out of a daemon
// text, for each of its readers, is given by a passage of that reader's prompt or of a skill it
// loads, quoted word for word on one line of its file. No golden sample holds such a sentence
// outside what R8 keeps and another author's words, and no passage names a number, a date, a time
// or a project of one deployment.
func TestEveryStepTakenOutOfATextIsInItsReadersPrompt(t *testing.T) {
	files := map[string][]string{}
	for _, step := range goldenSteps {
		if _, read := files[step.file]; !read {
			body, err := os.ReadFile(filepath.Join(goldenRepositoryRoot, filepath.FromSlash(step.file)))
			if err != nil {
				t.Fatalf("read %s: %v", step.file, err)
			}
			files[step.file] = strings.Split(string(body), "\n")
		}
		if !slices.ContainsFunc(files[step.file], func(line string) bool { return strings.Contains(line, step.passage) }) {
			t.Errorf("%s does not hold, on one line, the passage that gives the %s the step of %q (%s): %q",
				step.file, step.readers, step.sentence, step.kind, step.passage)
		}
		if found := goldenDeploymentWords.FindString(step.passage); found != "" {
			t.Errorf("the passage of %s for the %s names %q, a number, date, time or project of one deployment: %q",
				step.file, step.readers, found, step.passage)
		}
	}

	held, err := os.ReadFile(goldenTextsPath)
	if err != nil {
		t.Fatalf("read the golden file: %v", err)
	}
	var golden goldenFile
	if err := json.Unmarshal(held, &golden); err != nil {
		t.Fatalf("decode the golden file: %v", err)
	}
	if len(golden.Samples) == 0 {
		t.Fatal("the golden file holds no sample")
	}
	for _, sample := range golden.Samples {
		said := goldenSaid(t, sample)
		for _, step := range goldenSteps {
			if goldenSentence(step.sentence).MatchString(said) {
				t.Errorf("%s holds %q, the step of %s that the %s's prompt gives now (%s)", sample.id(), step.sentence, step.kind, step.readers, step.file)
			}
		}
	}
}

// goldenSaid is what a golden sample says in the daemon's own words: a task's text but the spans
// of R1's blocks and of the sentences R8 keeps, or a payload's every string but those its words
// object names, which are another author's.
func goldenSaid(t *testing.T, sample goldenSample) string {
	t.Helper()
	if sample.Form == texts.Task {
		units := utf16.Encode([]rune(sample.Text))
		var said []uint16
		at := 0
		for _, span := range sample.Spans {
			said = append(said, units[at:span.Start]...)
			at = span.End
		}
		said = append(said, units[at:]...)
		return string(utf16.Decode(said))
	}
	var payload any
	if err := json.Unmarshal(sample.Payload, &payload); err != nil {
		t.Fatalf("decode the payload of %s: %v", sample.id(), err)
	}
	if text, ok := payload.(string); ok {
		return text
	}
	values := map[string]string{}
	goldenFlatten(payload, "", values)
	words := map[string]bool{}
	for path := range values {
		if rest, ok := strings.CutPrefix(path, "words."); ok {
			field, _, _ := strings.Cut(rest, ".by")
			field, _, _ = strings.Cut(field, ".at")
			field, _, _ = strings.Cut(field, ".rest")
			field, _, _ = strings.Cut(field, ".more")
			words[field] = true
		}
	}
	var said strings.Builder
	for _, path := range slices.Sorted(goldenKeys(values)) {
		if strings.HasPrefix(path, "words.") || words[path] {
			continue
		}
		said.WriteString(values[path])
		said.WriteString("\n")
	}
	return said.String()
}

// goldenVerb is a format verb or an elision of a sentence of the table.
var goldenVerb = regexp.MustCompile(`%[sdqv]|…`)

// goldenSentence is a sentence of the table as a pattern: its fixed parts word for word, trimmed
// of the spaces at its ends, with any text where a format verb or an elision stands.
func goldenSentence(sentence string) *regexp.Regexp {
	parts := goldenVerb.Split(strings.TrimSpace(sentence), -1)
	for i, part := range parts {
		parts[i] = regexp.QuoteMeta(part)
	}
	return regexp.MustCompile(strings.Join(parts, `(?s:.*?)`))
}
