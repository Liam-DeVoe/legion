# Notes of level one

### legion-starts-work-in-order-under-its-cap

- Kind: behavior
- About: legion-coordination, legion-controller, legion-dispatch
- Supports: none

### the-daemon-moves-work-by-recorded-rules

- Kind: rule of structure
- About: legion-workflows, legion-coordination, legion-runners, worker-contract
- Supports: none

### steps-follow-the-chosen-workflow

- Kind: rule of structure
- About: legion-workflows, approval-gates-work
- Supports: none

### legion-gives-each-session-its-environment

- Kind: behavior
- About: legion-environments, caller-identity
- Supports: none

### the-controller-sets-the-status-of-issues

- Kind: behavior
- About: legion-controller, legion-dispatch
- Supports: none

### agent-writing-review-follows-its-settings

- Kind: behavior
- About: dispatch-write-review, dispatch-conversations, dispatch-documents
- Supports: none

### work-is-arranged-for-the-person

- Kind: behavior
- About: dispatch-records, dispatch-web-app
- Supports: none

### dispatch-draws-one-thing-one-way

- Kind: behavior
- About: dispatch-web-app, dispatch-records
- Supports: none

### each-issue-reads-on-its-own-page

- Kind: behavior
- About: dispatch-web-app, dispatch-conversations, dispatch-documents
- Supports: none

### records-are-linked-by-named-relations

- Kind: rule of structure
- About: dispatch-records, dispatch-documents, dispatch-conversations, dispatch-decisions
- Supports: none

### waiting-work-reaches-the-persons-inbox

- Kind: behavior
- About: dispatch-inbox, dispatch-decisions, dispatch-conversations, dispatch-envoy, credential-decisions
- Supports: none

### edits-lose-nothing

- Kind: behavior
- About: dispatch-documents, dispatch-conversations, dispatch-records
- Supports: none

### dispatch-actions-need-no-browser

- Kind: behavior
- About: dispatch-requests, dispatch-command-line, dispatch-access
- Supports: none

### existing-work-is-offered-first

- Kind: behavior
- About: dispatch-search
- Supports: none

### delivery-is-measured

- Kind: behavior
- About: dispatch-measurements, outside-event-intake
- Supports: none

### pages-work-for-each-reader-and-hold-their-place

- Kind: behavior
- About: dispatch-web-app
- Supports: none

### envoy-knows-session-presence-and-role-holders

- Kind: behavior
- About: envoy-directory, session-protocol, envoy-session-messaging
- Supports: none

### envoy-keeps-messages-until-saved

- Kind: behavior
- About: envoy-delivery, envoy-directory, session-protocol
- Supports: none

### the-broker-controls-credentials

- Kind: behavior
- About: envoy-secrets, credential-decisions, caller-identity
- Supports: none

### projects-try-agent-settings-by-experiment

- Kind: behavior
- About: legion-settings, legion-coordination, dispatch-measurements
- Supports: none

### failures-are-recorded-with-their-cause

- Kind: behavior
- About: legion, dispatch, envoy
- Supports: none

### merged-changes-deploy-without-a-person

- Kind: behavior
- About: build-and-release, dispatch-requests, envoy-session-messaging
- Supports: none

### work-keeps-its-owner-and-resumes-after-an-unexpected-stop

- Kind: behavior
- About: legion-runners, legion-coordination, legion-environments, worker-contract, envoy-directory
- Supports: none

### dispatch-answers-and-acts-without-an-agent

- Kind: behavior
- About: dispatch-search, shared-model-execution, dispatch-requests
- Supports: none

### delivered-changes-meet-their-requirements

- Kind: behavior
- About: legion, legion-workflows, legion-change-integration, build-and-release, dispatch-verification
- Supports: none

### each-act-names-its-caller

- Kind: behavior
- About: dispatch-access, caller-identity, envoy-secrets
- Supports: none

### check-results-say-what-they-prove

- Kind: behavior
- About: dispatch-verification, check-results, build-and-release
- Supports: none

### the-person-reads-and-steers-running-agents

- Kind: behavior
- About: dispatch-agent-views, dispatch-envoy, envoy-session-messaging, legion-runners
- Supports: none

### each-change-runs-the-checks-its-files-can-affect

- Kind: behavior
- About: build-and-release
- Supports: none

### legion-copies-dispatch-issues-by-sequence

- Kind: behavior
- About: legion-coordination, legion-dispatch
- Supports: none

### envoy-decides-each-role-act-from-its-claim-record

- Kind: behavior
- About: envoy-directory, envoy-delivery, session-protocol, dispatch-envoy
- Supports: none

### legion-writes-dispatch-status-over-what-it-read

- Kind: behavior
- About: legion-dispatch, legion-coordination, legion-controller, dispatch-records
- Supports: none

### each-record-kind-has-one-writer

- Kind: rule of structure
- About: legion, dispatch, envoy
- Supports: none

### legion-takes-deployment-values-from-its-settings

- Kind: behavior
- About: legion-settings, legion-environments, legion-runners
- Supports: none

### shipped-prompts-and-skills-name-no-deployment

- Kind: property
- About: legion-runners, legion-settings, envoy-session-messaging
- Supports: none

### dispatch-takes-deployment-values-from-its-settings

- Kind: behavior
- About: dispatch, dispatch-access, dispatch-search, shared-model-execution
- Supports: none

### envoy-takes-hosts-and-accounts-from-settings

- Kind: behavior
- About: envoy, envoy-secrets, envoy-session-messaging
- Supports: none

### dispatch-refuses-text-past-its-block-bounds

- Kind: behavior
- About: dispatch-conversations, dispatch-requests
- Supports: none

### dispatch-refuses-document-growth-past-its-bound

- Kind: behavior
- About: dispatch-documents, dispatch-web-app
- Supports: none

### legion-paces-the-session-wakes-it-causes

- Kind: behavior
- About: legion-runners, legion-settings
- Supports: none

### each-bound-statement-has-its-action-and-check

- Kind: rule of structure
- About: dispatch-verification
- Supports: none

### the-daemon-builds-issue-state-from-events

- Kind: behavior
- About: legion-workflows, legion-dispatch, outside-event-intake, legion-envoy
- Supports: none

### dispatch-publishes-each-record-change-to-its-subscribers

- Kind: behavior
- About: dispatch-envoy, dispatch-records, envoy-delivery
- Supports: none

### envoy-turns-each-outside-event-into-a-message

- Kind: behavior
- About: envoy-events, outside-event-intake
- Supports: none

### dispatch-reads-github-again-on-stated-timers

- Kind: behavior
- About: outside-event-intake, dispatch-measurements, dispatch-envoy, envoy-events
- Supports: none

### interfaces-that-carry-changes-name-source-or-timer

- Kind: rule of structure
- About: outside-event-intake, dispatch-envoy, legion-envoy, legion-dispatch, session-protocol
- Supports: none

### the-daemon-answers-each-plugin-in-its-contract

- Kind: behavior
- About: worker-contract, legion-runners, legion-work-participation
- Supports: none

### dispatch-routes-come-from-one-published-definition

- Kind: description of structure
- About: dispatch-requests, dispatch-web-app, dispatch-command-line
- Supports: none

### dispatch-release-serves-two-compatible-builds

- Kind: behavior
- About: dispatch, dispatch-requests, dispatch-records, dispatch-envoy, dispatch-web-app, build-and-release
- Supports: none

### each-interface-names-its-declared-contract

- Kind: rule of structure
- About: worker-contract, dispatch-requests, session-protocol, dispatch-envoy, legion-dispatch, legion-envoy, caller-identity
- Supports: none

### merger-merges-only-the-head-it-read

- Kind: behavior
- About: legion-change-integration, legion-workflows
- Supports: none

### the-daemon-takes-the-merge-verdict-from-github

- Kind: behavior
- About: legion-change-integration, legion-workflows
- Supports: none

### main-branch-merges-only-through-its-queue

- Kind: behavior
- About: build-and-release, legion-change-integration
- Supports: none

### legions-own-merge-conditions-only-hold-a-merge

- Kind: behavior
- About: legion-change-integration, legion-workflows
- Supports: none

### an-approval-records-person-time-request-and-hash

- Kind: behavior
- About: dispatch-decisions, approved-content, dispatch-access
- Supports: none

### an-approval-stands-only-for-its-version

- Kind: behavior
- About: approved-content, dispatch-decisions, dispatch-documents
- Supports: none

### legion-holds-a-tree-until-its-approvals-stand

- Kind: behavior
- About: legion-workflows, approval-gates-work, legion-change-integration
- Supports: none

### dispatch-holds-the-approved-spec-as-nodes

- Kind: behavior
- About: dispatch-decisions, dispatch-records, dispatch-verification
- Supports: none

### each-spec-node-has-the-persons-approval

- Kind: rule of structure
- About: dispatch-records, dispatch-decisions, approved-content, dispatch-verification
- Supports: none
