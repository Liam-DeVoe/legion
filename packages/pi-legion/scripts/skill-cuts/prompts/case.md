---
model: claude-opus-5-5
thinking_budget: 8000
max_tokens: 16000
temperature: 1
effort: high
---
You read part of a coding agent's session and propose one case for a test of a skill's text.

The user message holds two parts:

- `<skill>`: each file of the skill, each line numbered from 1 as `<number>: <line>`.
- `<transcript>`: the session from the agent's read of the skill on, each message with its index in the session and its role. Where the agent read a file of the skill, the file's text is left out, since `<skill>` holds it.

The test replays the session up to one step under three texts of the skill (this text, a shorter rewrite of it, and no skill at all), and a judge decides whether the agent's step passes a criterion. Your case names that step, the line of the skill that decides it, and the criterion.

Find the step:

- The step is the first assistant message after the read whose act a line of the skill decides: the line says what to do, or what not to do, in the situation the agent is in at that message.
- Pick a step where an agent that had not read the line could well act otherwise. A step that any agent would take without the skill (reading a file the user named, answering a greeting) is not decided by the skill.
- When no assistant message of the transcript is decided by a line of the skill, propose no case.

Name the line:

- Copy the one line of the skill's file that decides the step, whole and character for character, without its number and without the `: ` after the number. When the rule runs over more lines, copy the line that holds its main instruction.
- `file` is the file's path as its `<file path="...">` gives it.

Write the criterion:

- `pass`: what the step must do, at this message, to follow the line. `fail`: what a step that does not follow the line does here.
- The judge sees only the criterion, the line, the last messages before the step, and the step itself (its text and its tool calls, not its thinking). The criterion must let the judge decide from those alone.
- The real agent's step must pass. A reply that only says "I will continue." must fail.
- Quote no line of the skill but the one you copied: the criterion is the same under every text of the skill.

Answer with one JSON object and nothing else. With a case:

{"step": {"messageIndex": 14, "what": "<the step's act, in a few words>"}, "line": {"file": "SKILL.md", "text": "<the line, copied>"}, "criterion": {"pass": "...", "fail": "..."}}

where `messageIndex` is the step's index as `<transcript>` gives it. With no case:

{"step": null, "reason": "<why no step of the transcript is decided by a line of the skill>"}
