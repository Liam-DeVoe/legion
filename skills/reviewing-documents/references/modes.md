# Modes and the question tool

Settle the mode first: it decides where each finding that is not applied goes.

## Detect the mode

Arguments may hold a document path, a mode token, or both; both together is no conflict. A token that starts `mode:` is a flag, not a path: strip it, and take what remains as the document.

| What you see | Mode |
| --- | --- |
| `mode:non-interactive`, or its old alias `mode:headless`, in the arguments | Non-interactive |
| You run as a subagent, or an agent called you and wants the findings back | Non-interactive |
| No person reads this session's turns: a Legion role, a scheduled or batch run | Non-interactive |
| A person reads this session and made the request | Interactive |

- **Non-interactive contract:** `mode:non-interactive <document-path>`. Document text pasted into the request counts as the document (`references/document-intake.md`).
- **No document at all, non-interactive:** return `Review failed: non-interactive mode requires a document path. Expected arguments: mode:non-interactive <path>` and dispatch no lens.

## What the mode changes

The mode changes where findings go, never how a finding is judged or routed. Use the same judgment of each finding's route in both modes.

| Route | Interactive | Non-interactive |
| --- | --- | --- |
| Apply (authorized corrections) | Applied and listed | Applied and listed |
| Grouped confirmation (proposed fixes) | One confirmation question | Returned unapplied, awaiting the caller's confirmation |
| Decisions | Routing question, then the walk-through | Returned with their routes intact |
| FYI, residual concerns, deferred questions | In the presentation | In the result |
| The end | Completion report, then `Review complete` | The structured result, ending `Review complete` |

- **Non-interactive asks nothing:** no question tool, no routing menu, no terminal question.
- **The caller owns what you return.** It confirms the proposed fixes and takes the decisions to the author. Where no person reads its session either, `skill://dispatch-first` names the route to one.

## The question tool (interactive only)

- **Use the host's blocking question tool:** `ask` in Oh My Pi, `AskUserQuestion` in Claude Code. A tool in your current tool list exists: never call a question tool to find out whether it does.
- **Load it at the start of the interactive flow** when the host lists it but has not loaded it, with the host's tool-discovery primitive, once. Search for no other host's tool name.
- **Fall back to a numbered list** in the chat, and wait for the reply, only when no such tool is listed, a real call to it errors, or the runtime exposes none.
- **A question that needs the author's decision calls the tool or falls back loudly.** Writing it as narrative text, because the tool feels inconvenient or the turn is in report-writing form, is a bug.
- **Every question depends on it:** the grouped confirmation, the routing question, each walk-through question and the bulk-preview Proceed or Cancel.
