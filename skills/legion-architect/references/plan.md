# An issue's plan

Part of `skill://legion-architect`, sections 1 and 2.
Read it before you write or give an issue's plan.
Stopping a task, a later version of a plan Legion runs, and the `task-stopped`, `retro-held` and `plan-not-taken` notices:
`skill://legion-architect/references/plan-changes.md`.

## The task block

You choose which of Legion's steps each issue of your tree runs.
The plan is the issue's `plan.md` document (`dispatch artifact` on the issue, with `--force` once `plan.md` exists).
In this release it holds one `plan_task` block, the issue's one task:

```markdown
:::plan_task{#a steps="implementer reviewer"}
What the task must deliver. What the steps after the first need to know, since no planner step
writes it for them. For each step, a fresh agent or the agent of an earlier step, and why.
:::
```

- `steps` names the task's steps in the order they run, separated by spaces.
  Legion runs the retro and the merge after the last step itself, so no plan names them.

- Give the block `serves` naming the spec's `requirement` blocks, as a planner's task does, so the issue's coverage counts it.
  A change of `serves` alone changes no task.

- A block with no `steps`, or `steps=""`, runs the project's default steps, `projects.<KEY>.default_steps` in `legion.yaml`: `planner implementer tester reviewer
  acceptance:owner` unless the project sets others.

- This release gives each role one agent for each issue, the session that role keeps until the issue leaves the workflow.
  It cannot carry out your choice of a fresh or an earlier agent yet; write the choice all the same, because it records what the work needed.

## The roles and their order

The steps are some of these five, in this order, with `implementer` among them:

| Step | Type | Phase | What leaving it out means |
| --- | --- | --- | --- |
| `planner` | planning | `planning` | No implementation plan and no plan review: the steps work from your task's text and the spec. |
| `implementer` | implementation | `implementing` | Required. It also works the retro, and a failed step returns to it unless the failed step names an earlier one. |
| `tester` | testing | `testing` | Only the implementer records results against the checks, and nobody runs them again. |
| `reviewer` | review | `reviewing` | No review: on a base whose rules require an approving review, the merger waits and tells you. |
| `acceptance:owner` | review | `accepting` | Your step. Without it the last step's pass starts the retro, and you read no evidence before it. |

Without `acceptance:owner`, two steps still hold the checks:
a reviewer step approves only once a pass stands for every `before_merge` check at the head, and retro's first step sends back a check with no pass standing there, except a `ci_check` whose run has not concluded or cannot be read, which moves nothing back (`skill://legion-retro`).
So on a task with no reviewer step and no `acceptance:owner`, such a check holds the merge only where it is among the checks the base branch requires.
An issue with no `before_merge` check has neither gate, so give it that step: your acceptance is then its only gate.
Give it to a parent with children too, because a parent's last round runs from your acceptance (`skill://legion-architect/references/acceptance.md`, "The last round"):
without it, Legion holds the parent before its retro until its children leave, then starts the retro at the head the parent stood on.

## What Legion refuses

`register_plan` answers `409 PLAN_REFUSED` with `reasons: [{task, reason}]`, one reason for each cause, its `task` empty for a cause of the whole plan.
A reason that ends `This release of Legion
does not carry it out yet.` names a plan a later release takes: write the plan again within the rules above.
These are the reasons that end so, with `<…>` filled in:

- `the plan has <n> tasks (<ids>), and this release runs one task for each issue.`

- `task <id> needs <ids>.`

- `task <id> names <role> at step <i> after <role> at step <j>, and this release runs the steps in the order planner, implementer, tester, reviewer, acceptance.`

- `task <id> has no step implementer.`

- `task <id> names the role <role> at steps <i> and <j>.`

- `step <i> of task <id> names acceptance with no key, which asks for a fresh agent of the role acceptance.`

- `step <i> of task <id> names the key <key>.` (each key but `acceptance:owner`)

- `the plan defines the role <role>.` (a `plan_role` block)

- `task <id> names merges.`, `task <id> names owner.`, `task <id> names part_of.`

These reasons of the plan's text end with no "yet", because no release takes such a plan:

- `step <i> of task <id> names the role <role>, which neither the deployment nor the plan defines.`

- `the plan has no task block, so it gives Legion no work.`

- A block Legion cannot read, the one reason of its answer: `a <type> block has no id, so no step
  of the plan can be named.`, `the id <id> names a <type> block and a <type> block, so it names no
  one block.`, or `task <id>: the step "<word>" is neither a role nor a role:key, where each
  matches [a-z][a-z0-9-]*.`

The reasons a version gets for where its task stands are in `skill://legion-architect/references/plan-changes.md`.

## When to give it

Call the `legion` tool's `register_plan` (`issue`, `artifactId`, `version`) with the version you wrote.
Legion reads that version and no later one, so a version you do not give changes nothing.

- **A root:** before `register_gate`, because the gate's opening starts the root's first step.

- **A child:** before `release_children` releases it.
  The daemon has not recorded the child yet, so Legion keeps the plan until the release;
  the answer, as for any plan it takes, is the plan's `version` and each task with its `steps`.
  A child the daemon has not recorded is refused once its Dispatch status is none of `triage`, `backlog`, `icebox` and `todo` (`ISSUE_ALREADY_RELEASED`).
  The child's first step can start in the same admission pass as its release, so a plan given after it comes too late.

- **An issue that already runs** runs the task `default`, the project's default steps.
  Call `stop_task` on the task `default`, then `register_plan` with a version whose one task replaces it: Legion starts that task at its first step.

A `plan.md` that a planner of the default steps wrote holds that planner's task blocks.
The version you give replaces them with your own one block: delete theirs, and put what the next steps still need from the planner's plan into your task's text.

## Reading how plans went

`dispatch steps --issue <KEY>` lists each run of each step of an issue and how it ended.
Each finished issue of a project, with its plan, its retro's ratings, its time and its count of failed steps, is `GET /api/v1/projects/<KEY>/plans`:
`curl -sS -H @<(printf 'Authorization: Bearer %s' "$(< "$DISPATCH_TOKEN_FILE")") "$DISPATCH_URL/api/v1/projects/<KEY>/plans"`.
Read it before you choose the steps of an issue like one that finished.
