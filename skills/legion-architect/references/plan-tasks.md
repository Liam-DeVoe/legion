# A plan of several tasks

Part of `skill://legion-architect`, section 1.
Read it before you give a plan more than one task, or a task `needs`.
A plan's task blocks, its steps and when to give it: `skill://legion-architect/references/plan.md`.

## Several tasks

A task's `needs` names each task it needs, separated by spaces, each as a reference to that task's block: `dispatch://<KEY>/artifact/plan-md#b-<id>`.
A need that names another issue is refused (`PLAN_REFUSED`): a task needs only tasks of its own plan, and an issue waits on another issue by its `blocked_by` list (`skill://legion-architect/references/decomposition.md`, "Recording a wait on other work").
Legion starts each task with no `needs` when the issue's work starts, and each other task once every task it needs is done, from their joined work.
Exactly one task is needed by no task: Legion runs the retro and the merge in that task alone, and the work of every task merges there as one change.
Each other task is done when its last step passes.
This release runs one agent of each role for an issue, so give the steps of one role in several tasks the same key.
That agent works one step at a time, and a step of another task that it works waits for it.

An example with five tasks:

| Task | Needs | Steps |
| --- | --- | --- |
| a | | `implementer:i reviewer:r` |
| b | | `implementer:i reviewer:r` |
| c | b | `implementer:i reviewer:r` |
| d | a, c | `implementer:i reviewer:r` |
| e | d | `implementer:i tester` |

a and b start at once, and one implementer and one reviewer work the steps of all five tasks, one step at a time.
e is the one task that no task needs: its tester tests the joined work of every task, and the whole issue merges as one change after e's retro.

Task d as its block:

```markdown
:::plan_task{#d needs="dispatch://<KEY>/artifact/plan-md#b-a dispatch://<KEY>/artifact/plan-md#b-c" steps="implementer:i reviewer:r"}
What task d must deliver, on the joined work of tasks a and c.
:::
```

## The base of each task's pull request

Legion sets the base of each task's pull request, and a plan names no other.
The pull request of the task that no task needs, and of each task with no `needs`, is against the repository's default branch.
So a plan of one task, the project's default steps included, has its pull request against the default branch.
A task with one need has its pull request against that need's branch, and a task with several against its join branch, where Legion joins their work.
The implementer's completion is refused (`HANDOFF_PULL_REQUEST_BASE`) while its pull request is against any other base, so never ask for another base in a task's text.
Work that must build on unmerged work of the same issue is a task that `needs` the task that does it.
Work that must build on another issue's unmerged branch goes in an issue whose `blocked_by` list names that issue, and Legion starts it once that issue is `done`.
