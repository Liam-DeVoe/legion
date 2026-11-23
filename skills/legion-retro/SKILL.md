---
name: legion-retro
description: "Use when the Legion daemon starts you, the implementer, on `Phase: retro` after the architect accepted the reviewed head."
---

# Legion retro

Retro runs on the reviewed head, between the architect's acceptance and the merger's merge.

**Done when:** each of these holds before you complete:

- `jj -R "$LEGION_WORKSPACE" diff --from "legion/${LEGION_ISSUE:?}@origin" --to @ --name-only`
  lists nothing.
- Each body line you added or changed that says work was done names work this pull request did.
- The comment and the message are posted.
- The pull request's head is the head you found.
- Your `handoff_complete` carries `plan_rating` and `plan_reason`, or neither where your `legion`
  tool's `handoff_complete` takes neither.

`<issue>` is your `LEGION_ISSUE`. Run every step, even on a mechanical change.

- **Commit nothing and push nothing.** The merger merges the branch's head. A push would put a
  commit above the head the reviewer approved. Outside a forward merge of the base, no Legion check
  compares the head that merges with the one the review approved.
- **Make no other branch or bookmark.**
- **Write no handoff.**
- **Change nothing in the workspace.**
- **Outside the workspace, write only** step 4's `mktemp -d` directory, the PR body and the
  Dispatch comment and message.
- **Set no GitHub or Dispatch label:** Legion keeps phase state in the daemon.
- **Record what the work taught as a `dispatch comment` for the skill librarian.** No role writes
  to `docs/solutions/`.

## When retro stops

- **Do not call `handoff_complete` when a step fails** (the body cannot be computed, read or
  written, or Dispatch refuses a write). Send the command and its output to the architect's topic
  on your `Legion addressing` line.
- **Send a defect you find in the work to the architect with its evidence.**
- **Hold your completion until the architect answers:** `handoff_complete` takes away
  `request_backward_move`, which is yours while retro holds the phase.
- **End each turn of that hold with a WAITING line** naming the defect and the answer you wait on.

## Steps

1. **Read the evidence.** Read the issue, `dispatch results --issue <issue>`, the pull request, the
   tester's handoff (`dispatch handoff-read --issue <issue> --phase test`) and the review at the
   version your task's `Handoff:` line names (`--phase review --version <n>`).
   - **A pass must stand at the approved head for every `before_merge` check.** The body's
     `Checks` block carries each.
   - **A check carrying `ci_check` is CI's:** its derived result is the pass it needs
     (`skill://legion-worker/references/ci-checks.md`).
   - **A `ci_check` check has no pass standing at that head** when its derived result is a fail,
     or its run concluded `skipped`, `neutral` or `cancelled`.

   | The check | Do |
   | --- | --- |
   | A `ci_check` check whose run has not concluded | Wait for the run |
   | A `ci_check` check where GitHub could not be read | Read again |
   | A `ci_check` name that no run answers (`absent`) | Send it to the architect as a finding, since the check's words are wrong |
   | A check whose `run` is a whole test suite | The same |
   | A `ci_check` check whose derived result is a fail | It is the merger's: the change is accepted. The merger decides at the merge whether the base or the change broke it. Name it in your retro message and complete |
   | Any other check with no pass standing, or a check with no `ci_check` whose only evidence is a unit suite | `request_backward_move` with `to: implementing` for the implementer's result, or `to: testing` for the tester's, its `reason` naming the check's block id |

   Neither the wait nor that finding to the architect moves the issue back.
2. **Get a second reading.**
   - **First, list in your session** the constraints the issue did not state, choices between two
     working options, abandoned approaches and reusable code.
   - **Then run `task(agent="oracle")`** with the issue, the pull request body and the diff, asking
     for reusable learnings. Wait for its answer with `wait`.
   - **Drop a point that cites no file, symbol, quoted text or commit of this pull request.**
3. **Record the learnings** as one `dispatch comment` on the issue, per
   `skill://legion-retro/references/learnings.md`, which says what a point must carry for the
   skill librarian to use it.
   - **When no point qualifies, write no comment.** Say so in step 5's message.
4. **Update the PR body**, per `skill://legion-retro/references/pr-body-content.md`, when the
   repository derives body lines from changed paths.
5. **Post one Dispatch message on the issue**, at most 32,000 characters:

   ```bash
   dispatch message --issue '<KEY>' --body-file - <<'EOF'
   ## Retro Complete

   **Learnings:** the comment of step 3 (`dispatch://<KEY>/comment/<id>`), or none: <why no point qualified>.

   **Key takeaways:**
   - <one to three reusable lessons>

   **Results read:** implementer <check and result>, tester <check and result>.

   **Production check:** <the command or user action that will show the change working in production>
   **Carried by:** <the deploy or restart that carries the merge there>
   **Run by:** <the agent or role your system prompt's deployment instructions name; when none, the person to ask>
   EOF
   ```

   - **Never post it with a `gh issue` write.**
   - **Put no `<!-- legion: … -->` footer on a Dispatch comment or message.** Dispatch records the
     session that wrote it. Only a pull-request comment or review needs the footer.
6. **Complete** with the `legion` tool's `handoff_complete`. Make its summary two sentences for
   the architect.
   - **In the same call, rate the plan the issue ran:** its steps and the role that worked each,
     not the planner's `plan.md`.
   - **Send both fields below, or neither where your `legion` tool's `handoff_complete` takes
     neither field.**

   | Field | Value |
   | --- | --- |
   | `plan_rating` | A whole number from 1 (the plan cost the work) to 5 (the plan fit the work) |
   | `plan_reason` | One sentence of why, from step 1's evidence |

   | The daemon refuses a completion | Code |
   | --- | --- |
   | Without `plan_rating` and `plan_reason` | `PLAN_RATING_REQUIRED` |
   | With one alone, a rating off that scale, or a reason blank or over 2,000 characters | `PLAN_RATING_INPUT` |
   | With both, on any other completion | `PLAN_RATING_UNEXPECTED` |

   A refusal records nothing: complete again.
