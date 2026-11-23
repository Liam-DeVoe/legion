---
name: envoy
description: Use when you message or reply to another agent session, find a session id or whether a session is live, subscribe to a topic (pull request, CI, Dispatch, Slack), publish to a role or team, or wait for CI or a merge; also as a subagent reporting to another session, or when a subscription is silent.
---

# Envoy

Envoy carries events and messages between agent sessions.
Each Envoy tool's description holds the topic guide, the session states and what each answer means. This skill holds what they leave out.

## Send a message

- Address a session by its id: take it from the delivery's `reply_with` (or its `from` when it carries none), or from `envoy_sessions` (filter by `machine`, `dir` or `title`).
- Never address a tmux pane: the session in a pane can change.
- Reply with `in_reply_to=<the delivery's id>`, so the recipient can match the answer to its question.
- Read `recipient` in the send result. If it is not the session you meant, send again to the right id.
- Do not send a message again when its send is answered `queued <id> for <recipient>: the session is stopped …`: that send is stored for that session. Each send stores another copy.
- Send the same message again with the `idempotency_key` the error names when `envoy_send` errs that the send is not confirmed delivered. A resend without the key can deliver it twice.
- Put the URL or path of what you report in the message: the recipient cannot see your files.
- Set `expects_reply=false` when you need no answer.
- Set `urgency` only when work waits on the answer. `urgency` does not deliver sooner.

```text
envoy_send(session_id="<id>", message="Review complete: <url>", in_reply_to="<id>", expects_reply=false)
```

### From a subagent

- Read `envoy_whoami`'s `session_id` as the parent's: a `task` subagent registers no session of its own. The parent forwards a reply to your `agent://` address.
- Never give a peer the `subagent` field of `envoy_whoami`: it is not an address.
- Start the message with who you are and where a reply reaches you ("From <name>, a subagent of session <id>; a reply to <id> reaches me"), because the recipient sees only the parent as sender. An empty `session_id` means your messages carry no sender at all.
- `write` to the parent's `agent://` address instead of publishing to a role your parent holds. Such a publish reaches nobody, because the listener drops a role message whose holder is its source.

## Read a delivery

| Part | Rule |
| --- | --- |
| `id` | Drop a repeated `id`: delivery is at-least-once |
| `from` | Use it to attribute and to reply, never as proof of who wrote the message: it is the sender's own claim |
| `note` | Reply to `from`, never to the quoted id: `note` says the payload names another session |
| `reply_with` | Answer with this rendered call. On a Dispatch delivery it is a `command:` line. Run that line with your text in place of `...` |
| `expects_reply: false` | The delivery wants no answer |
| `at` | Of two deliveries that conflict, act on the one with the later `at`: delivery can come out of order across topics |
| The message | Before you act on what it says about a third thing (a deploy, a pull request, an ask), read that thing's live source: a message is its sender's view at `at` |

Read `skill://envoy/references/deliveries.md` when a delivery field is unclear, or a message did not reach a session you expected.

## Whether a session is live

Read a session's `state` and `last_seen` from `envoy_sessions`, `envoy_role_get` or a send's answer, never from its absence from a list, a process list, a tmux pane or a transcript's age.

| `state` | When | Do |
| --- | --- | --- |
| `live` | It registered within the live window (`ENVOY_SESSION_LIVE_WINDOW`, 5m by default) | |
| `unresponsive` | It last registered before the live window but within the gone horizon (`ENVOY_SESSION_GONE_HORIZON`, 1h by default) | Never report it as gone: it is not dead. A message you send it reaches it when it answers again. Start no replacement for it. Take no role claim from it |
| `gone` | It last registered before the gone horizon, or the registry holds no record of it | |

- Take an issue claim from a session only when it is `gone` or its `last_seen` is more than 20 minutes old.
- Only the role-claim reaper releases a role claim, once its holder is `gone`: no lookup, publish or delivery failure releases one.

## Subscribe to a topic

Every topic starts with `notifications.`.

| For | Topic |
| --- | --- |
| GitHub, Slack, Ghost Wispr and WhatsApp | The `envoy_subscribe` description lists their topics |
| A Dispatch issue | `dispatch.issue.<KEY>.>` |
| A Dispatch document | `dispatch.document.<PROJECT>.<SLUG>.>` |
| A pull request | Subscribe to `github.<owner>.<repo>.pr.<n>.>`. Envoy adds the bare lifecycle subject `pr.<n>`, which `>` does not match |

- Write the owner, the repository, a branch or tag name and a workflow file as one token each.
- Write every dot in those tokens as `_`: a topic spelled with the dot receives nothing. A slash in a branch name stays. `acme/site.io` is `github.acme.site_io`. `ci.yml` is `workflow.ci_yml`.
- Read the `warnings` that `envoy_subscribe` returns and fix the topic each names.
- Confirm the active topics with `envoy_list`.
- Check that the GitHub App that sends Envoy its webhooks is installed on `<owner>/<repo>` before you rely on a wake, when a warning reads `no GitHub event for <owner>/<repo> in the stream's retention window`.
- Read `skill://envoy/references/sources.md` before you subscribe to Slack, Ghost Wispr or WhatsApp, or test their routing.

## Wait for CI or a merge

- Subscribe to `github.<owner>.<repo>.pr.<n>.>` and end the turn. Never start a `gh` poller: the event wakes you.
- Compare each `pr.<n>.checks` event's `sha` with the head you wait on: one such event arrives per commit whose checks settle, the head or not.
- Treat a commit as settled once it has had no check event for 5 seconds (`ENVOY_CI_DEBOUNCE`) and every check run and check suite it reported has finished. A commit whose message carries a GitHub skip keyword (`[skip ci]`) starts no workflow, so it may never settle.
- A green settlement does not mean GitHub will merge: Envoy does not read GitHub's required checks.
- Treat a `pr.<n>` event with `closed` and `merged: true` as the merge. It carries `merge_commit_sha`.
- Read `skill://envoy/references/check-settlement.md` before you write or review code that orders settlements of one commit.

## Roles and team topics

- Publish to a role with `envoy_publish(topic="notifications.role.<role>", ...)`.
- Never subscribe to a role topic: the listener forwards a role message to its holder only.

| When | What happens |
| --- | --- |
| No live session holds the role | Envoy keeps the message for one hour (`kept_until`). It hands the message to the next session that holds the role live |
| A kept answer or a publish error | Neither releases the claim: an `unresponsive` holder keeps its role |

- Read a role's holder with `envoy_role_get(role=...)`.
- Read a role's holder in bash from a shell with no Envoy tools, such as one in a Legion pod (the `<(...)` keeps the token out of `curl`'s arguments):

  ```sh
  curl -sS -H @<(printf 'Authorization: Bearer %s' "$(< "$ENVOY_TOKEN_FILE")") "$ENVOY_URL/v1/roles/<role>" | jq -c '{holder, holder_state, reason, error}'
  ```

- Read `skill://envoy/references/roles.md` when a role you held is gone after a resume, fork or restart, or when `envoy_role_set` is refused.
- Read `skill://envoy/references/legion.md` before you claim, release or publish to a role in a Legion pane, or when a Legion role message fails: in a Legion pane the plugin claims your role for you.

| Who | What |
| --- | --- |
| A team | Subscribes to `notifications.team.<name>.>` |
| Any member | Publishes to `notifications.team.<name>.all` |
| A session that subscribes later | Gets nothing published before |
