# `dispatch carry-test`

`dispatch carry-test` decides whether a pass of a check carries from an earlier head of a pull
request to a new head across a merge of the base branch. It applies GitHub's rule for a stale
approval: the pass carries only when no merge between the two heads resolved a conflict, and the
pull request's own changes read the same at both heads.

```sh
git fetch origin main
dispatch carry-test --from <earlier head> --to <new head> --base origin/main
```

Run it in a clone or a worktree of the pull request's repository. A bare mirror works too.

## What it runs

1. In your clone, `git rev-parse --verify <commit>^{commit}` for each of `--from`, `--to` and
   `--base`, and `git rev-parse --path-format=absolute --git-common-dir`.
2. In a new directory under `TMPDIR`, a new bare repository (`git init -q --bare --template=`)
   whose `objects/info/alternates` names your clone's objects.
3. In that repository, `git rev-list --first-parent <to>`, read back to `--from`, and
   `git rev-list --parents -n 1 <commit>` for each commit on that path.
4. The merge test: `git merge-tree --write-tree <first parent> <second parent>` for each merge
   on the path, oldest first. Exit 0 is a clean merge, exit 1 a conflict.
5. The patch test, only when no merge resolved a conflict:
   `git diff --binary -M -U0 <base>...<head>`, then `git patch-id --verbatim` on that diff, at
   `--from` and at `--to`. The diff has no context lines, so a change of the base next to the
   pull request's lines does not change the id.

The command then removes the temporary directory by its exact path, on every exit.

## Output

- One line per merge on the first-parent path from `--to` back to `--from`, oldest first:
  `merge <40 hex>: clean` or `merge <40 hex>: conflict`.
- Only when no merge resolved a conflict: `patch-id at <40 hex>: <id>` (or
  `none (empty diff)`), for `--from` and then for `--to`.
- The verdict, on the last line:

| Last line | Exit | Meaning |
| --- | --- | --- |
| `carries: no merge between <from> and <to> resolved a conflict, and the pull request's diff reads the same at both heads` | 0 | The pass carries. Record it with the `--observed` text that names the earlier head, and add this line. |
| `runs again: merge <sha> resolved a conflict` | 1 | A merge resolved a conflict. The patch test does not run. Run the check again. |
| `runs again: the pull request's diff changed between <from> and <to>` | 1 | A hand edit in a merge, a push of the branch's own commits, a merge of another branch, or a moved gitlink changed the diff. Run the check again. |
| `no merge: no merge commit lies between <from> and <to>, so the push rule decides` | 4 | No merge to test. The push rule of your role prompt decides. |
| `UNKNOWN: <reason>` | 3 | git could not answer. Never read it as a carry. Run the check again. |

Exit 2 is a usage error (a missing or unknown flag), and the command runs nothing then.

**The last line is the verdict, not the exit code alone.** Exit 1 here means "runs again", not
a refusal: this command is a test.

## Why the tests run in an empty environment

A git setting can change both answers. `diff.ignoreSubmodules=all` or `diff.submodule=log` hides
a moved gitlink from the patch test. `diff.noprefix` changes a patch-id. A `merge=union`
attribute hides a conflict from the merge test. Such a setting can come from your environment
(`GIT_CONFIG_COUNT`, `GIT_CONFIG_KEY_<n>`), your home directory's `.gitconfig`, the system's git
configuration, the clone's own config, or its attribute files. A Legion pod sets git values in the
environment.

So each git process in the new bare repository gets only `PATH`, `HOME` (the temporary
directory), `GIT_CONFIG_NOSYSTEM=1`, `GIT_ATTR_NOSYSTEM=1` and `GIT_DIR` (the temporary
directory). The repository has no config of its own and no attribute file. Only the two
`rev-parse` reads of step 1 run in your clone, with your environment.

## Why a partial clone reads UNKNOWN

A clone made with `--filter=blob:none` holds every commit and tree but no file contents until git
fetches them. The new bare repository fetches nothing. When a test needs a file the clone does
not hold, its git process fails, and the command prints `UNKNOWN: git <subcommand> exited <n>:
<its first stderr line>` with exit 3. A clean merge with a hand edit then reads UNKNOWN in the
patch test, and a conflict merge can read UNKNOWN in the merge test. Neither reads `carries`.

Every git process has the exits the command expects (0, or 0 and 1 for `merge-tree`). Any other
exit is UNKNOWN. A commit your clone lacks reads `UNKNOWN: git rev-parse exited <n>: <first
stderr line>; the clone may lack commit <x>`. For an answer, run the command in a full clone, or
fetch the base branch and the two heads first.

These cases also read UNKNOWN:

- `--from` is not on the first-parent path of `--to`, as after a force-push or a rebase.
- A merge on the path has more than two parents (an octopus merge).
- git is older than 2.38, which has no `git merge-tree --write-tree`.

## What it does not do

It reads no Dispatch host, session or `envoy.json`, and sends nothing. It writes nothing to
Dispatch and refuses no write: the role that runs it records the carried result itself. The
`dispatch --help` list does not show it, as it does not show `dispatch login`.
