# Stopped tasks, later plans and their notices

Part of `skill://legion-architect`, section 1.
Read it before you stop a task or give a later version of a plan Legion runs, and on a `task-stopped`, `retro-held` or `plan-not-taken` notice.
A plan's text, its steps and when to give it first: `skill://legion-architect/references/plan.md`.

## Stopping and changing a task

Your role prompt says what `stop_task` does, what holds a stopped task and what starts it again, and what each refusal of `stop_task` or of a plan's version means.
This section adds the steps.

- A later version may change or remove only a task that waits or is stopped: call `stop_task` on the running task first, or keep its block as it is.
  A version that changes only the text outside the block is taken, and the task goes on at its step.
  A block that names no `steps` and a block that writes the default steps out are different blocks, so a version that turns one into the other changes the task.

- To give a parked or done child a new plan:

  1. Call `rerun_child`: before it, the plan's task is done or left the workflow, and `register_plan` is refused `409 TASK_DONE`.
  2. Call `stop_task` on the task of the child's plan (`default` where you gave it none), because the rerun's first step can start in the same admission pass.
  3. Call `register_plan` with the new version: Legion starts the new task at its first step.

- A refusal records nothing, so give the same version again: at once after `PLAN_STALE_GENERATION`, and after `stop_task` when it was refused because its task was running.
  After `VERSION_NOT_IN_DISPATCH`, upload that version to Dispatch first.

## The notices

- **`task-stopped`:** Legion stopped the task and holds the issue.
  Either a step failed and no earlier step of the type implementation can take the work back, and the reason names the task and the step: read the failed step's report.
  Or another architect of your tree stopped it with `stop_task`, and the reason names that architect and the notice carries `stoppedBy`: no step failed.
  A stop you make yourself sends you no notice: the 200 answer of `stop_task` is the record.
  Then give a later version that changes the task, or end the work: `park_child` for a child, under the park rule; `dispatch ask` on the root for a root.

- **`retro-held`:** a parent's task passed its last step with no `acceptance:owner` step to wait in, while issues beneath it are still in the workflow.
  Legion holds it before its retro, so that it merges after them, and starts the retro when the last one leaves.
  Run each issue it names to done, or park one with `park_child` under the park rule.
  `retry_or_escalate` refuses that hold, a retry and an escalation alike (`409 HELD_FOR_CHILDREN`, naming each issue beneath it still in the workflow), and a `todo` on the root leaves it held.

- **`plan-not-taken`:** an earlier owner of the issue gave it a plan, and Legion did not take it at the issue's first step.
  Its reason names the version, who gave it, and the plan the first step runs: yours, or the default steps.
  To run the earlier plan, `stop_task` the task that runs now, then give the earlier plan again with `register_plan` as your own.
