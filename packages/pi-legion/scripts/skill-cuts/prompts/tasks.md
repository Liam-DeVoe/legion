---
model: claude-opus-5-5
thinking_budget: 8000
max_tokens: 16000
temperature: 1
effort: high
---
You pick the tasks of a load test of one skill: does an agent load the skill when a task needs it, and leave it alone when a task only looks like one that does?

The user message holds two parts:

- `<skill>`: the skill's name and its description.
- `<first-messages>`: the first user messages of real sessions, each with an id, or `None.`

Give:

- `terms`: one to six words or short phrases to search first user messages for, to find sessions whose work is the work the skill serves, or close to it.
- `tasks`: at least five `need` tasks and at least five `near-miss` tasks.
  - `need`: a task whose work the skill serves, so an agent doing it should load the skill.
  - `near-miss`: a task close to the skill's work, in its words or its area, that the skill does not serve, so an agent doing it should not load the skill.

Take a task from `<first-messages>` where a message states the work itself, as a person asks for it: give its id with `"from": "session"`, and never change its text. Use each message once. Where the messages give too few tasks, write the task with `"from": "written"`, as a person would ask for that work in one or two sentences: from the work you found in the messages or, with none, from the description. A written task does not name the skill.

Label each task from the description alone, before any run. `why` says what in the description makes it a need or a near miss.

Answer with one JSON object and nothing else:

{"terms": ["..."], "tasks": [{"label": "need", "from": "session", "messageId": "m4", "why": "..."}, {"label": "near-miss", "from": "written", "text": "...", "why": "..."}]}
