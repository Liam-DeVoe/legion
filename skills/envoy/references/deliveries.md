# Envoy deliveries

Read this when a delivery field is unclear, or when a message did not reach a session you expected.

## Fields of a rendered delivery

| Field | Meaning |
| --- | --- |
| `id` | Pass it as `in_reply_to` when you reply |
| `from` | The sender's self-asserted id, with its session title in parentheses when it sent one. It is not an authenticated identity |
| `at` | When the server that accepted the delivery took it, by that server's clock: the Envoy listener for a session's message or a webhook, Dispatch's server for a Dispatch event. A delivery read late carries an old `at` |
| `delivered` | When this session took the delivery in, by this machine's clock. The Oh My Pi host adds it, and `waited` and `clocks`, in each model request |
| `waited` | Present when `delivered` is 15 minutes or more after `at`, less any difference `clocks` states: how long the delivery waited. An old `at` on a late delivery says it waited, not that a clock is wrong |
| `clocks` | Present when this machine's clock runs 15 minutes or more ahead of or behind the Envoy listener's: which way, and by how much |
| `urgency` | `low`, `med`, `high` or `blocking`, for you to read. It changes nothing about delivery |
| `expects_reply` | `false` (do not reply or acknowledge), `optional` (welcome, nothing waits on it) or `required` (the sender waits on your answer) |
| `re` | The delivery this one answers. On a Dispatch frame it names the ask or the message. `dispatch.question` holds the start of an ask's text. `dispatch read` gives the whole |
| `supersedes` | An earlier delivery this one replaces. Act on this one instead |
| `reply_with` | The call that answers the sender. On a Dispatch frame it is a `command:` line with `...` where your text goes. Absent under `expects_reply: false` |
| `reply_role` | The role-publish reply, when the sender holds a role. Absent under `expects_reply: false` |
| `note` | The payload names another session. Never send to that id |
| `unrecognised` | The payload failed validation or came from an unknown source. It names what failed |

## Who receives a message

- A session reads every message the stream keeps for it, its inbox and each topic it follows with `envoy_subscribe`, once, from its own durable consumer, whoever sent it.
- Your own publish to a topic you follow reaches you.
- A subagent's publish reaches its parent when the parent follows that topic.
- A message published while a session was away reaches it when it is back, within 72 hours.
- A role message goes to the role's live holder alone, kept on the holder's inbox. The listener drops it when that holder is the message's source session.
- No send or publish answer says that a session read the message.
- A listener `/v1` API error is JSON. When a field is at fault, `expected` names it.
