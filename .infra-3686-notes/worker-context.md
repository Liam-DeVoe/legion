# Shared context for every coding worker of INFRA-3686

You write code for one task of a large change. Another agent (the implementer) owns the commits,
the pushes, the pull request and every GitHub and Dispatch write. Read this whole file first.

## Where you work

- The workspace is `/legion/workspaces/cybermonklabs/legion/infra-3686` (a jj repository; repo
  cybermonklabs/legion). Begin every shell command with
  `cd -- /legion/workspaces/cybermonklabs/legion/infra-3686 &&`. Use absolute paths under it with
  the file tools.
- Make NO commit, NO push, NO `jj` command that writes (`jj new`, `jj describe`, `jj split`,
  `jj commit`, `jj squash`, `jj abandon`, `jj restore` of another's file, `jj undo`, `jj op restore`),
  and NO GitHub or Dispatch write. Read-only `jj diff`, `jj status`, `jj log` are fine. Never run
  `git` mutations. The implementer commits your work after it checks it.
- Do not create another workspace or clone. Do not ask for `isolated` subagents.
- The repository's rules are in `AGENTS.md` at the workspace root and in `packages/*/AGENTS.md`.
  Follow them: Biome style (double quotes, semicolons, ES5 trailing commas, 100 columns), gofmt,
  `node:` imports, `type` imports, semantic line breaks in guides (`bun scripts/semantic-line-breaks.ts <file>`).

## The documents of the issue (read the parts your task names)

- The plan, version 4 (approved): `/legion/scratch/tmp/infra-3686/plan-v4.md`. Your task is one
  `plan_task` block of it (T1 … T19). The numbered "What the plan decides" decisions 1-14 bind every task.
- The approved spec: `/legion/scratch/tmp/infra-3686/spec.md` (requirements R1-R8 and the Design).
- The checks: `/legion/scratch/tmp/infra-3686/verification.md` (each `:::check` names its tests,
  their exact names, and what each test must find; the names are fixed: use them exactly).
- The decisions: `/legion/scratch/tmp/infra-3686/decisions.md`.
- The audit: `/legion/scratch/tmp/infra-3686/audit.md`.
- If a file is missing, fetch it again: `dispatch doc-read --issue INFRA-3686 --artifact plan-md --version 4`
  (and `--artifact spec`, `verification-md`, `decisions-md`, `notification-audit-md`), redirecting to the path above.
- Progress notes of the earlier tasks, and the API of the `texts` package once T1 is done:
  `local://progress.md` (read it if it exists).

## Rules for the code

- The base is `main` at b4a566cb. The plan's code facts were read there.
- Delete, never re-pin: a test that pins one of today's sentences is changed to assert the facts
  (fields, values) its builder writes, never to pin the new text word for word. A test that only
  restates wording is deleted. New tests are the ones the plan and the checks name, with exactly
  those names.
- No stub, no placeholder, no TODO, no "follow-up". Finish what your task names, end to end.
- Keep each change inside the files your task names, plus the tests those files' changes break.
  If you must touch a file outside that list, say so in your report with the reason.
- Prose a person or an agent reads (prompts, skills, docs, tool descriptions) is written in
  ASD-STE100 style (`skill://asd-ste100`): short sentences, one instruction per sentence, no
  number, date, time or project of one deployment.
- The repository is public: no private host name, no company or private repository name.

## The pod: what you may run

The pod has 2 CPUs and 8 GiB shared by six agents. A command that fills it kills everyone's work.

- NEVER run a whole module's test suite (`go test ./...`) or a whole Postgres-backed package.
  Compile and test only the packages your task touches, and in a package with slow or Postgres
  tests run only named tests (`go test -run '<names>'`). Use `go test -p 1` when you name several
  packages. `go vet ./internal/<pkg>/...` of the packages you touched is fine.
- Postgres for tests: `LEGION_TEST_PG_DSN` is set (the pod's own test server). For Dispatch tests
  run `export DISPATCH_TEST_DATABASE_URL="$LEGION_TEST_PG_DSN"`. NEVER use the URL in the file
  `OMP_SESSION_SQL_DSN_FILE`: that is the live sessions database.
- NATS for daemon tests that need it: a server may already run on 127.0.0.1:14222 (monitor 18222);
  check with `nc -z 127.0.0.1 14222`. If none runs, start one in the background:
  `(cd packages/envoy && go run ./internal/testnats/cmd/natsd -p 14222 -m 18222 -store_dir "$(mktemp -d -p "$TMPDIR")") &`
  then `export LEGION_TEST_NATS_URL=nats://127.0.0.1:14222 LEGION_TEST_NATS_MONITOR_URL=http://127.0.0.1:18222`.
  Run Go packages that share that server one at a time (`-p 1`).
- Put large files, caches and scratch under `$TMPDIR` (`/legion/scratch/tmp`), never `/tmp` or home.
  Make scratch with `mktemp -d -p "$TMPDIR"`; delete only paths you made, by exact path.
- Bun: run `bun install` at the workspace root once if `node_modules` is missing. Run a package's
  tests in its own directory (`bun test <file>` or `bun test -t '<name>'`), never at the root.
- Never kill processes broadly; kill only a PID you started.

## Your report

End with: the files you changed; what each change does; the exact commands you ran with their
results (pass counts, failures); every deviation from the plan and why; anything left undone and
why (there should be none). Do not claim a test passes unless you ran it and saw it pass.
