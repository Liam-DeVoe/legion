# Stopped tasks, later plans and their notices

Part of `skill://legion-architect`, section 1.
Read it before you stop a task or give a later version of a plan Legion runs, and on a `task-stopped`, `retro-held` or `plan-not-taken` notice.
A plan's text, its steps and when to give it first: `skill://legion-architect/references/plan.md`.

## Stopping and changing a task

- `stop_task` (`issue`, `task`) stops a task that is not done.
  Legion ends its running step, stops that step's process and keeps its session, and holds the issue from the phase its task stood in.
  The issue keeps its admission slot where that phase keeps one: a stop before `accepting` keeps it, and a stop in `accepting`, `retro` or `merging` holds none.
  On an issue whose first step has not run, the stop first settles the plan that step would walk, the newest plan you gave or the project's default steps (the task `default`), then stops its task.
  It refuses a task the plan does not hold (`404 TASK_NOT_FOUND`, naming the plan's tasks), one that is done or left the workflow with its issue (`409 TASK_DONE`) and a stop in a tree that lingers after it left the workflow (`409 TREE_LINGERING`).
  It also refuses, changing nothing, a stop of an issue outside your tree or one that left it (`403 ISSUE_OUTSIDE_TREE`) and a stop of a run of the issue that ended before Legion applied it (`409 TASK_STALE_GENERATION`): read the issue again.
  A stop of a task already stopped changes nothing.

- A stopped task stays held until you give a later version of the plan that changes it, or, on a root, until a person sets the root `todo`.
  Legion then starts it at its first step.
  A task that never started waits for its gate and its line first, as any task does before its first step.
  `retry_or_escalate` refuses the hold, a retry and an escalation alike (`409 TASK_STOPPED`), as the daemon refuses an operator's restart of it.

- A later version may change or remove only a task that waits or is stopped.
  Legion refuses one that changes or removes the running task, a change of the text in its block alone included, with `PLAN_REFUSED` and the reason `task <id> is running: stop it with stop_task before you
  change it.` It takes a version that leaves the running task's block as it is, including one that changes only the text outside the block, and the task goes on at its step.
  A block that names no `steps` and a block that writes the default steps out are different blocks, so a version that turns one into the other changes the task.

- Legion refuses a version at or below the one it holds (`PLAN_VERSION_OLD`), a version above Dispatch's latest (`VERSION_NOT_IN_DISPATCH`), a version of another document than the plan in force (`PLAN_DOCUMENT_CHANGED`), a version that changes a task that is done or left the workflow (`TASK_DONE`), a plan of an issue that left your tree before Legion applied it (`ISSUE_OUTSIDE_TREE`), and a plan given for a generation the issue left before Legion applied it (`PLAN_STALE_GENERATION`).

- A refusal records nothing, so the same version can be given again:
  one refused `PLAN_STALE_GENERATION` at once, and one refused while its task ran after `stop_task`.

- An issue that started its work before Legion took plans has no task, and Legion takes no plan for it:
  `<KEY> started its work before Legion took plans, so it runs the steps it started with to
  their end. This plan changed nothing.`

## The notices

- **`task-stopped`:** Legion stopped the task and holds the issue.
  Either a step failed and no earlier step of the type implementation can take the work back, and the reason names the task and the step: read the failed step's report.
  Or another architect of your tree stopped it with `stop_task`, and the reason names that architect and the notice carries `stoppedBy`: no step failed.
  A stop you make yourself sends you no notice: the 200 answer of `stop_task` is the record.
  Then give a later version that changes the task, or end the work: `park_child` for a child, under the park rule; `dispatch ask` on the root for a root.

- **`retro-held`:** a parent's task passed its last step with no `acceptance:owner` step to wait in, while issues beneath it are still in the workflow.
  Legion holds it before its retro, because a parent's head proves its children's merged work, and starts the retro when the last one leaves.
  Run each issue it names to done, or park one with `park_child` under the park rule.
  `retry_or_escalate` refuses that hold, a retry and an escalation alike (`409 HELD_FOR_CHILDREN`, naming each issue beneath it still in the workflow), and a `todo` on the root leaves it held.

- **`plan-not-taken`:** an earlier owner of the issue gave it a plan, and Legion did not take it at the issue's first step.
  Its reason names the version, who gave it, and the plan the first step runs: yours, or the default steps.
  To run the earlier plan, `stop_task` the task that runs now, then give the earlier plan again with `register_plan` as your own.
