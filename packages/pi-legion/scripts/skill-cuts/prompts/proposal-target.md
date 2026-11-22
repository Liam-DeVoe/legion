---
model: claude-opus-5-5
thinking_budget: 16000
max_tokens: 32000
temperature: 1
effort: high
---
You read one proposal of a closed pull request and find where it goes: the hunks of the pull request's diff that make it, and the file or skill its change is for today.

The user message holds five parts:

- `<diff>`: the whole diff of the closed pull request, as `gh pr diff` gives it.
- `<hunks>`: each file of the diff by its `path` (after the change, or before it for a file the change deletes; a renamed file also gives its path `before`), and each hunk of the file by its index in that file, from 0, with its `@@` line.
- `<repository>`: the repository the pull request changed, at the commit read today.
- `<item>`: the proposal: its id, its state and its text, from a list in the body of a later pull request. The state and the text are that body's words.
- `<skills>`: each skill a later reader reads, by its repository and its name.

Name each hunk of the diff that makes the proposal: `file`, the file's path as `<hunks>` gives it, and `hunk`, the hunk's index in that file. Name every hunk the proposal needs and no other.

Then name each target: where the proposal's change would go today.

- `dotfiles`: a file of the repository, by its `path` in the repository. A file the change would add is a target too, by the path it would have.
- `skill`: a skill of `<skills>`, by its `repo` and `name` as the list gives them. A change to a file inside a listed skill's directory has that skill as its target, not the file.
- `none`: no file of the repository and no listed skill is the proposal's target today, for example when its file was deleted and nothing took its line, or when it moved to a place no listed skill is.

Give each target's `why`: what in the diff, the item or the list makes it the target, or, for `none`, why there is none. Find the target from the diff and the list; the item's text may say where its line went.

Answer with one JSON object and nothing else:

{"hunks": [{"file": "...", "hunk": 0}], "targets": [{"kind": "dotfiles", "path": "...", "why": "..."}, {"kind": "skill", "skill": {"repo": "...", "name": "..."}, "why": "..."}]}
