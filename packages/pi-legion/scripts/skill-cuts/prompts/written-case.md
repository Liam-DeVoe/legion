---
model: claude-opus-5-5
thinking_budget: 8000
max_tokens: 32000
temperature: 1
effort: high
---
You write tasks for a behaviour test of a coding agent's skill. In each task, one line of the skill decides the agent's first step after it reads the skill.

The user message holds four parts:

- `<skill>`: the skill's files, each line as `<number>: <line>`.
- `<session-cases>`: the cases already taken from real sessions that read the skill: each case's real step and its criterion. It says `None.` when there are none.
- `<first-message>`: the first user message of a real session that read the skill. Your tasks go in that session's context: the same project, working directory, tools and other skills.
- `<count>`: the most tasks you may write.

The test gives a fresh agent your task as the user's message, then the agent's read of the skill and its result, and asks for the next assistant message under several texts of the skill (this text, a shorter rewrite of it, this text less the line you name, this text again, and no skill at all). A judge decides whether that step passes your criterion. With no skill, the read and its result are taken out, and the agent has only your task.

Write each task:

- The task is a user's message that asks for real work. It reads like the real work of the session `<first-message>` comes from: the same project and the same kind of request, in the words a user of that session would use. It does not read like a test, and it does not name the skill.
- The skill applies to the task: an agent given the task would read this skill for it.
- One line of the skill decides the agent's first step after the read: the line says what to do, or what not to do, at that step. Pick a line where an agent that had not read it could well act otherwise. A step that any agent would take without the skill (reading a file the user named, answering a greeting) is not decided by the skill.
- The task must not state, restate or hint at what the line says. A task that asks for what the line says tests nothing, since an agent with no skill would do it too.
- Where the skill gives them, the tasks of your answer name different lines, and lines that no session case already tests (each session case's step and criterion show which line it tests).

Name the line:

- Copy one line of `<skill>` whole and character for character, without its number and without the `: ` after the number. When the rule runs over more lines, copy the line that holds its main instruction.
- `file` is the file's path as its `<file path="...">` gives it, and `number` is the line's number there.

Write the criterion:

- `pass`: what the agent's first step after the read must do to follow the line. `fail`: what a step that does not follow the line does.
- The judge sees only the criterion, the line, your task, the read call with a note in place of the skill's text, and the step itself (its text and its tool calls, not its thinking). The criterion must let the judge decide from those alone.
- A reply that only says "I will continue." must fail.
- Quote no line of the skill but the one you named: the criterion is the same under every text of the skill.

Write the following step:

- The next assistant message of an agent that follows the line, sent right after the read: it passes the criterion.
- Give its text (an empty string when it holds only tool calls) and its tool calls, each with the tool's name and its arguments as a JSON object.

Write the violating step:

- A next assistant message the agent could plausibly send instead, which breaks the line: it meets the criterion's fail statement and not its pass statement.
- In every other way it is like the following step: the same goal, the same kind of tool calls with arguments of the same kind, about the same length and tone. It is no refusal, no error and no act unrelated to the work, and it does not say that it breaks the line or mention the skill's rule.
- Give it in the same form as the following step.

Answer with one JSON object and nothing else:

{"tasks": [{"task": "<the user's message>", "line": {"file": "SKILL.md", "number": 12, "text": "<the line, copied>"}, "criterion": {"pass": "...", "fail": "..."}, "followingStep": {"text": "...", "toolCalls": [{"name": "bash", "arguments": {"command": "..."}}]}, "violatingStep": {"text": "...", "toolCalls": [{"name": "bash", "arguments": {"command": "..."}}]}}]}

with at least one task and at most the number `<count>` gives.
