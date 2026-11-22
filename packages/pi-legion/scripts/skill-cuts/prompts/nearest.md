---
model: claude-opus-5-5
thinking_budget: 8000
max_tokens: 16000
temperature: 1
effort: high
---
You read one skill and a list of candidate skills from other sources, and name the one or two candidates nearest to the skill's job.

The user message holds two parts:

- `<skill>`: the skill's name, its whole description and its SKILL.md body.
- `<candidates>`: each candidate's name, the source it comes from and its whole description. An agent loads a skill when its task matches the description.

A candidate's verdict:

- `equivalent`: the candidate does the skill's job, for the same trigger.
- `partial`: the candidate does a material part of the skill's job.

A candidate that does no material part of the job is not named.

Name at most two candidates, the nearest first. For each, give:

- `name` and `source`: the candidate's, as the list gives them. Two candidates of different sources can have the same name: the source tells them apart.
- `verdict`: `equivalent` or `partial`.
- `missing`: what the candidate lacks of the skill's job, or `none`.
- `reason`: why the candidate does that much of the job, from the two texts.

Then give the answer's `verdict`: `none` when no candidate does a material part of the job (and then name none), else the best verdict of the candidates you name. Give its `reason` in one or two sentences.

Judge by the job each text gives, not by shared words or a shared topic. Two skills on one topic that serve different tasks (one writes a spec, the other reviews it) do not do each other's job.

Answer with one JSON object and nothing else:

{"verdict": "partial", "nearest": [{"name": "...", "source": "...", "verdict": "partial", "missing": "...", "reason": "..."}], "reason": "..."}
