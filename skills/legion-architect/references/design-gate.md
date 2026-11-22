# The root spec and the design gate

Read this before you write a root spec, before any edit to an approved one, and on
`design-approved`, `design-changes-requested`, `basis-ended`, `basis-changes-requested` or
`resting-root`. A child's own architect runs no gate.

## Writing the root spec

- Write it as `skill://dispatch-writing-specs` says, extending the issue's primary document in
  place with `dispatch doc-edit`. Never post a second spec artifact: `dispatch artifact --force`
  replaces a whole document. Each point the person decides is a decision block.
- Upload `verification.md` beside it (`--force` from its second version). Each clause of a check's
  `negative_control` names the test row or run that drives it. A requirement over every member of
  a class iterates the class's list, never a sample.
- Where a real recorded input exists (a stored record, a captured log, a real service's answer),
  hold at least one read-only check against it, or say in a decision block why none can be
  written. A requirement of the form "try X and see" is accepted by an observed real run.
- Cut each requirement carried over from an earlier issue that no source of this issue states:
  the words a person wrote in it, and what its `Filed for` line cites.

## A check CI proves

A check carrying `ci_check` is CI's: `ci_check` names the GitHub check run that proves it at the
head; it takes no `run`, its `when` is `before_merge`, and no role runs it or records its result
(`skill://legion-worker/references/ci-checks.md`). Where a CI job that runs on an ordinary pull
request already makes a check's proof, write that check as `ci_check` rather than as a command. A
`run` that names tests names specific tests or files; a whole suite is the CI job that runs it.

1. Name the check run of the job that runs the proof, never a roll-up. On cybermonklabs/legion
   never `ci-ok`, which passes when the jobs it did not select were skipped
   (`.github/scripts/ci-ok.sh`).
2. A job held back from ordinary pull requests needs its label on the pull request for as long as
   a check names it (`full-ci` on cybermonklabs/legion); without it the job reads `skipped`.
3. Write in `negative_control` the case inside that job that fails when the property breaks: a
   step with `continue-on-error` keeps its job green.

## Show the surface before you ask

Where this tree builds or changes a surface a person opens (a page, a screen, a dashboard), that
person sees it and answers before you request approval: a spec for a surface they have not seen is
refused. A new surface or a large visual change needs this round, and a flow needs a page they can
click; a small change inside a standing shape does not, unless you are unsure what they want. A
surface that is only text (an alert, a report, printed output) carries that literal text in the
spec instead.

1. Build the surface as a page from real readings of the real system. Draw the empty state and the
   worst realistic data, or say which states you left out. Mark every invented figure in the
   picture, and draw an existing surface as it is now beside the new one.
2. Take the picture with the pod's Chromium and put it on the issue, with a clickable page as a
   `file` artifact for a flow:

   ```bash
   chromium --headless --disable-gpu --no-sandbox --window-size=1440,900 \
     --screenshot=<file.png> file://<page.html>
   dispatch artifact --issue <KEY> --name <name>.png --path <file.png>
   ```

3. Ask one question with two to four answers, one of them that this is the wrong surface. Write no
   code for the round, and end your turn.
4. Put the agreed pictures in the spec, each with a line on what it shows and which readings are
   real, and one line on the states they do not show. The spec's words still govern the build.

You end the round, not the person. The round writes no spec version of its own, and a tree whose
gate is open starts none. Under `Design gate steps: none`, put the picture in the spec and ask
nobody. The surfaces children build are the root architect's.

## Which steps are gated

The `Design gate steps` sentence of your `Legion addressing` line names the gated steps, or `none`.
Each step records one document of the root issue:

| Step | Document |
| --- | --- |
| `criteria` | the Verification document (`verification.md`) |
| `spec` | the primary document |

Under `none`, `register_gate` the spec at its current version with no `step`, and request nothing.

## The armed gate

For each gated step, once every child is filed:

1. Edit the document in place: `dispatch doc-edit --issue <root> --ops-json-file -`.
2. Settle every decision block (`skill://dispatch-writing-specs`, "Approval").
3. Give the current spec, the code and output it relies on, and the person's words to two fresh
   reviewers: `task(agent="plan-gap-analyst")` for its gaps, and `task(agent="plan-reviewer")`,
   told it is a spec, for each factual claim. `wait` for each, resolve every finding, and check
   each unverified claim with a command or take it out.
4. Request approval only once the person has agreed to every point; a new point gets its own
   decision block first. `--include-verification` covers `spec` and `criteria` in one request:

   ```bash
   dispatch request-approval --issue <root> --include-verification --summary '<what they approve> Reviews: <gap result>, <claim result>; <what they do not show>.'
   ```

5. Register each step on its own document, with the id and version the request printed, never a
   slug. Leave `step` out only where your line names one step:

   ```ts
   legion({ op: "register_gate", issue: "<root>", step: "spec", artifactId: "<UUID>", version: <N> })
   ```

6. End your turn. The person's review brings your next task (the `design-approved` row of
   `skill://legion-architect/references/wake-routing.md`).

- A newer version moves the open request to it and to your turn, and the person stops seeing it:
  once they have agreed to that version, run `dispatch request-approval` again to give it back.
- `already approved at version <N>`: still `register_gate` that id and version.
- Refusals record nothing: `VERSION_NOT_IN_DISPATCH` (register the latest version),
  `GATE_STEP_REQUIRED` (name the step), `GATE_STEP_NOT_CONFIGURED` (register only gated steps),
  `GATE_STEP_DOCUMENT_MISMATCH` (register that step's own document).
- Never open a `dispatch ask` with an `Approve` option: the daemon ignores its answer.

## After approval

An approval is pinned to its version: any new version closes that step, with no wake. Two keep it:
a version whose only change is a person's answer to a decision block, and one equal to the approved
text (an accidental edit undone exactly).

Edit an approved spec only where its Summary, a requirement, a check, the scope or a settled
decision changes: put the problem to the person as a decision block at the end of the section it
changes. When their answer changes text outside the block, rewrite it and repeat steps 3 to 5,
with a `--summary` sentence naming what changed. Work in flight goes on; release no further child
until the next `design-approved`.

A change that keeps all five goes in the plan (`plan.md`), never the spec. A plan overturns a
settled decision only with evidence the person did not weigh, put to them as a decision block. A
person's later request is a requirement only once written into the spec and approved.

## A root under an approved ancestor

A root under an approved ancestor can be written as a view on it, and open its gated steps on the
ancestor's approval with no approval of its own. The ancestor is the nearest issue up the root's
`child_of` chain (read with `dispatch read`) whose document for the step has a review or an open
approval request; never take one farther up. A route version is a version of the ancestor's
document that adds, revises, restores or takes out a requirement a root sent up; its approval
request is a route request.

The ancestor may be in another project: register on it the same way; its reviews reach your daemon at the next controller tick.
For anything else you need from that project, `read_record` included, ask its controller on `notifications.role.legion-<project>-controller`.

### When the root is a view

Write the root as a view only when the ancestor carries the `legion` label, is not accepted, has a
person's approval of its latest version (or a route version waiting on the person) and marks
requirement blocks, and the root's own newest review is not a changes request. Otherwise request a
person's approval of the root's own document ("The armed gate") and register it with no basis. Do
the same when the ancestor's text says the root's kind asks its own approval; when no person gave
the ancestor's approval; when the ancestor's request is open and names no root; when its newest
review is a changes request, or its approval is of an older version, with no route request
waiting; or when the person kept out a requirement the root needs. The root's spec states in its
own words which of those cases holds: a pointer to decisions.md, to a list or to another passage
does not count. When a person asks for the root's own approval, request it first, then register
that version with no basis: the person's request wins over a basis.

### Writing the view

- No document of a view holds a requirement block. One line under `## Requirements` starts
  `Delivers:` and names each ancestor block it delivers as
  `dispatch://<ANCESTOR>/spec#b-<id>@v<N>`; where it delivers only part of one, it says which part,
  for example the part in one repository.
- Its Verification document holds at least one check, no other document of the root holds a check,
  and each check's `serves` names only the ancestor's requirements it proves. A check the
  deployment asks of every change, such as an after-merge production check, serves the requirement
  whose change it deploys. When work leaves the spec, its check leaves the Verification document.
- A root that runs with requirements of its own becomes a view at its next spec change: each
  requirement the ancestor states becomes checks that serve the ancestor's block, and each one it
  does not state goes up.
- The view's Design decides how the root builds each requirement, also where it changes a Design
  line a person approved before: such a line is no requirement and does not go up. A requirement
  the root needs that the ancestor does not state goes into the ancestor's spec ("Sending a
  requirement up"), never into the root's.
- Where the view's work needs another child's pull request merged first, name that child in the
  spec. Stack on its branch where the deployment says to, and accept only a head with its merge;
  else link the root `blocked_by` it. Ask no person.

### Registering on the basis

A basis registration takes the place of steps 4 and 5 of "The armed gate". File every child before
it.

1. Run steps 1 to 3 with both fresh reviews. Also give the `plan-reviewer` the ancestor's approved
   version, and send up each new service, credential, outside dependency or cost over a bound of
   that version that it finds the root's delivery brings in.
2. Register each gated step's own document with `basis` (the ancestor's document for that step),
   `basisVersion`, and a `reason` that names the ancestor's requirements the checks serve and the
   reviews' result:

   ```ts
   legion({ op: "register_gate", issue: "<root>", step: "spec", artifactId: "<UUID>", version: <N>,
     basis: "dispatch://<ANCESTOR>/spec", basisVersion: <M>, reason: "<requirements served; reviews>" })
   ```

3. In the same turn, after the registration, retract the root's own open approval request for that
   step: `dispatch resolve-ask --ask <id> --kind retracted --reason '<registered on the basis>'`.
4. After a registration on a version that waits on the person (a pending basis), end the turn.

Release the children once each gated step reads `approved` or `open_on_basis`, and act on a
`design-approved` that names an ancestor's basis as on any other. A relaunched architect does not
register again, in the same generation, a step that opened on a basis. A condition the deployment's
instructions put on an approval request, such as an owner's approval link, also holds for a basis
registration. After a new version of the view's own spec, run both fresh reviews and register again
with the basis, or request a person's approval.

### Sending a requirement up

Find the ancestor's owner with `read_record` on the ancestor and each issue above it: the nearest
claim on an issue in the workflow whose `architect.state` is not `failed` or `retired`. Read
`treeRuns` on the record of the ancestor's tree root to learn whether the ancestor's tree runs.
While it runs, `envoy_publish` the requirement and why the root needs it to the ancestor
architect's role topic, then:

1. The ancestor's architect adds the requirement as a block whose text starts
   `Delivered by dispatch://<ROOT>.`, keeps the how in the root's Design, and tells the root's
   architect the block's reference and version.
2. The root's architect writes its `Delivers:` line and check for the block as written, pinned to
   that version, and tells the ancestor's architect, naming missing parts or concerns without
   holding back the line or the registration.
3. Once told, the ancestor's architect runs both fresh reviews, requests the person's approval,
   registers its gate at that version where the ancestor is its tree's root, and tells the root's
   architect that the request is open.
4. The root's architect runs both fresh reviews, registers on that version as a pending basis, and
   ends its turn.

Where the ancestor's tree does not run, the root's architect writes the block, the `Delivers:` line
and the check itself, reviews the ancestor's version and requests its approval, registers no
ancestor gate, then registers as a pending basis. Where the ancestor is on the backlog, tell the
project's controller that the root rests on it, whether or not you send a requirement up.

While a route request waits on the person, send a second requirement or a further part for the
next version; still register on the waiting version, and build that part only after registering on
a version that holds it. Any other root under the ancestor may register as a pending basis on a
route version that waits on the person.

### For the ancestor's architect

- A route request's summary names each root, the requirement and the cost, whatever length
  `--help` asks for, and its last sentence gives both reviews' result. The cost says: a tree root's
  new version closes its gate until approval; a backlog root's approval re-admits its tree; a
  re-admitted root that waits in the line starts a tree that waits for this approval.
- On the route, the approval request, not a decision block, is the person's decision on an edit to
  the approved ancestor.
- At your start, read the ancestor's document and any open request before you act on an older
  `basis-changes-requested` or `resting-root` notice. When a start task asks you to request
  approval of a version a route request is already open on, leave that request alone and register
  the version it asks about.
- When you hand a new ancestor version back to the person, tell each root whose pending basis that
  version ended.
- A `Delivered by` requirement gets no task in the ancestor's plan and no check in its Verification
  document.
- When the root a `Delivered by` requirement names leaves undelivered, or a delivered requirement
  names no root, file a view child of the ancestor for it, rewrite the requirement with its name,
  and request approval; ask nothing first.
- On `resting-root`, where a wait ended on a new version or a changes request, tell that root of
  the next version once its request is open.
- On `SIGNOFF_RESTING_ROOT_IN_WORKFLOW`, do not move the resting root (its own architect or a
  person does), and sign off again once each one is done or out of the workflow.

### A changes request on the route

The ancestor's architect revises the version (an exact undo where the person keeps the requirement
out), runs both reviews, requests approval again, and registers its gate where it is its tree's
root. It tells each root whose pending basis ended, each whose step opened on the version sent
back, and each that sent the requirement: the version sent back, the new version, and that the
request is open. Where the ancestor's tree does not run, the root's architect does that itself, and
finds the other roots by the `Delivered by` name, or by the checks that serve the block and
`read_record`'s `tree`.

A root acts on a changes request only where the person's reason touches a requirement its
`Delivers:` line names; every other root keeps its spec and its step. While the ancestor's tree
runs, a root waits for the ancestor's architect to name the new version and its open request before
it acts. Then:

- A root whose pending basis ended registers on the next version if it still states the
  requirement.
- One whose step opened on the version sent back changes work and check by a new spec version and
  registers pending on the revised one.
- Where the person keeps the requirement out, a root that can do without it takes the work and
  check out and registers pending on the restored version; one that still needs it asks for its own
  approval.

Before each registration on another ancestor version, pin the `Delivers:` line to that version and
run both fresh reviews of the root's new version. On `basis-ended`, read the ancestor's document
and open request, then register again on a newer route version, follow this route, go on after the
root's own approval, or ask for the root's own approval.

### Accepting a view

Accept a view on the parts of the ancestor's requirements that its `Delivers:` line names at the
pinned version, and never read its empty requirement list as a gap, in the `goal-red-team` read
too. Count a requirement that views deliver in parts only when the parts make the whole, and name
each part in the acceptance comment with the issue and the check that proved it. In a parent's
acceptance, a view that roots a tree of its own is not a child of the tree: its own checks prove
the parts its `Delivers:` line names.

## A decision block for a read no pod can make

When a check needs evidence from a production or outside system, first check whether a credential
the pod carries reaches it: read the pod's environment (for example `AWS_CONFIG_FILE` or
`AGENT_SECRETS_URL`) and `skill://agent-secrets`. If none does, write the missing capability as a
decision block (which system, which read, why); never fold in a read another session made.
