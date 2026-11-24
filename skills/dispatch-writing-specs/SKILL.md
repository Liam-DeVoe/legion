---
name: dispatch-writing-specs
description: "Use when you write or change a spec, ask, comment or message a person reads, in a session with Dispatch. It replaces superpowers' brainstorming there."
---

# Writing specs

Reach shared understanding at the least cost of the person's time, in a spec another agent can build and check from alone.

**Done when:** you requested approval of the spec and `verification.md`.

## The conversation

A spec is a conversation, not a document you write and then ask about. A Legion phase worker writes none (`skill://legion-worker`).

1. **Read.** Read the issue, the code and what the person wrote.
2. **Ask.** Send what you found and the questions it leaves open, a few at a time, before the spec states a design.
3. **Write.** Write each part only after the answers it rests on.
4. **Request first.** On a wrong premise or a changed request, change the request first.

## The outline

Find the issue's project with `dispatch projects` or `search`, never the environment: it may differ.

The spec's `##` parts, in order | Content
--- | ---
`Summary` | The change in the reader's terms, at most 100 words
`Problem` | What goes wrong, for whom, with counts
`Goals and non-goals` | What is in and out of scope
`Requirements` | a `:::requirement` block each, `**R1.**` on, its source (`From:`)
`Verification` | the issue's `verification.md`, a `:::check` block each: `serves`, `surface`, `run`, `when`, `negative_control`
`Design` | How it meets them. A value and its reason for each limit, size and time
`Decisions` | The choices the spec rests on (Decisions below)

```md
:::requirement{#r1}
**R1.** <rule>
From: <source>
:::

:::check{#c-x serves="dispatch://KEY/spec#b-r1" surface="cli" run="<tests>" when="before_merge" negative_control="<a-break-it-catches>"}
<claim>
:::
```

- `serves` and `needs` name a block of this issue or an ancestor.
- Where `run` names tests, name specific ones.
- For a whole test suite, name its CI job in `ci_check`. A check carrying `ci_check` is CI's and takes no `run`.
- Give a small change a few sentences per part, no matrix.
- Split a large design into children.

## Rules for the text

For each spec, ask, comment and message:

- Write in `skill://asd-ste100`'s strict mode.
- **Write about the current design, never the person or how people reached it.** Never quote or upload their words.
- Link each source where the person wrote it. With no link, state the design.
- Keep only text a reader needs to understand, decide, build or check (`skill://plain-words`).
- Say what each issue you name does for the reader.
- Treat a person's stated preference or past answer as evidence of their taste, never a decision that settles the question.
- Never treat a commit attributed to a person as their decision.
- In a spec, cite code by file and symbol, never a line number.
- In an ask, ask block, comment or message, name a requirement or check by what it is, never its number.

## Questions

- Name the concrete case and its numbers, then the choice in those terms, never "Which option?".
- **Yours to decide:** a choice inside what the person asked for, one their messages favour, routine cleanup, sequencing, naming, a choice between equivalent options, and a fix you can make.
- Ask with an [ask block](skill://dispatch/SKILL.md#asks-and-ask-blocks): 2 to 4 options, short and self-contained: a person answers it with no other reading.
- Measure each claim the ask rests on, its premise and each option's precondition. A claim you cannot measure stays out of the ask.
- Call an option unavailable only where a precondition demonstrably fails.
- No unmeasured number goes in a premise.
- Try the source that answers a question, then the other records holding it, before you pass it on as one only an attended or privileged act answers.
- Keep a privileged read for what only the live system knows.

## Each version

1. **Rewrite.** After a person's answer, comment or edit, reread the problem and rewrite what the answer touches. Delete what it contradicts.
2. **Answer.** Answer each comment in its thread.
3. **Review.** Where a spec is large, or changed in a way you cannot check alone, give the whole spec to a fresh subagent to find gaps or check facts.
4. **Name.** Name each version with `dispatch doc-edit --summary`.

Never narrate an edit.

## Decisions

- A choice earns an entry in the spec's `## Decisions` part only if a disagreeing reader would act differently.
- Otherwise, keep others to one spec line each.
- Write each entry in the spec, not a separate document.
- Put the choice first, then the reason and the options weighed.
- Use a `:::decision` block once the server's block types include `decision`.
- A person's answered ask is their decision: it stays in the spec where it was asked.

## Old specs

Rewrite a spec outside the outline whole in one version. Move its ask blocks with `doc-edit`'s `move`.

## Approval

- Request approval of the spec and `verification.md` (`--include-verification`): each ask block answered or waived, each claim checked, this version unapproved. An unrun check never delays it.
- After approval, version only a change to scope, a check, cost, risk or a person's decision, all at once.
- Put any other fix in the plan or a comment.
- An exact undo can restore approval.

## The plan

The plan is the issue's `plan.md`, never a repository file. Each task is a `:::plan_task` block with `serves` and `needs`.
