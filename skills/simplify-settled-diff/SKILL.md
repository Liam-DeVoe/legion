---
name: simplify-settled-diff
description: "Use when implementation is done and before review, to simplify recently changed code for clarity, reuse, quality and efficiency without changing its behavior."
argument-hint: "[blank to simplify current branch changes, or describe what to simplify]"
---

# Simplify settled diff

Simplify recently changed code for clarity, reuse, quality and efficiency while preserving exact behavior. Prefer readable, explicit code to compact code: fewer lines is not the goal.

**Done when:** each failure the simplification caused is fixed or its change reverted, and you have summarized the pass as Step 5 says.

## Step 1: Resolve the scope

1. A scope the user names is the scope. Do not widen it.
2. Otherwise, in git, use the current branch against its base. Without a usable base, use `git diff HEAD`.
3. Outside git or without a diff, use the files the user named or that were edited earlier in the conversation.

- **If none of these gives a non-empty scope, ask the user what to simplify.** Ask with the host's question tool where it has one, else as numbered options in chat.
- **Where the host lists a question tool that is not loaded, load it** with the host's tool-discovery primitive before you fall back to chat.
- **Never skip the question.**
- **Every file of the scope is in the pass:** documentation, workflow and configuration files as much as code.
- **Edit no generated, vendored or lockfile content.** Read it for what it shows.
- **Size is no gate:** callers own any size or cost threshold.

## Step 2: Dispatch three reviewers in parallel

Dispatch three subagents. Give each the full text of its prompt file, read from this skill's directory and passed verbatim, and the whole resolved scope (the full diff or file set):

- `references/personas/code-reuse-reviewer.md`
- `references/personas/code-quality-reviewer.md`
- `references/personas/efficiency-reviewer.md`

| Host | Do |
| --- | --- |
| Oh My Pi | Dispatch each as the bundled reviewer, `task(agent="reviewer")`, and `wait` for each result |
| Elsewhere | Use the host's subagent tool (`Agent`/`Task` in Claude Code, `spawn_agent` in Codex), or run the reviews inline |

- **Omit the `mode` parameter,** so the user's permission settings apply.
- **A concurrency-limit error is backpressure:** retry when a slot frees.
- **When a dispatch fails for any other lasting reason, run that reviewer inline with the same prompt.** Say so in one line.

## Step 3: Apply the worthwhile findings

Start only after all three reviews are complete. Without asking the user, apply the worthwhile findings. Record false positives and low-value findings as skipped, without asking the user.

- **Edit only the scope and its necessary import and export seams.** Read beyond the scope to judge a finding. For a scope the user named, those seams must be inside it too.
- **Each fix keeps outputs, errors, side effects and ordering.** If you cannot show that, skip it.
- **Never thin or remove a safety check:** trust-boundary validation, data-loss protection, security checks, accessibility affordances.
- **An interface or data shape that existed only in an earlier iteration of this unshipped scope is not protected** once you verify it has no deployed, persisted, public, external, dependent-branch or in-repo caller outside the scope. Remove it only when every caller update fits the scope.
- **A plan the caller passed as context is not scope.** Keep each decision it marks `session-settled:`, including a duplication or a separation made on purpose.

**Report a defect you see; never drop it.** Put a problem this pass may not change (a wrong procedure in a document, a workflow step that cannot work, a check that cannot fail) in the summary with its file, its line and what is wrong, never as out of scope. The caller fixes it outside the pass.

## Step 4: Verify the fixes

- **Only when a fix was applied:** run the typecheck, lint or build of the packages the fixes touched, and the tests that cover the changed code, by name. Run no whole test suite.
- **With no fix applied,** record `no fix applied; head unchanged, no verification run` and go to Step 5.
- **Report a failure with the check name and its output.**
- **Fix a failure the simplification caused, or revert that change.** Never relax an assertion, weaken a type or skip a test.
- **If no test, lint or typecheck is configured,** say so in the summary.

## Step 5: Summarize the pass

Say what was already sound and what improved. Report applied counts by reuse, quality and efficiency; the skipped count; each defect reported but not fixed, with its file and line; and the check outcomes. Name the agent or model that produced each reviewer's findings. If nothing changed, say so. Lines removed is not the measure of success.
