# Dispatching the lenses

## The subagent

- **Generic only.** A generic subagent's whole brief is the prompt you send it. When the host's tool makes you name an agent, use a name you have seen the host list, and pick the plain one that adds no role of its own (Oh My Pi's `task`). Never pick a specialist, such as a reviewer or worker agent: its role prompt, output format or tool limits replace or narrow your brief.
- **All lenses at once, within the host's limit.** Dispatch the team in one call where the host allows it; in Oh My Pi, one `task` call with one `tasks` item per lens.
- **Omit any permission `mode` parameter**, so the user's own permission settings apply.
- **Leave the model to the host.** Name a model for a lens only when the user's or the project's instructions name one for it.
- **No subagent tool:** run the lenses yourself, one at a time, each from a fresh read of its brief. Say so in Coverage. Inline runs are not independent, so their agreement never promotes a finding.

## The prompt

Each lens needs three files whole: its lens file `references/personas/<lens>.md`, `references/subagent-template.md` and `references/findings-schema.json`.

- **Give their locations, not their text.** In Oh My Pi, give them as `skill://` URLs, for example `skill://reviewing-documents/references/subagent-template.md`, `skill://reviewing-documents/references/findings-schema.json` and `skill://reviewing-documents/references/personas/coherence-reviewer.md`. Elsewhere, give their absolute paths under this skill's directory.
- **Tell each lens** to read all three before reviewing, to follow everything inside the template's Template block, and to take the lens file and the schema as the values of `{persona_file}` and `{schema}`.
- **Paste the files' text only when the host's subagents cannot read files:** a pasted copy costs thousands of tokens per lens before any lens starts.
- **In Oh My Pi, put the shared slots once** in the call's `context`, the document's text among them, and give each item its lens file. Set each item's `outputSchema` to the schema's JSON, so the tool checks the shape of what the lens returns.

Fill every other slot:

| Slot | Value |
| --- | --- |
| `{document_type}` | `requirements`, `spec`, `plan` or `other`, from the classification |
| `{document_path}` | The readable path the missing-document check passed |
| `{origin}` | The upstream document that settled the premise, or `none` |
| `{settled_decisions}` | The decisions already made, each with its rejected alternative, or `none` |
| `{decision_primer}` | Round 1: the block below. Round 2 and later: rendered as `references/decision-primer.md` says |
| `{document_content}` | The whole document. Never split it: a lens that sees one part misses the contradictions across parts |

On round 1, set `{decision_primer}` to:

```
<prior-decisions>
Round 1 — no prior decisions.
</prior-decisions>
```

## A full queue, and a failed lens

- **A concurrency-limit error is backpressure, not failure.** The host is full: keep the rejected lens queued and dispatch it when a slot frees.
- **Mark a lens failed** only when a dispatched lens times out or fails, or a dispatch fails for a reason other than capacity that survives a corrected call.
- **Go on without a failed lens.** Synthesize from the lenses that returned, and name the failed one in Coverage. Never block the review on one lens.
- **Stop as incomplete when capacity cannot recover** and selected lenses remain undispatched: no synthesis, no fixes, no `Review complete`. Report each lens as completed, failed or never run, with the reason, and keep what the completed lenses returned.
- **Collect every outcome before you move on**, failures included.
- **Release the agents this review started** before you return, where the host lets you release agents.
- **Send no message to a finished agent** that has nothing left to do.
