---
name: legion-oracle
description: Use when you are about to ask a human a question about a pattern, convention, solved problem or technical approach that the repository or its docs/solutions/ might already answer.
---

# Legion Oracle

Which questions to research, which to put to a person, and what your answer says about what you
searched: `skill://dispatch-first`, "Research before you ask". These are the research steps in a
Legion repository.

**Done when:** you have reported as `skill://dispatch-first` says, naming `docs/solutions/` and the codebase queries you ran.

## Research steps

- **Use only `read`, `grep`, `web_search`, `envoy_publish`, and `task` with `scout` or `oracle`:** the daemon's boot gate refuses to start when a prompt names an agent that Oh My Pi cannot find.
- **`wait` for each `task` result:** each `task` runs as a job of its own. Answer from what came back.

1. **Research role first.** If your deployment instructions name a role that answers research questions, send it the question first with `envoy_publish(topic="notifications.role.<name>", expects_reply="required")`. Then end your turn: the reply starts your next one. When the publish says Envoy only kept the question because no session holds the role, put the publish's answer in your output. Then research at once.
2. **Run steps 1 and 2 of the table below**, in parallel if you like.
3. **Check each step 1 hit.** Before you rely on a step 1 hit, `grep` `docs/solutions/` for its path. If another file lists that path under its front-matter `supersedes:` key, use that file instead. If one has a line `Extends docs/solutions/<hit path>`, read both.
4. **Run steps 3 and 4 of the table only where steps 1 and 2 give no `file:line` source for some part of the question.** Run step 3 when the question names a library. Run step 4 when it asks for general practice.

| Step | Tool | Query |
|------|------|-------|
| 1. Institutional learnings | `grep`, then `read`, over `docs/solutions/` (front-matter `tags` first, then the body) | each name the question gives (module, file, function, error text), then two or three words for the problem |
| 2. Codebase patterns | `task(agent="scout")` for a read-only search; `task(agent="oracle")` when scout's answer spans two or more modules or shows patterns that disagree | how [module] handles [topic] |
| 3. Framework docs | `web_search`, then `read` the library's official docs page | [library] docs [topic] |
| 4. External practices | `web_search`, then `read` what the tool's or standard's maintainer publishes; give its URL | current practice for [topic] |
