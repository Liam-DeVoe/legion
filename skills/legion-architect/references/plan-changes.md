# Stopped tasks, later plans and their notices

Part of `skill://legion-architect`, section 1.
Read it before you stop a task or give a later version of a plan Legion runs, and on a `task-stopped`, `retro-held` or `plan-not-taken` notice.
Read it also before you decide something for an agent of a task.
A plan's text, its steps and when to give it first: `skill://legion-architect/references/plan.md`.

## Stopping and changing a task

Your role prompt says what `stop_task` does, what holds a stopped task and what starts it again, and what each refusal of `stop_task` or of a plan's version means.

- After `VERSION_NOT_IN_DISPATCH`, upload that version to Dispatch first.

- A later version that changes only the text of a running task is taken, and the task goes on at its step.
  Any other change of a running task needs `stop_task` first.

## A decision for an agent

Put each decision for an agent of a task into the task's text, in a later version of the plan.
Every later start of the task holds that text, a new session's start included.
The agent at work now reads no new start.
So when the answer of `register_plan` names the step the decision is for, in `running`, also send that step's agent the decision by message.
On the project's default steps, the task is `default`, which no block of yours names.
For it, give a version of `plan.md` whose one task block is `:::plan_task{#default}`, with no `steps`, and whose text holds the decision.
Legion takes that version while the task runs, and stops nothing.
A planner's task blocks do not go in that version: Legion refuses a version that removes the task `default`.
An issue that started its work before Legion took plans has no task text.
On such an issue, put the decision in a comment on the issue, and send it to the agent by message.
These rules hold also where a general skill offers another place for a decision.
A list of open items, such as an issue's `open-items.md`, is such a place.
Write no decision for an agent in it, and do not keep it as the record of the work.
The issue and its plan are the record of the work to be done.

## The notices

- **`task-stopped`:** Legion stopped the task and holds the issue.
  Either a step failed and no earlier step of the type implementation can take the work back, and the reason names the task and the step: read the failed step's report.
  Or another architect of your tree stopped it with `stop_task`, and the reason names that architect and the notice carries `stoppedBy`: no step failed.
  A stop you make yourself sends you no notice: the 200 answer of `stop_task` is the record.
  Then give a later version that changes the task, or end the work: for a child under your own issue, your own status write in Dispatch, by the rules for ending a child in your role prompt; `dispatch ask` on the root for a root.
  When the stopped task is your own issue's, as a child's architect, send the end of that work to the architect above you with `envoy_publish`: the daemon sets back your status write on your own issue.

- **`retro-held`:** a parent's task passed its last step with no `acceptance:owner` step to wait in, while issues beneath it are still in the workflow.
  Legion holds it before its retro, so that it merges after them, and starts the retro when the last one leaves.
  Run each issue it names to done, or end it by your own status write in Dispatch, by the same rules.
  `retry_or_escalate` refuses that hold, a retry and an escalation alike: while issues beneath it are in the workflow, `409 HELD_FOR_CHILDREN` names each of them; with none left, the answer says that its tree lingers or that its root waits in the admission line.
  A `todo` on the root leaves it held.

- **`plan-not-taken`:** an earlier owner of the issue gave it a plan, and Legion did not take it at the issue's first step.
  Its reason names the version, who gave it, and the plan the first step runs: yours, or the default steps.
  To run the earlier plan, `stop_task` the task that runs now, then give the earlier plan again with `register_plan` as your own.
