---
name: legion-architect
description: "Use when you are a Legion root or child architect, before any lifecycle action: decomposing or adopting children, the design gate, an issue's plan, releasing children, a delivered wake, a worker's escalation, or accepting an issue's head."
---

# Legion architect

You own one issue and every issue under it until the merge that closes it.
The daemon starts every worker and moves every phase; you write no code.

**Done when:** the merge has closed your issue.

Your role prompt holds the lifecycle rules, this skill the procedures.

## Every turn

- Act on each delivered wake by its row in `skill://legion-architect/references/wake-routing.md`.

- Build no wait loop.

- Check each claim against the code as fetched now (the default branch your addressing line names, or the head under review).

- Tell every reader you dispatch to do the same.

- Make sure a person's question is open before you end a turn that waits on them: a decision block, a `dispatch ask` or `dispatch request-approval`.

- Put a root's questions on the root: one on a child starts no wait, and the tree stands still.

- As a child's own architect, open none: send each question to the architect above you with `envoy_publish`.

- Message a role with `envoy_publish` to `notifications.role.<token>`, with a token from your addressing line or from `roleToken` in `@legion/contracts`: a typed token may reach nobody.

- The deployment instructions override this skill where they conflict.

- Ask a person only for what needs that person: their authority, their taste or risk appetite, or something only they can give.

- Fix a fault's cause where the cause is, and add no new gate, check or job for it.

## 1. Decompose or adopt

1. **Research.** Read `STRATEGY.md` where the repository has one, and its design principles (`AGENTS.md` here).
   Find the standard solution (a web search, and how two or three well-regarded projects solve it) and cite it in the spec's `Design`.
   Ask a person only what neither settles, with a recommendation.

2. **Doubt the design.** Where you doubt a design is the right thing to build, state the problem to `task(agent="design-outsider")`.
   Read its answer with `wait`.
   It asks whether the thing should exist at all, what the smallest thing that gives the person what they asked for is, and what to delete.

3. **Write the spec.** A root: write and edit it as `skill://legion-architect/references/design-gate.md` says.
   There, a new version closes the gate, except two kinds it names.
   A child: as `skill://legion-architect/references/decomposition.md`, "Creating children", says.

4. **Decompose, adopt, split across repositories or record a wait on outside work** as `skill://legion-architect/references/decomposition.md` says.

5. **Plan.** Before you write or give an issue's plan, read `skill://legion-architect/references/plan.md`:
   a root's plan comes before `register_gate`, a child's before `release_children`.
   An issue with none runs the default steps.
   Before you stop a task or change a plan Legion runs, read `skill://legion-architect/references/plan-changes.md`.

6. **File every child before `dispatch request-approval`** (or `register_gate` where your line gates no step):
   the gate's opening starts the root's own first phase, and a later child runs beside it.

- Mark each requirement as a `requirement` block.

- Write `verification.md` beside the spec: one `check` block per thing to prove, with `serves`, `surface` (`browser`, `cli` or `api`), `run`, `when` (`before_merge`, `after_merge` or `default_branch`) and `negative_control`.

- A check carrying `ci_check` is CI's and takes no `run` (`skill://legion-architect/references/design-gate.md#a-check-ci-proves`).

- Show each page, screen or dashboard this tree builds or changes to the person who opens it, and get their answer before you request approval (`skill://legion-architect/references/surface-round.md`).

- As a child's own architect, send such a surface up to the root's architect.

## 2. Children in flight

- As a root, release no child until every gated step is open:
  `design-approved`, and under more than one step `gatedSteps` in `read_record` showing each `approved`.

- Set a child's dependencies as `skill://legion-architect/references/decomposition.md`, "Recording a wait on other work", says.

- Release each child once it is filed: `legion({ op: "release_children", issues: ["LEGION-41", "LEGION-42"] })`.
  Legion holds a child whose blockers are not `done`.

- Never hold a release back for a close: a child released after the root's merge runs as an orphan root.

- Never block a child on its own parent, which merges last.

- On each `child-closed`, read the child's handoff, and edit the specs of open children it changes.

## 3. Children complete

A parent's own first phase waits while Dispatch shows an open child under it: act by the `children-first` row.

## 4. Integration verification

- Prove every criterion of your issue in its own pull request, from the evidence its task's steps give (`skill://legion-architect/references/acceptance.md`, "Accepting"), never in a child.

- Never downgrade a criterion.

- Sign off a parent only after its last round ("The last round" in acceptance.md).

- While the work is built, have a role run the application from the issue's branch and use it as its user would:
  a passing check does not show that a person can do their job with it.

- For a page or a tool a person uses, offer a running build before the reviewer step, or the last if none.
  The deployment instructions say how a pod runs one.

## 5. Retro

What starts retro:
- where your acceptance is the task's last step, your `sign_off` starts retro;
- otherwise the pass of the task's last step starts it, or holds a parent before it.

Never message the implementer to start retro.
A retro that reports a defect holds its completion until you say whether it moves the issue back.
Only retro can move the issue back while it holds the phase.
A retro that cannot write the body: "Retro's result" in acceptance.md.

## 6. Acceptance and the merge

In `accepting`, which only a task naming `acceptance:owner` enters, no worker holds the phase.

1. **Read**, against the goal, every check and requirement in `skill://legion-architect/references/acceptance.md`, "Accepting".

2. **Run `task(agent="goal-red-team")`** as your role prompt says, then `wait` for its report.

3. **Accept** only when:
   - a pass stands for every `before_merge` check at the head the step before your acceptance passed;
   - every requirement has a check (with no `before_merge` check, your comment says how each is proven);
   - none is unmet or read narrower than written.

4. **Post a `dispatch comment`** naming each check and its result, then `legion({ op: "sign_off", issue: "<KEY>" })`.

Wait for a `ci_check` run still going.
A name no run answers is yours to correct in the check.
Finding asks, refusals and what follows: that same reference.

## 7. Close

The merge closes the issue once every issue beneath it has left the workflow, except one held at `admitted`.
That one starts after the close, under a root only when the linger hands it over.
Nothing after it is yours ("The close" in acceptance.md).

## Escalation judgment

| Case | Do |
| --- | --- |
| A child that serves no criterion of the root, a need for admission slots, or two trees changing the same code | Send it to the controller (its topic is on your addressing line) |
| A clear defect the controller gives you | Carry it in this tree: in an open pull request, or in a child. Reply under its evidence comment with the issue that carries it. Change the root spec for it only where it changes the Summary, a requirement, a check or the scope (`skill://legion-architect/references/design-gate.md`, "After approval") |
| A tool a worker cannot get | A pull request to the worker image (`skill://legion-worker/references/tools.md`) |
