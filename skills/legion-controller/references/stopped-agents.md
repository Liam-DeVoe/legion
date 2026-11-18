# Questions and decisions for a stopped agent

Read this on an `agent-question` or an `agent-decision` wake, and for each entry of `read_state`'s
`questionsForController`. Both wakes exist only where the deployment configures the daemon's
answer loop (`settings.answers` in `read_state`): the daemon then answers a question to an agent
that is not running, in the agent's name, from the agent's own conversation.

## A question the answer did not settle

The `agent-question` wake (`{kind: "agent-question", role, reason, question}`) is a question that
answer did not settle, sent once per question:
`question` is `{id, issue, session, asker, ref, text}`, the agent is the `role` of `question.issue`
in `question.session`, and the asker already has a reply in the agent's name saying the question
was passed to you. `reason` says why:

- `not_answered` (the agent could not answer from its conversation and what `gh` reads) or
  `needs_work` (the question asks for work it cannot do while stopped): the agent's own reply;
- `answer_seconds`, `model_calls`, `length`, `refusal`, `malformed` or `provider_error`: the
  answer failed;
- `over_limit` (the project's daily spend), `no_provider`, `no_price`, `unmetered` (no model call
  of the answer was metered, so it could not be priced), `unknown_format`, `no_conversation` or
  `post_refused`: the deployment could not answer it or post the reply.

`question.asker` is `user:<login>`, or `session:<id>` with the asking session's title when known.
`question.ref` is where it was asked: `dispatch://<KEY>/message/<id>`, `dispatch://<KEY>/comment/<id>`,
`dispatch://<KEY>/ask/<id>` (a person's answer to the agent's ask that also asks something),
`dispatch message <id>` (the agent's own conversation page), or `envoy:<event id>` (an agent's
Envoy message). `question.text` is the question, cut at 2,000 characters with a note naming where
the whole is.

Read `read_state` and the issue (`dispatch read`), then take the first of these that fits:

- **An architect of the issue's tree runs:** the first issue on the chain from `question.issue`
  up through each parent to its tree's root (`issues.<KEY>.tree`) whose
  `issues.<ISSUE>.architect.locator.claim` is in `read_state`. `envoy_publish` the question, its
  asker and its `ref` to `notifications.role.` followed by that claim, because that architect owns
  the work.
- **`read_state` or Dispatch holds the answer:** answer the asker where it asked: `dispatch comment
  --issue <KEY> --reply-to <comment id>` for a comment, `dispatch comment --issue <KEY>
  --reply-to-ask <ask id>` for an ask's answer, `dispatch message --issue <KEY> --in-reply-to
  <ref>` for an issue's message, `dispatch message --issue <KEY>` naming the asker for a message on
  the agent's own page, and `envoy_send` to the asking session for an `envoy:` ref.
- **It asks for work on an issue Legion does not run now:** tell the asker the same way that the
  work needs the issue running again, which a person decides (`todo` with the `legion` label), and
  re-admit nothing for it.

A reason the deployment causes (`over_limit`, `no_provider`, `no_price`, `unmetered`,
`unknown_format`, `no_conversation`, `post_refused`) also goes in the next daily report, because
the operator's `answers` configuration fixes it. Act on a question once: one you already
answered, by your reply on its `ref` thread or your message to the asker, needs nothing, whether
it reaches you as this wake or in `questionsForController`. An entry of `questionsForController`
carries no `text`: read the question at its `dispatch://` ref, and for an `envoy:` ref ask the
asking session to send it again.

## A decision on a stopped agent's ask

The `agent-decision` wake (`{kind: "agent-decision", role, agent_decision}`) is a person's
decision on an ask that an agent that is not running opened: their answer to the ask, or their
reply that leaves the ask on the agent's turn. It goes once to the architect that owns the agent's
issue while that architect holds its role, and to you otherwise. `agent_decision` is `{id, issue, session, asker, ref, text}`: the
agent is the `role` of `agent_decision.issue` in `agent_decision.session`, `asker` is the person
(`user:<login>`), `ref` is their answer (`dispatch://<KEY>/ask/<id>`) or their reply
(`dispatch://<KEY>/comment/<id>`), and `text` is the options they selected and what they wrote,
cut at 2,000 characters with a note naming where the whole is. The daemon already answered any
question the same message asks, in the agent's name; one that answer did not settle reaches you as
an `agent-question` wake of its own.

A decision on an ask of a tree whose root has left the workflow never reaches you here: that one
is the parked-decision relay's, and it reaches you as a `parked-decision` wake naming the tree's
root, with the decision kept for that root's next start. So every decision reaches you once, and
an issue of a parked tree is not a case of this wake.

Read `read_state` and the `ref`, then take the first of these that fits:

- **An architect of the issue's tree runs:** the first issue on the chain from
  `agent_decision.issue` up through each parent to its tree's root whose
  `issues.<ISSUE>.architect.locator.claim` is in `read_state`. `envoy_publish` the decision, its
  `asker` and its `ref` to `notifications.role.` followed by that claim, because that architect
  acts on it.
- **No architect of the tree runs:** each is suspended, stopped, failed or waiting for a slot,
  and nothing inside the tree will act on the decision. A decision does not by itself re-admit a
  tree, because only a request or a plan ruling that names the issue moves one out of `backlog` or
  `icebox` (`skill://legion-controller`, "Free slots"). Re-admit nothing for it. When the decision
  needs the tree to run, tell the person on its `ref` thread (`dispatch comment --issue <KEY>
  --reply-to <comment id>`, or `--reply-to-ask <ask id>` for an answer) that it does, which they
  decide (`todo` with the `legion` label), and name the decision in your summary.
