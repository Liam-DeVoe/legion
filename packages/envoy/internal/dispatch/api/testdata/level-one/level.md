# Level one of The Spec

This level holds 59 statements and 43 components, 12 of them interfaces. Since version 14, 25 new specifics under the components replace its seven design principles, with 5 rules of structure that graph queries decide.

**Least sure** marks the 6 new statements that the writers are least sure of, with the doubt. **New** marks each statement that this version adds. **Today** names what today's code does not meet. **Check** gives the query on The Spec's graph that decides a rule of structure. **Level two** gives the count of statements under a statement in level two, after the moves of this version. **The person** is the one who approves The Spec. The sources of each statement are the Decisions line of its notes, and no statement here holds a link.

## The statements that the writers are least sure of

- `legion-writes-dispatch-status-over-what-it-read`. A controller may change a status, so the gap in the Today line may want a fence on that route, not its removal.
- `each-record-kind-has-one-writer`. The check sees only the records that some statement names, so a copy that no statement names escapes it.
- `legion-takes-deployment-values-from-its-settings`. Whether the shared private-name guard is a policy of one deployment or a shared safety check that holds one deployment's word list.
- `shipped-prompts-and-skills-name-no-deployment`. Whether a skill that predicts the person's approvals is a shared tool of every deployment or one person's policy.
- `envoy-takes-hosts-and-accounts-from-settings`. Whether a load test's example file counts as code of Envoy.
- `dispatch-refuses-document-growth-past-its-bound`. The editor half rests on a refusal code that the code does not define yet, so its form may change.

## Statements

### Legion

It runs agents to turn approved work into software changes.

:::requirement{#legion-starts-work-in-order-under-its-cap}
Legion starts each new issue in its project's order, once its blockers clear and a place under the project's cap is free. A place under the cap holds one issue with its owner, whatever work units, steps or workers it runs, and returning it stops no agent.
:::

**Level two:** 10.

:::requirement{#the-daemon-moves-work-by-recorded-rules}
Legion's daemon moves work by recorded rules, which give the same result for the same records, events, service answers and due timers. The daemon does not decide what a review finding or a failed check means for the work, and follows the recorded outcome.
:::

**Level two:** 11.

#### Work coordination (component `legion-coordination`)

It admits issues, shares the capacity of a project between issues, and keeps ownership and dependencies consistent.

:::requirement{#legion-copies-dispatch-issues-by-sequence}
Legion's daemon keeps a copy of the status, blockers, parent and claim of each Dispatch issue that it reads. It replaces a copied value only with a reading at a newer Dispatch sequence.
:::

**New.** **Today.** The daemon can replace an issue's copied parent at the sequence that it already holds. The status, the blockers and the claim each need a newer reading. **Level two:** 1.

Also about it: `legion-starts-work-in-order-under-its-cap`, `the-daemon-moves-work-by-recorded-rules`, `projects-try-agent-settings-by-experiment`, `work-keeps-its-owner-and-resumes-after-an-unexpected-stop`, `legion-writes-dispatch-status-over-what-it-read`.

#### Workflows (component `legion-workflows`)

It decides which steps of a work unit can run, from the unit's workflow version, what the unit waits on and the recorded outcomes.

:::requirement{#steps-follow-the-chosen-workflow}
Legion runs each work unit through the steps of the workflow version that the unit's owner chose or wrote. A new workflow version needs no change of Legion's code, and neither adds nor removes an approval that the person must give.
:::

**Today.** Legion runs each issue through one fixed sequence of phases that its code defines. The text states an approved aim. **Level two:** 7.

:::requirement{#the-daemon-builds-issue-state-from-events}
Legion's daemon builds each issue's state from Dispatch issue events and from GitHub pull request, review, push and check events that Envoy publishes. It reads GitHub only in reply to an event, at a handoff, and once for each issue at its own start.
:::

**New.** **Today.** The daemon also retries a failed read of a GitHub installation on a timer, from thirty seconds up to ten minutes. Its start pass stops early when GitHub limits it. **Level two:** 3.

:::requirement{#legion-holds-a-tree-until-its-approvals-stand}
Legion starts no phase of an issue tree, and merges none of its changes, while an approval that the tree waits on does not stand. Until its repository holds The Spec, each top-level Legion issue waits for the person's approval of its spec.
:::

**New.** **Today.** A deployment setting can turn the wait off. A new spec version during the work holds only the merge, and later phases still start. **Level two:** 5.

Also about it: `the-daemon-moves-work-by-recorded-rules`, `delivered-changes-meet-their-requirements`, `merger-merges-only-the-head-it-read`, `the-daemon-takes-the-merge-verdict-from-github`, `legions-own-merge-conditions-only-hold-a-merge`.

#### Agent runs (component `legion-runners`)

It starts, resumes and observes agents with their assigned roles.

**Least sure.** Whether a skill that predicts the person's approvals is a shared tool of every deployment or one person's policy.

:::requirement{#shipped-prompts-and-skills-name-no-deployment}
The role prompts and skills that Legion and Envoy ship name no deployment, project, private repository or person, and quote no measurement or incident. Legion adds the deployment's own instructions file after its role prompts, in the prompt of every role.
:::

**New.** **Today.** Shipped skills name one deployment's private repository, one person's tool repository and an issue of one project. One skill holds one person's review preferences, and another quotes measured token counts. **Level two:** 2.

:::requirement{#legion-paces-the-session-wakes-it-causes}
In any minute, Legion starts, resumes or wakes no more agent sessions than its configured rate plus burst, by default 8 plus 1. It delays each wake past that and drops none, and a rate of 0 turns the pacing off.
:::

**New.** **Today.** A wake of a project's controller, and a start or a resume that an operator asks for, do not wait for the pacer. **Level two:** 0.

Also about it: `the-daemon-moves-work-by-recorded-rules`, `work-keeps-its-owner-and-resumes-after-an-unexpected-stop`, `the-person-reads-and-steers-running-agents`, `legion-takes-deployment-values-from-its-settings`, `the-daemon-answers-each-plugin-in-its-contract`.

#### Work environments (component `legion-environments`)

It provides isolated computing and repository workspaces for agent work.

:::requirement{#legion-gives-each-session-its-environment}
Legion gives each agent session that it starts an environment with the tools, access and resources that the operator provides. Work on one issue changes no working file of another issue.
:::

**Today.** On a shared machine, workers of two issues run as one account, so one can write into the other's working files. **Level two:** 14.

Also about it: `work-keeps-its-owner-and-resumes-after-an-unexpected-stop`, `legion-takes-deployment-values-from-its-settings`.

#### Change integration (component `legion-change-integration`)

It publishes the change of a work unit for review and brings it into the repository under the rules of that repository.

:::requirement{#merger-merges-only-the-head-it-read}
Legion's merger role, not its daemon, merges each pull request, as Legion's implementing GitHub App and only at the head it read. On a branch with a merge queue it puts that head in the queue, and it never turns on automatic merge.
:::

**New.** **Today.** Only the merger's instructions make the merge this way. No program of Legion makes the merge, or checks the head and the method that the merger used. **Level two:** 3.

:::requirement{#the-daemon-takes-the-merge-verdict-from-github}
Legion's daemon moves an approved issue from review to acceptance only when GitHub's merge state for the approved head is clean, unstable or has hooks. It reads no ruleset and no list of required checks.
:::

**New.** **Level two:** 1.

:::requirement{#legions-own-merge-conditions-only-hold-a-merge}
Legion checks the reviewer's approval against the current head before acceptance, and not again when the merger starts. The architect's sign-off and an open design gate also hold the merger back.
:::

**New.** **Today.** A push after acceptance can replace the head that the reviewer approved. No later step reads the approval again. **Level two:** 2.

Also about it: `delivered-changes-meet-their-requirements`, `main-branch-merges-only-through-its-queue`, `legion-holds-a-tree-until-its-approvals-stand`.

#### Deployment settings (component `legion-settings`)

It applies the runtimes, models, role instructions and limits that a deployment chooses, and keeps the experiment group of each issue or issue tree.

**Least sure.** Whether the shared private-name guard is a policy of one deployment or a shared safety check that holds one deployment's word list.

:::requirement{#legion-takes-deployment-values-from-its-settings}
Legion's daemon takes its project, service addresses, credentials, runtime, worker image and model overlays from its deployment's settings file. Its code names no host, repository, account or person of one deployment.
:::

**New.** **Today.** Legion's own worker image installs a leak check from one deployment's private repository, which the code names. The daemon also carries that deployment's private names, and checks every public write of a worker against them. **Level two:** 12.

Also about it: `projects-try-agent-settings-by-experiment`, `shipped-prompts-and-skills-name-no-deployment`, `legion-paces-the-session-wakes-it-causes`.

#### Project controller (component `legion-controller`)

It is the one agent of a project that ranks waiting work and settles what the owners of work cannot agree on.

:::requirement{#the-controller-sets-the-status-of-issues}
Through Legion's daemon, the project controller can set an issue's status in Dispatch to todo, backlog or icebox, and to no other status. The daemon refuses that change from any session that is not the project's controller.
:::

**Today.** A grant that the operator's token mints passes as a controller's, with no controller session. A project's controller can also set the status of an issue of another project. **Level two:** 0.

Also about it: `legion-starts-work-in-order-under-its-cap`, `legion-writes-dispatch-status-over-what-it-read`.

#### Work participation (component `legion-work-participation`)

It lets an agent in its session take its assignment, report its state and its completion, and get a reminder of open work.

Also about it: `the-daemon-answers-each-plugin-in-its-contract`.

#### Worker contract (interface `worker-contract`)

An agent in its session and Legion agree on compatible messages and on the work that a report is about.

:::requirement{#the-daemon-answers-each-plugin-in-its-contract}
Legion's daemon answers each worker plugin in the shape of the contract number that the plugin's build states. A plugin refuses an answer that it cannot read, and names both contract numbers and each wrong field.
:::

**New.** **Today.** The daemon narrows only its state answer, by the number that the registration of the caller's pod recorded. The plugin's refusal names each wrong field, but neither contract number. **Level two:** 6.

Also about it: `the-daemon-moves-work-by-recorded-rules`, `work-keeps-its-owner-and-resumes-after-an-unexpected-stop`, `each-interface-names-its-declared-contract`.

### Dispatch

It holds work, documents and decisions for people and their agents.

:::requirement{#agent-writing-review-follows-its-settings}
Where settings turn writing review on, it checks the agent texts that they cover, and holds a text only where they require a hold. A rewritten text keeps its original behind a badge, and each person sees daily counts of texts held, rewritten, passed, unreviewed or posted despite objections.
:::

**Today.** Only the agent plugin reviews agent writing: questions, messages, comments, documents and approval requests. It rewrites no text, and no page shows daily counts. **Level two:** 10.

:::requirement{#work-is-arranged-for-the-person}
Dispatch lets a person arrange and see work by project, priority, state and time. A priority is a ranked goal with an owner that work of any project can advance, and it is not an issue.
:::

**Today.** A priority can have no owner, because Dispatch keeps its owner as optional text and not as a link to a person. **Level two:** 9.

:::requirement{#dispatch-draws-one-thing-one-way}
Dispatch draws each kind of record the same way in every list, and each reference to a record the same way on every page. Its lists, filters and text fields share one component for each kind of record, value or purpose, except document bodies and live agent conversations.
:::

**Today.** Two lists draw a document in different ways, with different fields. A link in a project list shows an issue's key alone, and a reference in text shows its key and title. **Level two:** 9.

:::requirement{#each-issue-reads-on-its-own-page}
Dispatch shows an issue's conversation, spec, verification, plan and decisions on the issue's own page. From that page, a person reaches each document of the issue and its history.
:::

**Level two:** 6.

:::requirement{#records-are-linked-by-named-relations}
Dispatch stores issues, documents, conversations and decisions as linked records with named relations. Each decision links to its author, and to the question that it settles where there is one.
:::

**Today.** A decision is a section of a document, with no required link to its author. Its link to a question exists only where its text names that question. **Level two:** 11.

:::requirement{#waiting-work-reaches-the-persons-inbox}
Each ask, approval request, credential request and thread that waits on a person reaches that person's one Inbox. It stays there until someone settles, dismisses or hands it on, and that person's answer reaches the agent that asked.
:::

**Today.** A thread reaches a person's Inbox only after that person wrote in it. A credential request leaves the Inbox when it expires, with no act of the person. **Level two:** 9.

:::requirement{#edits-lose-nothing}
An edit changes only what it names, its event, the questions that it opens or closes, and the approval requests that its new version moves. It never drops another writer's concurrent edit, and earlier versions stay readable unless a permitted deletion hides their text.
:::

**Today.** Two writers who replace one comment or question at the same time can overwrite each other. A replacement names no earlier version that it must match. **Level two:** 10.

:::requirement{#dispatch-actions-need-no-browser}
Each read or change of a record that Dispatch's pages offer is also available as a request or a terminal command, under the same permissions.
:::

**Level two:** 7.

:::requirement{#dispatch-takes-deployment-values-from-its-settings}
Dispatch's server takes its database, sign-in provider, GitHub App, embedding and answer models and message hub from its deployment's settings. An optional setting left unset turns its feature off or takes one stated default.
:::

**New.** **Level two:** 1.

:::requirement{#dispatch-release-serves-two-compatible-builds}
While two builds of Dispatch serve during a release, each accepts the other's requests, stored documents, bus messages and subscription settings. A page that the earlier build served keeps working.
:::

**New.** **Today.** At one replica, the deployment stops the old process before the new one starts, so two builds never serve together. Continuous integration serves two builds of one source, and an older build beside a newer one is tested only by hand. **Level two:** 1.

#### Records (component `dispatch-records`)

It holds each record of Dispatch and the relations between records, and it resolves each reference to a record.

Also about it: `work-is-arranged-for-the-person`, `dispatch-draws-one-thing-one-way`, `records-are-linked-by-named-relations`, `edits-lose-nothing`, `legion-writes-dispatch-status-over-what-it-read`, `dispatch-publishes-each-record-change-to-its-subscribers`, `dispatch-release-serves-two-compatible-builds`, `dispatch-holds-the-approved-spec-as-nodes`, `each-spec-node-has-the-persons-approval`.

#### Document editing (component `dispatch-documents`)

It lets people and agents write and read versioned text and attachments.

**Least sure.** The editor half rests on a refusal code that the code does not define yet, so its form may change.

:::requirement{#dispatch-refuses-document-growth-past-its-bound}
Dispatch refuses an edit that would grow a document past 1 MiB of text or 65,536 elements, and names the bound and the size. The editor shows the reason and drops only the refused edit.
:::

**New.** **Today.** Dispatch refuses such an edit from a request or a command and names the bound. An edit in the browser editor is not measured, so a document can grow past the bound there. **Level two:** 5.

Also about it: `agent-writing-review-follows-its-settings`, `each-issue-reads-on-its-own-page`, `records-are-linked-by-named-relations`, `edits-lose-nothing`, `an-approval-stands-only-for-its-version`.

#### Conversations (component `dispatch-conversations`)

It holds the threads of comments, messages and replies between people and agents, with their authors and their history.

:::requirement{#dispatch-refuses-text-past-its-block-bounds}
Dispatch refuses a comment, comment edit, message, broadcast or reply that holds a block over 32,000 text units, or more than 32,768 blocks. The refusal names the field, the bound and the count over it.
:::

**New.** **Today.** A refusal names the block only where one block is too big. A character outside the common range counts as two text units. **Level two:** 4.

Also about it: `agent-writing-review-follows-its-settings`, `each-issue-reads-on-its-own-page`, `records-are-linked-by-named-relations`, `waiting-work-reaches-the-persons-inbox`, `edits-lose-nothing`.

#### Decisions and approvals (component `dispatch-decisions`)

It records questions, answers, decisions and what the person approved.

:::requirement{#an-approval-records-person-time-request-and-hash}
When a person approves, Dispatch records the person, the time, the approval request that it answers, the version approved and a hash of its text. It refuses a person's approval from a credential without the grant to approve.
:::

**New.** **Level two:** 2.

:::requirement{#dispatch-holds-the-approved-spec-as-nodes}
Dispatch holds the approved Spec as nodes: statements in one tree, their checks, and the components and interfaces they are about. Only the person's approval of a change request adds, changes or removes a statement, component or interface in it.
:::

**New.** **Today.** The Spec is not built. Dispatch holds documents, and any signed-in caller can import components from repository files with no approval. **Level two:** 6.

Also about it: `records-are-linked-by-named-relations`, `waiting-work-reaches-the-persons-inbox`, `an-approval-stands-only-for-its-version`, `each-spec-node-has-the-persons-approval`.

#### Inbox (component `dispatch-inbox`)

It shows a person what waits on that person, and keeps what that person read, answered or dismissed.

Also about it: `waiting-work-reaches-the-persons-inbox`.

#### Search and actions (component `dispatch-search`)

It finds relevant records and answers or acts on a request of a person.

:::requirement{#existing-work-is-offered-first}
When someone writes a new issue or question, Dispatch offers the nearest existing issues and answered questions, up to a stated number. It says when it could not search, and that is different from no match.
:::

**Level two:** 10.

Also about it: `dispatch-answers-and-acts-without-an-agent`, `dispatch-takes-deployment-values-from-its-settings`.

#### Verification (component `dispatch-verification`)

It connects statements to checks and records what each check run found.

Also about it: `delivered-changes-meet-their-requirements`, `check-results-say-what-they-prove`, `each-bound-statement-has-its-action-and-check`, `dispatch-holds-the-approved-spec-as-nodes`, `each-spec-node-has-the-persons-approval`.

#### Measurements (component `dispatch-measurements`)

It computes and shows work duration, delivery health, and each other measure that a project registers.

:::requirement{#delivery-is-measured}
Dispatch shows recorded measures of how long work takes, what it costs and how often changes fail, each with its method. A missing measure never shows as zero.
:::

**Today.** Dispatch shows no measure of cost. With no failure reports, it shows a failure rate of zero, also for a window with no deployment. **Level two:** 12.

Also about it: `projects-try-agent-settings-by-experiment`, `dispatch-reads-github-again-on-stated-timers`.

#### Caller access (component `dispatch-access`)

It decides what each person, agent or service can read or change.

Also about it: `dispatch-actions-need-no-browser`, `each-act-names-its-caller`, `dispatch-takes-deployment-values-from-its-settings`, `an-approval-records-person-time-request-and-hash`.

#### Review of agent writing (component `dispatch-write-review`)

It reviews what an agent writes to a person before Dispatch takes it, and returns advice or a refusal.

Also about it: `agent-writing-review-follows-its-settings`.

#### Agent views (component `dispatch-agent-views`)

It lists agent sessions and shows one session while it runs, where a person can steer it.

Also about it: `the-person-reads-and-steers-running-agents`.

#### Web app (component `dispatch-web-app`)

It gives each part of Dispatch one way to navigate and to lay out a page, by keyboard and on a phone.

:::requirement{#pages-work-for-each-reader-and-hold-their-place}
Each page of Dispatch works with the keyboard alone and on a phone. Content that others add does not move a reader's place in older content while that content and its version stay on screen.
:::

**Today.** The conversation page follows a new thread while the reader stays at the newest one, so older content can move down. **Level two:** 10.

Also about it: `work-is-arranged-for-the-person`, `dispatch-draws-one-thing-one-way`, `each-issue-reads-on-its-own-page`, `dispatch-refuses-document-growth-past-its-bound`, `dispatch-routes-come-from-one-published-definition`, `dispatch-release-serves-two-compatible-builds`.

#### Command line (component `dispatch-command-line`)

It gives each action of Dispatch as a command in a terminal, for an agent or a person.

Also about it: `dispatch-actions-need-no-browser`, `dispatch-routes-come-from-one-published-definition`.

#### Dispatch requests (interface `dispatch-requests`)

The web app, the command line and other callers use one described set of requests.

:::requirement{#dispatch-routes-come-from-one-published-definition}
Dispatch's server publishes one definition of its routes, and its web app and command line take each request and answer from it. A client that does not follow a changed route fails to build.
:::

**New.** **Today.** The command line names each route and its answer type by hand, so a renamed route does not fail its build. **Level two:** 0.

Also about it: `dispatch-actions-need-no-browser`, `merged-changes-deploy-without-a-person`, `dispatch-answers-and-acts-without-an-agent`, `dispatch-refuses-text-past-its-block-bounds`, `dispatch-release-serves-two-compatible-builds`, `each-interface-names-its-declared-contract`.

#### Approved content (interface `approved-content`)

It ties an approval to the exact text that the person approved, and shows when that text changed after.

:::requirement{#an-approval-stands-only-for-its-version}
An approval in Dispatch stands for the version it names until a changes request or a new version. A new version keeps it only when its text is the approved text, apart from the approver's own later answers.
:::

**New.** **Today.** A new version also keeps the approval when an agent reworded another open question in it. Any caller can end a plan's approval by recording a review of an older plan version. **Level two:** 3.

Also about it: `an-approval-records-person-time-request-and-hash`, `each-spec-node-has-the-persons-approval`.

### Envoy

It connects agent sessions to messages, outside events and controlled credentials.

:::requirement{#envoy-knows-session-presence-and-role-holders}
Envoy records whether each session is live, unresponsive or gone, and which process holds each role. A lookup or a delivery attempt never releases a role, and a release frees only the claim of the process that holds it.
:::

**Today.** A forced release, and a claim from before claims named their process, can free a role that another process holds. **Level two:** 16.

:::requirement{#envoy-keeps-messages-until-saved}
Envoy keeps and redelivers a message accepted for a session until the session saves it in its conversation, or a stated limit removes it. Envoy tells a session when a message that it did not get is gone.
:::

**Today.** Envoy reports a gap only from a saved cursor or a kept delivery state. Without either, a message that expired can go with no notice. **Level two:** 11.

**Least sure.** Whether a load test's example file counts as code of Envoy.

:::requirement{#envoy-takes-hosts-and-accounts-from-settings}
Envoy's listener and secrets broker take each host name, service account and machine that they trust from their deployment's settings. Envoy's code, tests and examples name no host, cloud project or account of one deployment.
:::

**New.** **Today.** A load test example names one deployment's cloud project. Broker tests name that deployment's domain, a person's account and a person's secrets. **Level two:** 3.

#### Message delivery (component `envoy-delivery`)

It stores and delivers messages until the receiving session records them.

Also about it: `envoy-keeps-messages-until-saved`, `envoy-decides-each-role-act-from-its-claim-record`, `dispatch-publishes-each-record-change-to-its-subscribers`.

#### Session and role directory (component `envoy-directory`)

It records session presence, roles and the session that holds each role.

:::requirement{#envoy-decides-each-role-act-from-its-claim-record}
Envoy's listener decides each grant, renewal, release and delivery of a role from that role's claim record, read when it acts. When the read fails, the act waits and changes nothing.
:::

**New.** **Today.** Dispatch's outbox also finds a role's holder in Envoy's stores, and it forwards or keeps role messages by its own rule. **Level two:** 1.

Also about it: `envoy-knows-session-presence-and-role-holders`, `envoy-keeps-messages-until-saved`, `work-keeps-its-owner-and-resumes-after-an-unexpected-stop`.

#### Outside events (component `envoy-events`)

It turns events from outside services into messages that the systems can act on.

:::requirement{#envoy-turns-each-outside-event-into-a-message}
Envoy takes GitHub, Slack and Ghost Wispr events at its webhooks, checks the signature of each, and publishes the supported ones as messages. It folds a commit's check runs into one record, and publishes a message when they settle.
:::

**New.** **Today.** Envoy answers a Slack set-up request before it checks a signature, and checks a Ghost Wispr signature only where a secret is set. One event can give no message, or more than one. **Level two:** 4.

Also about it: `dispatch-reads-github-again-on-stated-timers`.

#### Secrets broker (component `envoy-secrets`)

It authorizes credential use and forwards requests that need protected secrets.

:::requirement{#the-broker-controls-credentials}
Envoy's secrets broker allows the use of a credential under the rule of that secret: its scope, its duration and its approval. The rule of a secret says whether the broker releases its value, mints a short-lived token, or makes the call itself.
:::

**Level two:** 10.

Also about it: `each-act-names-its-caller`, `envoy-takes-hosts-and-accounts-from-settings`.

#### Session messaging (component `envoy-session-messaging`)

It lets an agent session register its presence, and receive and send messages through Envoy.

Also about it: `envoy-knows-session-presence-and-role-holders`, `merged-changes-deploy-without-a-person`, `the-person-reads-and-steers-running-agents`, `shipped-prompts-and-skills-name-no-deployment`, `envoy-takes-hosts-and-accounts-from-settings`.

#### Session protocol (interface `session-protocol`)

It keeps presence, role ownership and delivery consistent when a session loses its connection for a short time.

Also about it: `envoy-knows-session-presence-and-role-holders`, `envoy-keeps-messages-until-saved`, `envoy-decides-each-role-act-from-its-claim-record`, `interfaces-that-carry-changes-name-source-or-timer`, `each-interface-names-its-declared-contract`.

### All three systems

These statements are about components of more than one system, or about the components that serve all three.

:::requirement{#projects-try-agent-settings-by-experiment}
A project can try an agent setting, such as memory, by drawing a variant at random for each new issue or each new issue tree. Legion records the variant that each issue or issue tree drew, so that a reader can compare the outcomes of variants.
:::

**Level two:** 9.

:::requirement{#failures-are-recorded-with-their-cause}
Legion and Envoy record their failures with the cause, and alert on each condition that they declare. A health answer of Dispatch or Envoy says whether that service can do its work now.
:::

**Today.** When a status write to Dispatch fails, the daemon answers with a general error and records no cause. **Level two:** 9.

:::requirement{#merged-changes-deploy-without-a-person}
A merged change to the three systems reaches production by itself, unless a recorded hold, a failed safety check or a required approval stops it. During a release, no request goes unanswered: it succeeds or its caller gets an error.
:::

**Today.** A deployment loop can deploy each new image of Dispatch, where an operator runs it. A release publishes the images of Legion and Envoy and replaces no running service. **Level two:** 9.

:::requirement{#work-keeps-its-owner-and-resumes-after-an-unexpected-stop}
Each work unit that Legion runs has one owner on record, the agent that answers for it. After an unexpected stop, Legion resumes the agent with any task still owed, in its saved conversation where it exists, within its restart budget.
:::

**Today.** The code has no work unit, so the first sentence states an approved aim. **Level two:** 13.

:::requirement{#dispatch-answers-and-acts-without-an-agent}
From the records of Dispatch, a person can get an answer that names its sources, and can do an operation by a request in words. Dispatch's server calls the models and runs their tools itself, needs no running agent session, and records each such operation.
:::

**Today.** The server answers a question with its sources, but its model can only search. A request in words cannot run an operation, which needs a chosen action and its arguments. **Level two:** 10.

:::requirement{#delivered-changes-meet-their-requirements}
Legion lands a change only when the checks of each statement that it builds or restores pass on the commit that lands. Each behavior that it adds or fixes has a test that fails without the change.
:::

**Today.** The statements of The Spec have no checks yet, so the first sentence states an approved aim. A commit pushed after review can merge with no new check and no regression test. **Level two:** 6.

:::requirement{#each-act-names-its-caller}
Each Dispatch act, Envoy message, broker call and Legion commit names its caller, and a Dispatch act names the person an agent acted for. Each system refuses a read or a write that the caller has no permission for.
:::

**Today.** Envoy accepts a message that names no sending session. It also takes the sender name that a shared token claims, with no check against a session. **Level two:** 10.

:::requirement{#check-results-say-what-they-prove}
Each result of a check names the check, the run that produced it, the change that it tested and what it found. A result keeps its verdict, Dispatch shows separately whether it holds for the change in question, and a check that did not run never passes.
:::

**Today.** A result needs its check and its commit, but its run and what it found can be empty. So a pass of a check that never ran looks like a real one. **Level two:** 9.

:::requirement{#the-person-reads-and-steers-running-agents}
From Dispatch, a person can read a running agent session's conversation and send it a message or a command. Where a session cannot take one of these, Dispatch says so.
:::

**Level two:** 9.

#### Build and release (component `build-and-release`)

It tests each change before or after it merges, and releases and deploys what merged.

:::requirement{#each-change-runs-the-checks-its-files-can-affect}
For each merged commit, each merge-queue check that its files can affect runs on code containing it, before the merge or on main after. Each run selects its checks by its event, and a change's run runs a passing check once in each configuration that can change its result.
:::

**Today.** A merge outside the queue with a skip marker can reach main with no affected check, before or after the merge. **Level two:** 16.

:::requirement{#main-branch-merges-only-through-its-queue}
The main branch of the three systems' repository takes a change only from its merge queue, as a squashed pull request with one approving review. GitHub merges it only when the checks ci-ok and pr-title pass and the change adds no CodeQL alert of high severity or more.
:::

**New.** **Level two:** 3.

Also about it: `merged-changes-deploy-without-a-person`, `delivered-changes-meet-their-requirements`, `check-results-say-what-they-prove`, `dispatch-release-serves-two-compatible-builds`.

#### Model execution (component `shared-model-execution`)

It runs a model with tools in one way for each part of the three systems that needs a model.

Also about it: `dispatch-answers-and-acts-without-an-agent`, `dispatch-takes-deployment-values-from-its-settings`.

#### Legion to Dispatch (interface `legion-dispatch`)

It exchanges work, approvals, execution state and results between the two systems.

**Least sure.** A controller may change a status, so the gap in the Today line may want a fence on that route, not its removal.

:::requirement{#legion-writes-dispatch-status-over-what-it-read}
Legion writes an issue's status in Dispatch only at the Dispatch sequence that it last read. Dispatch refuses the write when the issue changed after that read.
:::

**New.** **Today.** The status route of the controller and the architects writes with no sequence. Each agent also holds the daemon's credential for Dispatch and can set a status, which the daemon sets back on an issue that it runs. **Level two:** 2.

Also about it: `legion-starts-work-in-order-under-its-cap`, `the-controller-sets-the-status-of-issues`, `legion-copies-dispatch-issues-by-sequence`, `the-daemon-builds-issue-state-from-events`, `interfaces-that-carry-changes-name-source-or-timer`, `each-interface-names-its-declared-contract`.

#### Legion to Envoy (interface `legion-envoy`)

It carries workflow notices and session presence between the two systems.

Also about it: `the-daemon-builds-issue-state-from-events`, `interfaces-that-carry-changes-name-source-or-timer`, `each-interface-names-its-declared-contract`.

#### Dispatch to Envoy (interface `dispatch-envoy`)

Dispatch publishes each change of a record and chooses who gets it, and Envoy delivers it to sessions.

:::requirement{#dispatch-publishes-each-record-change-to-its-subscribers}
Dispatch appends each change of an issue, document or project to its event log, and a publisher sends it without a timed search. One publisher holds the work, a standby takes over within six seconds, and each destination keeps one record's order.
:::

**New.** **Today.** A copy goes again when Dispatch fails to record that it was sent. A session that subscribes twice can get two copies out of order. **Level two:** 3.

Also about it: `waiting-work-reaches-the-persons-inbox`, `the-person-reads-and-steers-running-agents`, `envoy-decides-each-role-act-from-its-claim-record`, `dispatch-reads-github-again-on-stated-timers`, `interfaces-that-carry-changes-name-source-or-timer`, `dispatch-release-serves-two-compatible-builds`, `each-interface-names-its-declared-contract`.

#### Outside event intake (interface `outside-event-intake`)

It brings outside changes into work state and delivery measurements.

:::requirement{#dispatch-reads-github-again-on-stated-timers}
Dispatch learns of a GitHub change from the events that Envoy publishes. Where an event never arrives, its own timers bring the record up to date: a sweep after two minutes, and reads after five minutes.
:::

**New.** **Today.** Dispatch holds its own readers inside Envoy's stream, and writes what it reads into Envoy's record of a head. The component import follows no push event, and the redelivery sweep adds up to a tenth to its wait. **Level two:** 5.

Also about it: `delivery-is-measured`, `the-daemon-builds-issue-state-from-events`, `envoy-turns-each-outside-event-into-a-message`, `interfaces-that-carry-changes-name-source-or-timer`.

#### Approval to work (interface `approval-gates-work`)

It tells Legion which approval of which document an issue waits on, and whether that approval stands.

Also about it: `steps-follow-the-chosen-workflow`, `legion-holds-a-tree-until-its-approvals-stand`.

#### Credential requests (interface `credential-decisions`)

Dispatch lists a credential request and links to the page of the broker, where the person decides.

Also about it: `waiting-work-reaches-the-persons-inbox`, `the-broker-controls-credentials`.

#### Caller identity (interface `caller-identity`)

It carries authenticated caller identity and permissions across service boundaries.

Also about it: `legion-gives-each-session-its-environment`, `the-broker-controls-credentials`, `each-act-names-its-caller`, `each-interface-names-its-declared-contract`.

#### Check results (interface `check-results`)

It makes each result of a check available in Dispatch with its source and its commit, from an agent or from CI.

Also about it: `check-results-say-what-they-prove`.

### Rules of structure

Each of these statements holds across components after their specifics, and a query on The Spec's graph decides it.

**Least sure.** The check sees only the records that some statement names, so a copy that no statement names escapes it.

:::requirement{#each-record-kind-has-one-writer}
The Spec names one writer component for each kind of record or shared value that its statements name. It names each other component that holds that record as a copy.
:::

**New.** **Today.** Dispatch's questions, issues and documents each have a second writer component, and Dispatch also writes two stores of Envoy. Legion's queue of effects has three writers, and Legion writes the status of a Dispatch issue from three places. **Check.** A query on The Spec's graph returns each record node with no writes relation, or with two or more. It also returns each copies relation whose holder has no statement about that record. No node returned is a pass. The graph does not hold this yet. It needs a record node for each kind of record or shared value that a statement names. It needs one writes relation from the writer component, and a copies relation from each other holder. Each relation comes from the statement that names it. The failing example is a copy of the graph with a second writes relation on one record. **Level two:** 2.

About: `legion`, `dispatch`, `envoy`.

:::requirement{#each-bound-statement-has-its-action-and-check}
Each statement of The Spec that states a bound also states what its component does at that bound. Each such statement has a check that runs the component at that bound.
:::

**New.** **Today.** The graph has no mark that says a statement states a bound, so no query can find these statements yet. Dispatch already links a check to the statement it serves and lists a statement with no check. **Check.** One query at one transaction returns each statement marked as stating a bound that has no at-bound value. It also returns each such statement that no check serves at the bound's load. No node returned is a pass. The graph needs three things that it does not hold. First, a bound mark on a statement, with its number, unit, load and share of runs. Second, an at-bound value on it: refuses with a reason, waits, keeps, delays, or cuts and says so. Third, a load on each check. The serves relation from a check to its statement exists today. The failing example is a copy of the graph with the at-bound value of one statement removed. **Level two:** 2.

About: `dispatch-verification`.

:::requirement{#interfaces-that-carry-changes-name-source-or-timer}
Each interface that carries changes of records between components has a statement that names the events it carries, or the timer of its reads. A statement of a timer gives its period.
:::

**New.** **Today.** The Spec marks no statement as an event source or as a stated timer. So the query below returns nothing until each such statement carries that mark. **Check.** The query reads the graph at one transaction. It takes each interface whose purpose or whose statements say that it carries changes of records. It follows the about relation from each statement to that interface. It returns each interface for which no statement names an event source, and none names a read with a period. An empty answer is the pass. The graph holds the about relation and the kinds today. It holds no field that says whether a statement names an event source or a timer, and none that holds a period. The failing example is a copy of the graph with the event source of one statement removed. **Level two:** 2.

About: `outside-event-intake`, `dispatch-envoy`, `legion-envoy`, `legion-dispatch`, `session-protocol`.

:::requirement{#each-interface-names-its-declared-contract}
Each interface of The Spec has a statement that names the declared contract of what its sides exchange. That statement has a check that compares each side with the contract.
:::

**New.** **Today.** No interface names its contract in The Spec yet. In the code, Legion's requests to Dispatch and to Envoy, and the broker's reads of Dispatch, use hand-written copies. **Check.** A query over The Spec's graph returns each interface that has no contract relation to a statement. It also returns each statement at the end of a contract relation that has no check, or no about relation to that interface. The graph must gain the contract relation, from an interface to the statement that names its contract. The relations that an interface holds today, joins and about, name no contract. The failing example is a copy of the graph with one interface's contract relation removed. A query that cannot read the interface nodes gives unknown. **Level two:** 1.

About: `worker-contract`, `dispatch-requests`, `session-protocol`, `dispatch-envoy`, `legion-dispatch`, `legion-envoy`, `caller-identity`.

:::requirement{#each-spec-node-has-the-persons-approval}
Each statement, component and interface of the approved Spec has a record of the person's approval that covers its current text. Each check has one too, or a check review that found it no weaker.
:::

**New.** **Today.** The Spec is not built, and an approval covers a whole document. So no node of the graph carries an approval of its own. **Check.** A query on The Spec's graph returns each statement, component, interface and check of the approved Spec that no approval covers. An approval covers a node through an approves relation whose hash matches the node's current text. A check that a check review approved is left out. An empty answer passes, and a query that cannot read gives unknown. The graph must hold an approval node for each approval, with its person, time and hash. It must hold an approves relation to each node that the hash covers, and a check review node for each reviewed check. The failing example is a copy of the graph with the approves relation of one statement removed. **Level two:** 0.

About: `dispatch-records`, `dispatch-decisions`, `approved-content`, `dispatch-verification`.

## Components

| Id | Kind | Name | Purpose | Parent or joins |
| --- | --- | --- | --- | --- |
| legion | component | Legion | It runs agents to turn approved work into software changes. | none |
| legion-coordination | component | Work coordination | It admits issues, shares the capacity of a project between issues, and keeps ownership and dependencies consistent. | legion |
| legion-workflows | component | Workflows | It decides which steps of a work unit can run, from the unit's workflow version, what the unit waits on and the recorded outcomes. | legion |
| legion-runners | component | Agent runs | It starts, resumes and observes agents with their assigned roles. | legion |
| legion-environments | component | Work environments | It provides isolated computing and repository workspaces for agent work. | legion |
| legion-change-integration | component | Change integration | It publishes the change of a work unit for review and brings it into the repository under the rules of that repository. | legion |
| legion-settings | component | Deployment settings | It applies the runtimes, models, role instructions and limits that a deployment chooses, and keeps the experiment group of each issue or issue tree. | legion |
| legion-controller | component | Project controller | It is the one agent of a project that ranks waiting work and settles what the owners of work cannot agree on. | legion |
| legion-work-participation | component | Work participation | It lets an agent in its session take its assignment, report its state and its completion, and get a reminder of open work. | legion |
| dispatch | component | Dispatch | It holds work, documents and decisions for people and their agents. | none |
| dispatch-records | component | Records | It holds each record of Dispatch and the relations between records, and it resolves each reference to a record. | dispatch |
| dispatch-documents | component | Document editing | It lets people and agents write and read versioned text and attachments. | dispatch |
| dispatch-conversations | component | Conversations | It holds the threads of comments, messages and replies between people and agents, with their authors and their history. | dispatch |
| dispatch-decisions | component | Decisions and approvals | It records questions, answers, decisions and what the person approved. | dispatch |
| dispatch-inbox | component | Inbox | It shows a person what waits on that person, and keeps what that person read, answered or dismissed. | dispatch |
| dispatch-search | component | Search and actions | It finds relevant records and answers or acts on a request of a person. | dispatch |
| dispatch-verification | component | Verification | It connects statements to checks and records what each check run found. | dispatch |
| dispatch-measurements | component | Measurements | It computes and shows work duration, delivery health, and each other measure that a project registers. | dispatch |
| dispatch-access | component | Caller access | It decides what each person, agent or service can read or change. | dispatch |
| dispatch-write-review | component | Review of agent writing | It reviews what an agent writes to a person before Dispatch takes it, and returns advice or a refusal. | dispatch |
| dispatch-agent-views | component | Agent views | It lists agent sessions and shows one session while it runs, where a person can steer it. | dispatch |
| dispatch-web-app | component | Web app | It gives each part of Dispatch one way to navigate and to lay out a page, by keyboard and on a phone. | dispatch |
| dispatch-command-line | component | Command line | It gives each action of Dispatch as a command in a terminal, for an agent or a person. | dispatch |
| envoy | component | Envoy | It connects agent sessions to messages, outside events and controlled credentials. | none |
| envoy-delivery | component | Message delivery | It stores and delivers messages until the receiving session records them. | envoy |
| envoy-directory | component | Session and role directory | It records session presence, roles and the session that holds each role. | envoy |
| envoy-events | component | Outside events | It turns events from outside services into messages that the systems can act on. | envoy |
| envoy-secrets | component | Secrets broker | It authorizes credential use and forwards requests that need protected secrets. | envoy |
| envoy-session-messaging | component | Session messaging | It lets an agent session register its presence, and receive and send messages through Envoy. | envoy |
| build-and-release | component | Build and release | It tests each change before or after it merges, and releases and deploys what merged. | none |
| shared-model-execution | component | Model execution | It runs a model with tools in one way for each part of the three systems that needs a model. | none |
| legion-dispatch | interface | Legion to Dispatch | It exchanges work, approvals, execution state and results between the two systems. | legion, dispatch |
| legion-envoy | interface | Legion to Envoy | It carries workflow notices and session presence between the two systems. | legion, envoy |
| dispatch-envoy | interface | Dispatch to Envoy | Dispatch publishes each change of a record and chooses who gets it, and Envoy delivers it to sessions. | dispatch-conversations, dispatch-inbox, dispatch-agent-views, envoy-delivery, envoy-directory |
| dispatch-requests | interface | Dispatch requests | The web app, the command line and other callers use one described set of requests. | dispatch-web-app, dispatch-command-line, dispatch-access |
| worker-contract | interface | Worker contract | An agent in its session and Legion agree on compatible messages and on the work that a report is about. | legion-runners, legion-work-participation |
| session-protocol | interface | Session protocol | It keeps presence, role ownership and delivery consistent when a session loses its connection for a short time. | envoy-session-messaging, envoy-directory, envoy-delivery |
| outside-event-intake | interface | Outside event intake | It brings outside changes into work state and delivery measurements. | envoy-events, legion-workflows, dispatch-measurements |
| approval-gates-work | interface | Approval to work | It tells Legion which approval of which document an issue waits on, and whether that approval stands. | legion-workflows, dispatch-decisions |
| approved-content | interface | Approved content | It ties an approval to the exact text that the person approved, and shows when that text changed after. | dispatch-documents, dispatch-decisions |
| credential-decisions | interface | Credential requests | Dispatch lists a credential request and links to the page of the broker, where the person decides. | dispatch-inbox, envoy-secrets |
| caller-identity | interface | Caller identity | It carries authenticated caller identity and permissions across service boundaries. | dispatch-access, envoy-directory, envoy-secrets, legion-environments |
| check-results | interface | Check results | It makes each result of a check available in Dispatch with its source and its commit, from an agent or from CI. | dispatch-verification, build-and-release, legion-runners |
