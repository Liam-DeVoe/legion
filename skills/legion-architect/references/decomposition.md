# Decomposing a tree

Read this before you read a child's handoff, create or adopt children, review a child's spec,
split work across repositories, record a wait on outside work, set a parent link, or attach
architecture components.

## Reading the tree

Read your own issue (`read_record`), its criteria, its children and their handoffs:
`dispatch handoff-read --issue <child> --phase <plan|implement|test|review>`, with `--version <n>`
for an earlier one.

## Adopting children

Release a child a person created at once and as it is:
`legion({ op: "release_children", issues: ["LEGION-41", "LEGION-42"] })`. The answer names
`released`, `started` and `held`, each held child with what it waits on. A refused release writes
nothing: release the rest again without the child it names.

- `CHILD_NOT_ADOPTABLE`: it runs or ran as a tree of its own. Leave it, or ask the controller.
- `CHILD_IN_OTHER_TREE`: re-parent it back in Dispatch, or ask that tree's architect or the
  controller.
- `ISSUE_NOT_OWNED`: the issue is another architect's; send the move to the architect above you.
- `CHILD_UNDER_ACCEPTED_PARENT`: an ancestor is already accepted. Take it back with
  `request_backward_move`, or release once it has closed.

## Creating children

1. Write each child's spec as `skill://dispatch-writing-specs` says, scaled to the child, each
   requirement a `requirement` block, with its `verification.md`: its workers read only these.
   Cite no item number of another document and copy no text that does not apply to the child.
2. Red-team the list with a fresh `task(agent="oracle")`, and `wait` for its verdict. Give it your
   issue's spec, the tree's open children with their repositories and pull requests, and each
   proposed child's title and whole spec, with this mandate: "You did not write this list. Argue
   that each child should not exist. Name every child that is a to-do, a review fix of the issue's
   own code, a proof of the acceptance of the issue it is filed under, or a part of the architect's
   own work, and say where it folds: an open child's pull request, or the issue's own where it has
   no child. Weigh each separate pull request against combining its work: each round
   tests a large pull request whole again, and each small one costs a workspace, an admission
   wait, review, CI, a queue wait and a deploy." A repeated change is one child, and deleting what
   this tree replaced is this tree's work.
3. Post the verdict and your folds as one `dispatch comment` on your own issue whose first line is
   exactly `Red-team verdict for <your issue key>:`. The controller's weekly audit reads that line.
4. Check each survivor's spec with a fresh `task(agent="plan-gap-analyst")` against
   `skill://dispatch-writing-specs`, `wait` for it, and fix what it flags.
5. File only the survivors, and reply under the verdict with their keys:

   ```bash
   dispatch issue --project <project> --parent "$LEGION_ISSUE" --title '<child outcome>' --spec-file - <<'EOF'
   <the child's spec>
   EOF
   ```

6. Review the version you filed ("Reviewing a child's spec") before `release_children`.

- `--project` is the key's prefix (`LEGSMOKE-3` → `LEGSMOKE`). `--parent` is `$LEGION_ISSUE`,
  never a child of yours: that child's own architect files its children.
- Pass no `--assignee` unless a person names one: the default keeps the tree's questions in one
  Inbox.
- A child's own architect starts beside its child's planner: decide first whether the child needs
  children. If it does, file and release them; the child then carries no code of its own.
- Where the project has architecture components, give each child a `check` for the update of
  `.dispatch/architecture/<id>.md` of each component its pull request changes.
- A role token is `legion-<project>-<key>-<role>` with the daemon's project and the key
  lower-cased: `legion-acme-legion-41-architect`.

## Reviewing a child's spec

No person reads a child's spec: review every version of it and of its Verification document,
whoever wrote it, before the child's work goes on with that version.

1. Have a fresh `task(agent="plan-reviewer")`, told the documents are a child's spec and its
   Verification document, read them against your own spec (a root's at its approved version, a
   child's at the version its parent approved) with this mandate: "Does this version serve each
   requirement of the parent spec that it names? Does it narrow or contradict any? Does it add
   scope the parent spec does not ask for? Check each fact it states against the code, the docs or
   a read-only command." `wait` for its result.
2. Approve or request changes, with a reason either way.
3. Post one `dispatch comment` on the child whose first line is exactly one of these, and whose
   second line is `Verification document v<m>`; then tell the child's architect with
   `envoy_publish`:

   ```text
   Spec review of <child key> v<n> by <parent key>: approved
   Spec review of <child key> v<n> by <parent key>: changes requested
   ```

As the child's architect: tell the architect above you of each new version, naming each document
and its version; tell your workers of it only after its approving comment; answer
`changes requested` with a new version. A decision only a person can make goes to the architect
above you, never into your spec.

## Across repositories

Split a change across repositories into children of one repository each, choosing it at creation
(`dispatch issue … --repo <owner/name>`). Release a child that builds on another repository's
change only after that pull request merges, never by `--blocked-by`, which starts it at the
blocker's close, and name the merged commit in its task
(`gh api repos/<owner>/<repo>/pulls/<n> --jq .merge_commit_sha`); for a repository that pins the
other one by commit (a lock file or dependency pin), the pin is that merged commit. Your `gh` reads
every repository your App is installed on (`-R owner/repo`).

A change in a repository that another project's deployment runs is filed in that project, with `--parent` your root:
`dispatch issue --project <KEY> --parent <root> --repo <owner/name> …`.
Tell that project's controller with `envoy_publish` on `notifications.role.legion-<project>-controller` (its key in lower case).
That project's daemon runs the child, and your root waits for it as for an open child of its own.
A credential scoped to one project cannot make this link from either end: the create answers `PROJECT_SCOPE`.
Where yours is scoped, tell that controller what to file and under which root instead; it files the issue in its own project and has the parent set by a credential that names no single project, such as the operator's.
Release no such child with `release_children`: it answers `ISSUE_OUTSIDE_TREE`.
That project's controller takes it as a root of its own, and its architect writes it as a view on your root where your root's approval allows ("A root under an approved ancestor" in `design-gate.md`).

## Recording a wait on other work

Set an issue's `blocked_by` list when it must wait on an issue outside this tree
(`dispatch issue-update --issue <key> --blocked-by <a> --blocked-by <b>`, which replaces the whole
list; a credential scoped to one project cannot link another project's issue: `PROJECT_SCOPE`). A
blocker's close is its Dispatch issue reaching `done`, not a merge or a deploy: pick a blocker
whose close is the fact the issue needs. Legion starts the issue once its blockers close.

## An issue with children has no code of its own

Every change of an issue with children, and a fix found in its work, goes into an open child's pull
request or a new child. Where an issue whose branch already carries code needs children, file a
child for that code, whose work starts from those commits.

## A root with no code of its own

When every change lands through children, the root's implementer opens its pull request on an empty
commit. Plan no code for the root, and create no child only to give it a commit.

## Class roots and links

A class root (an issue whose spec states a class of work, each member its child) that must take new
members is a Legion root with no code of its own, never an issue that holds only a design. A
mention of a class in prose is no link: only a Dispatch `child_of` link (`--parent`) is one, and a
root counts only once a person approved its spec.

- A new member of your root's class, new work in your tree's unfinished work, or such a defect the
  controller sends becomes a child of the tree or part of an open issue's work.
- Once your spec is approved, link each issue already filed that belongs to your root's class or
  unfinished work: `dispatch issue-update --issue <KEY> --parent <ROOT>`. Release a linked issue
  that has not run. One that runs as a root of its own stays its own tree: `envoy_publish` to its
  architect's role topic your root's key and that "A root under an approved ancestor" in
  `skill://legion-architect/references/design-gate.md` now applies to it.
- Work outside the tree that another approved root holds is a finding for the controller.

## Architecture components

A project's components are the `.dispatch/architecture/` files Dispatch imports from its source
repositories (`skill://dispatch`).

- They exist: attach the root before you decompose, so every child inherits them:
  `dispatch issue-update --issue <root> --components-json '{"mode":"explicit","ids":[…]}'`.
- They do not: the root's first pull request writes `.dispatch/architecture/<id>.md` for each
  planned component (front matter `title`, `parent`, `depends_on`, `external`; no `paths`) in the
  repository that holds it, with an id no other source uses. Leave the root on `inherit` until it
  merges (an earlier attach answers `400 COMPONENTS_INPUT`), then run
  `dispatch architecture-sync --project <KEY>` and attach.
- Give a child its own `--components-json` only for a narrower set, and
  `{"mode":"none","reason":"…"}` when it changes no code a component describes.
