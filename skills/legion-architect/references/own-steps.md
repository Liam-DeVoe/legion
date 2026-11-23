# Your own steps and the retro

Part of `skill://legion-architect`, sections 5 and 6.
Read it when a step of your issue's plan has the key `owner`, when its task has no step of the type implementation, and when a retro reports to you.

## The retro

- The retro is done by the agent of the task's last step of the type implementation, as that step's role.
  Where the task has no such step, or that step's key is `owner`, it is yours: you do it as `skill://legion-retro` says.

- A retro that reports a defect holds its completion until you say whether it moves the issue back, which only retro can do while it holds the phase.

- Retro's output is in `skill://legion-retro`, steps 3 to 5.
  When it reports that it cannot compute, read or write the pull request body, it is still in its phase:
  answer it on its role topic from the deployment instructions, or open a `dispatch ask` naming the missing instruction.

## Which steps are yours

- A step whose key is `owner`, of any role and at any place in the task: `implementer:owner`, `tester:owner`, `reviewer:owner`, `acceptance:owner`, or a role of the deployment or of the plan with the key.

- The retro of a task with no step of the type implementation, or whose last such step is yours.

## How Legion gives you a step

- Legion starts no agent for your step.
  It sends you one `owner-step` notice: its role is the step's role (none for the retro), its phase the phase the issue stands in, and its reason the task a worker of that step would get.

- The notice comes only while that step runs, once your slot's gh files hold the GitHub App of the step's role, your own App included (the review App, for the retro, a planner, tester or reviewer step and acceptance); Legion writes the slot first only for another App.
  While you do the step, your slot acts on GitHub as that App, so an implementer step opens its pull request as the implement App.
  At the step's end Legion writes your own App back to your slot, unless your next step, of another App, already runs; a notice that comes after the end waits for that write.

- While the step runs, Legion tells it to you again in the same words when you stay idle past the silence bound, and at each launch of your session.

- A retry of your claim at your step (`legion claims retry`) relaunches your session, and Legion tells you the step again; it starts no worker.

## Reporting it

1. Do the work the task names, as a worker of that role would.
   Before you report a planning, implementing, testing or reviewing step, write its handoff record with `dispatch handoff-write`.

2. Report it with `legion({ op: "complete_step", issue: "<KEY>", step: <n>, summary: "<two sentences>" })`:
   `step` is its place in the task, 1 for the first step, and 0 for the retro.
   Add what that role's completion carries: the `verdict` `pass` or `fail` where the role reports one, `return_to` with a `fail`, and on the retro `plan_rating` and `plan_reason` (`skill://legion-retro`, step 6).

- At your acceptance step, `pass` is your sign-off and `fail` sends the task back, to `return_to` or to its first step of the type implementation before the acceptance;
  a `fail` that names no `return_to` where no such step stands before it stops the task, and Legion tells you (`task-stopped`).
  `sign_off` and `request_backward_move` still work there.

- The daemon refuses, changing nothing, a report from any architect but the issue's own (`NOT_THE_OWNER`), one of a step that does not run now with you as its agent (`OWNER_STEP_NOT_RUNNING`, naming where the task stands and who works that place), and one that breaks that step's completion rules, with the codes `handoff_complete` gets.
  `handoff_complete` stays refused to you.

## Your review step

- `reviewer:owner` ends by your `complete_step`, with the `verdict` `pass` or `fail`, and the commit your review's handoff record names, whether or not the issue has a pull request.
  No review on GitHub decides it, yours included, and your `fail` counts no review round.

- A review you leave on the pull request serves only the repository's own merge rule, such as a required approving review; Legion reads nothing of it.

## A task with no pull request

- A task where no step opens a pull request, such as the plan `tester` alone, has no merge: its retro's end closes the issue (`done`), no merger starts and no production check opens.

- Read its evidence from its steps' reports, its results (`dispatch results --issue <KEY>`) and the working copy its task names, never from a pull request.
