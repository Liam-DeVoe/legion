# An issue's plan

Part of `skill://legion-architect`, sections 1 and 2.
Read it before you write or give an issue's plan.
Stopping a task, a later version of a plan Legion runs, and the `task-stopped`, `retro-held` and `plan-not-taken` notices:
`skill://legion-architect/references/plan-changes.md`.

## The task block

You choose which of Legion's steps each issue of your tree runs.
The plan is the issue's `plan.md` document (`dispatch artifact` on the issue).
In this release it holds one `plan_task` block, the issue's one task:

```markdown
:::plan_task{#a steps="implementer reviewer"}
What the task must deliver. What the steps after the first need to know, since no planner step
writes it for them. For each step, a fresh agent or the agent of an earlier step, and why.
:::
```

- Give the block `serves` naming the spec's `requirement` blocks, as a planner's task does, so the issue's coverage counts it.
  A change of `serves` alone changes no task.

- Steps that share a key, `<role>:<key>`, are one agent in one session, and a step with no key has an agent of its own.
  To have one agent work several steps, give each of them the same key after its role, as `steps="implementer:a tester:a reviewer"`: Legion runs the steps of one key in one claim and one session, whatever role each step is on.
  A key other than `owner` may stand on any role but `acceptance`, and `owner` on `acceptance` alone.
  A role may stand at two steps, of one agent where both steps carry one key, or of two.
  Each agent is named for the role of its first step, and an agent whose role's name another agent of the plan holds takes a numbered name of its own:
  `steps="implementer reviewer implementer"` runs step 3 as a fresh agent, `implementer-2`, in a claim and a session of its own.
  Under `runtime: kubernetes` that agent runs in a slot of the issue pod that carries its name, and where the pod was built without that slot, step 3 waits until the pod's update round or its release replaces the pod.
  A key keeps its name in every version: a later version that names the key again runs its steps in the same agent and session, and while a version leaves the key out, no other agent takes its name.
  Write in the task's text whether each step needs a fresh agent or an earlier one, and why.
  Retro rates the plan's steps, and the agent that worked each, against that choice.

## The roles and their order

| Step | Type | Phase | What leaving it out means |
| --- | --- | --- | --- |
| `planner` | planning | `planning` | No implementation plan and no plan review: the steps work from your task's text and the spec. |
| `implementer` | implementation | `implementing` | Required. It also works the retro, and a failed step returns to it unless the failed step names an earlier one. |
| `tester` | testing | `testing` | Only the implementer records results against the checks, and nobody runs them again. |
| `reviewer` | review | `reviewing` | No review: on a base whose rules require an approving review, the merger waits and tells you. |
| `acceptance:owner` | review | `accepting` | Your step. Without it the last step's pass starts the retro, and nothing waits for you to read the evidence. |

Without `acceptance:owner`, two steps still hold the checks:
a reviewer step approves only once a pass stands for every `before_merge` check at the head, and retro's first step sends back a check with no pass standing there, except a `ci_check` whose run has not concluded or cannot be read, which moves nothing back (`skill://legion-retro`).
So on a task with no reviewer step and no `acceptance:owner`, such a check holds the merge only where it is among the checks the base branch requires.

## A role of your plan

A plan can define a role of its own in a `plan_role` block, which a step names by its name:

```markdown
:::plan_role{#prober type="testing" model="<a listed model>"}
The role's prompt, which can hold headings, lists and code.
:::

:::plan_task{#a steps="implementer prober reviewer"}
What the task must deliver.
:::
```

- The block's id is the role's name, and its text is the role's prompt.
  The role's agent runs on Oh My Pi and starts with that prompt.

- `type` is the type of the role's step, as the table above gives each of Legion's; a role with no `type` is taken too.
  A role of the type implementation acts as the implement App, and a role of any other type, or of none, as the review App.
  No role but `implementer` of the type implementation stands after the first `implementer` step, since the implementer also works the retro.

- `model` is the model the role's agent starts on, one your addressing lists among the models of this deployment.
  A role with no `model` starts on the default model, and where the deployment lists no model, a role of your plan names none.

- A role's prompt has no bound of its own, but a launch into an issue pod carries the whole system prompt.
  It goes in one message of at most 1 MiB.
  Your role prompt says what Legion does with a larger one, and how you change the role then.

### A role under a running task

- A later version that changes the type, the model or the prompt of a role block that a step of the running task names is refused (`task <id> is running: stop it with stop_task before you change it.`).
  Stop the task with `stop_task` first, then give the version.
  A change of a role block that no step names changes no task.

- A later version that takes away a role block that a step still names is refused whether the task runs or not (`… which neither the deployment nor the plan defines.`), and stopping the task does not help.
  Keep the block, or change the task's steps too, and then the rule above for a running task applies.

- Once an operator adds a role of the deployment with your role's name, your issue keeps its plan's role, but Legion refuses every later version that keeps a role of that name (`… which the deployment or Legion defines already; …`).
  A version that gives the role a new name changes each task whose steps name it, so Legion refuses it while such a task runs:
  stop that task with `stop_task` first, then give a version whose role has a new name.

## What Legion refuses

`register_plan` answers `409 PLAN_REFUSED` with `reasons: [{task, reason}]`, one reason for each cause, its `task` empty for a cause of the whole plan.
Fix each reason and give a new version.
A role block is refused for a name that is not of the form `[a-z][a-z0-9-]*` with at most 32 characters.
It is refused for a name Legion or the deployment defines already (`… which the deployment or Legion defines already; a role of a plan takes a name of its own.`), and for a `type` that is set and is not of that form (`… Dispatch's step record takes no other.`).
A role block whose `model` the deployment does not list is refused at the first step of each task that names it (`step <n> of task <t> names the role <role>, whose model <m> the deployment does not list; it lists <m1>, <m2>.`, or `it lists none.`).
Such a block that no step names is refused as a cause of the whole plan (`the plan defines the role <role> with the model <m>, which the deployment does not list; it lists <m1>, <m2>.`).
A step that names a role neither the deployment nor the plan defines is refused (`step <n> of task <t> names the role <role>, which neither the deployment nor the plan defines.`).
The steps of one key, `<role>:<key>`, are one agent.
So a key whose steps name roles of two models is refused (`… one agent runs on one model.`), and so is one whose steps name roles of two harnesses (`… one agent runs on one harness.`).
A reason that ends `This release of Legion does not carry it out yet.` names a plan a later release takes: write it again within the table above.

Your role prompt gives the reasons a version gets for where its task stands.

## When to give it

- **A root:** before `register_gate`, because the gate's opening starts the root's first step.

- **A child:** before `release_children` releases it.
  The daemon has not recorded the child yet, so Legion keeps the plan until the release;
  the answer, as for any plan it takes, is the plan's `version` and each task with its `steps`.
  A child the daemon has not recorded is refused once its Dispatch status is none of `triage`, `backlog`, `icebox` and `todo` (`ISSUE_ALREADY_RELEASED`).
  The child's first step can start in the same admission pass as its release, so a plan given after it comes too late.

A `plan.md` that a planner of the default steps wrote holds that planner's task blocks.
The version you give replaces them with your own one block: delete theirs, and put what the next steps still need from the planner's plan into your task's text.

## Reading how plans went

`dispatch steps --issue <KEY>` lists each run of each step of an issue and how it ended.
Each finished issue of a project, with its plan's source and steps, its retro's ratings, its time and its count of failed steps, is `dispatch plans --project <KEY>`.
Read it before you choose the steps of an issue like one that finished.
