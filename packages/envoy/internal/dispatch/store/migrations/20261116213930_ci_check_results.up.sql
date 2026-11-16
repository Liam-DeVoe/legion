-- 20261116213930_ci_check_results.up.sql
--
-- One row per result a CI job recorded against a check its ci_check names
-- (POST /api/v1/issues/{key}/results under a results token the secrets broker minted for the
-- job's run). The row holds what Dispatch read from the token, from GitHub and from the check,
-- never what the caller said of itself:
--   issue_key          the issue the result was recorded on, the issue whose branch the job pushed.
--   check_issue_key    the issue whose Verification document holds the check, and the issue the
--                      result.recorded event is appended on.
--   check_artifact_id  that Verification document.
--   check_block_id     the check block's id within it.
--   check_version      document_blocks.changed_version of that block when the result was
--                      recorded: the document version whose words the result proves.
--   check_sha256       document_blocks.text_sha256 of that block at the same moment, which a read
--                      compares with the block's digest now.
--   run                the name of the run's check run, which is the check's ci_check.
--   surface, negative_control
--                      the check's own, as the block held them.
--   observed           what the job observed, kept by the rule of the project's results route.
--   commit_sha         the 40-hex commit of the run's token, which the job tested.
--   branch             the ref of the run without refs/heads/: the branch whose push it ran on.
--   verdict            'pass' or 'fail'.
--   attestation        the in-toto statement of the run, which the read answers whole.
--   session, actor     the session of the token, its workflow's own, and the actor naming it and
--                      no person.
--   event_seq          the seq of the result.recorded event on the check's issue.
-- Nothing updates or deletes a row: a result is a record of one run, and another run or another
-- attempt is another row. One writer inserts them (insertCIJobCheckResult), and no route
-- retracts one. A row goes only with its issue or its document, by the cascade check_results
-- takes too.
--
-- A table of its own rather than more rows of check_results, which holds what a role recorded:
-- the release before this one reads every check_results row against a check block as a reason
-- that the check is no check CI proves, and its settlement then deletes the block's ci_check from
-- the live document. It reads no row of this table, so no CI job's result changes a check block
-- under any build.
--
-- A new table: nothing existing is read, locked or rewritten, and the release before this one
-- neither reads nor writes it, so it rolls beside this one (cmd/dispatch/README.md, "Database
-- migrations"). The census answers 0, and rolling back past it is an image rollback alone: the
-- release before reads none of its rows, so a result a job recorded changes nothing it serves.
create table ci_check_results (
    id uuid primary key default gen_random_uuid(),
    issue_key text not null references issues (key) on delete cascade,
    check_issue_key text not null references issues (key) on delete cascade,
    check_artifact_id uuid not null references artifacts (id) on delete cascade,
    check_block_id text not null,
    check_version integer not null,
    check_sha256 text not null,
    run text not null check (run <> ''),
    surface text not null default '',
    negative_control text not null default '',
    observed text not null default '',
    commit_sha text not null check (commit_sha ~ '^[0-9a-f]{40}$'),
    branch text not null check (branch <> ''),
    verdict text not null check (verdict in ('pass', 'fail')),
    attestation jsonb not null,
    session text not null,
    actor jsonb not null,
    created_at timestamptz not null default now(),
    event_seq bigint
);

-- The results read: every result of one Verification document's checks, newest first.
create index ci_check_results_check on ci_check_results (check_artifact_id, check_block_id, created_at desc);
-- Every result an issue recorded, which the delete of an issue cascades through.
create index ci_check_results_issue on ci_check_results (issue_key);
