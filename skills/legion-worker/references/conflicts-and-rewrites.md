# Conflicts, retargets, and a wrong pushed commit

Read it when GitHub reports the pull request `CONFLICTING`, the daemon's merge-forward message asks you to merge the base forward or to stop writing the workspace, the controller asks you to resolve a conflict, the change needs something only the base has, the pull request is retargeted, you read a merge of the base, or a commit you pushed is wrong.

## Reintegrating the base

The implementer merges the base into the issue branch only when GitHub reports it `CONFLICTING`, the daemon's merge-forward message asks it, the controller asks because of a conflict, the architect's backward move names a parent's last round, the change needs something only the base has, or the pull request was retargeted. Never to refresh CI or pick up the default branch: each merge is a new head that reruns CI and can void a review. Never with `jj rebase`, which rewrites pushed commits.

Before you race another tree's pull request on shared files, `envoy_publish` to its implementer's role topic, ask when its next push lands, and merge once, after it.

1. Merge the bookmark, never `@`, with the base. `base` is the pull request's base branch, JSON-quoted so jj reads it as a string literal:

   ```bash
   base=$(gh pr view <n> -R <owner>/<repo> --json baseRefName -q '.baseRefName | @json') && \
     legion merge -R "$LEGION_WORKSPACE" -m "<message>" legion/<KEY> "${base}@origin"
   ```

   Write `<message>` in the repository's own commit convention (its commit-lint configuration, else its recent commits on the base), such as `chore(<scope>): merge <base> into <branch>`, or `Merge <base> into <branch>` where none shows. `@` after a split is an empty commit with no description, which `jj git push` refuses.
2. Resolve as `legion merge` prints and as `legion merge --help` says. Every merge of two commits is `legion merge`, also where a role prompt names `jj new`. When your `legion` has no `merge` command, make no merge and tell the architect.
3. A merge brings instances from the base that the branch's own diff never shows, so before you push the merge search its head for the class of every finding of every earlier review round.
4. Push the merge as any code push, build the branch, and record the issue's checks again at the merge head, each check with no `ci_check` (a check carrying one is read again from its CI run), even with no text conflict.
5. Post one PR comment with the Legion footer: `forward merge <old-tip-sha> → <new-tip-sha>; <the condition it meets>`, with each class search beside it. In a parent's last round, end it with `parent's last round: full test round`.

After a retarget, do the same merge onto the new base, and cite the new head's run in the PR body. Recover a jj mistake forward (`jj new`, `jj restore <paths>`), never with `jj undo`, `jj abandon` or `jj op restore`, which rewrite the shared operation log.

## Reading a merge of the base

Every role reads a merge of the base the same way:

- `legion merge --help` says how to read its resolution and the files the base brought in.
- A check those files or the resolution can reach runs again, and a check the merge was made for has no pass to carry.

## The daemon's ask

A head that conflicts with its base keeps the issue in its phase. The daemon reads GitHub twice, at least a minute apart, and acts only on a conflict both reads report, and only when the next step needs a mergeable head: the issue stands in `accepting`, `retro` or `merging`; a check the base requires has no green result on the head; the base requires a pull request to be current with it; or the daemon could not read the base's rules.

- The implementer gets a merge-forward message on its role topic. Merge the base forward in `$LEGION_WORKSPACE`, the one workspace every role of the issue shares, so another role's work can sit in it: before `legion merge`, run the two path lists of step 2 of `skill://legion-worker` again, even when this turn already ran a `jj new`, and read `skill://legion-worker/references/foreign-path.md` for any path you did not write. Then merge as *Reintegrating the base* says and push the issue branch, with no handoff and no completion for the merge. In `implementing` and `retro` the phase is the implementer's, and its assignment and its completion stand.
- In `testing`, `reviewing` and `merging`, the phase's worker is told to stop writing the workspace: stop at once, complete nothing at the old head (the merger merges and enqueues nothing), and keep your turn and its jobs. When the push lands you are told the new head: read the merge as above and go on at that head.

## A wrong pushed commit

Never rewrite a pushed commit: no `jj squash --into` it, no new description, no backward bookmark move, no force push. A rewrite moves every commit built on it and leaves reviews, CI runs and links naming a commit GitHub no longer holds. Answer a wrong commit with a follow-up commit on top, and a wrong message with a line in the PR body. A base frozen for others to stack on takes its fixes the same way; the body's `Chain` line records it.
