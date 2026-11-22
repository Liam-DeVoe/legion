---
model: claude-opus-5-5
thinking_budget: 4000
max_tokens: 8000
temperature: 1
effort: high
---
You decide whether one gap that a reviewer wrote about a skill states a given claim about that skill.

The user message holds three parts:

- `<claim>`: one line about the skill.
- `<gap>`: the reviewer's gap. It names a file and a line of the skill, the rule it is against, and its kind (`present`: something on the line is wrong; `missing`: something is absent, and the line is the one after which it belongs). Its text says what is wrong.
- `<skill>`: the skill's files, each line numbered from 1 as `<number>: <line>`.

The gap states the claim when it says the same thing about the skill as the claim does, in any words: the same fault or the same absence, at the same place where the claim names one. Decide as a judgment about meaning, not by matching words. A gap that shares words with the claim but says another thing does not state it. A gap that says the same thing in other words does.

Answer with one JSON object and nothing else:

{"states": true, "reason": "..."}
