GitHub's answers to the requests the rig answers with an error, each as gh 2.98.0 (2026-08-20)
printed it on 2026-11-18 against api.github.com, as the legion-implementer App on
cybermonklabs/legion: <name>.stdout is the body GitHub sent and <name>.stderr gh's line for it,
byte for byte; gh exited 1 for each. The request each answered:
branch-404      gh api repos/cybermonklabs/legion/branches/no-such-branch-infra-1303
commit-422      gh api repos/cybermonklabs/legion/commits/1111111111111111111111111111111111111111
checkruns-422   gh api repos/cybermonklabs/legion/commits/1111...1111/check-runs
suites-422      gh api repos/cybermonklabs/legion/commits/1111...1111/check-suites
noroute-404     gh api repos/cybermonklabs/legion/runs/1   (no such endpoint; CRLF pretty body)
checkruns-list-404     gh api repos/cybermonklabs/legion/check-runs   (exit 1)
joblog-410      gh api repos/cybermonklabs/legion/actions/jobs/90713847067/logs   (job past log retention)
repo-404-pulls  gh api repos/cybermonklabs/no-such-repo-infra-1303/pulls/1
repo-404-checkruns gh api repos/cybermonklabs/no-such-repo-infra-1303/commits/abc/check-runs
repo-404-runs          gh api repos/cybermonklabs/no-such-repo-infra-1303/actions/runs/1   (exit 1)
repo-404-branch        gh api repos/cybermonklabs/no-such-repo-infra-1303/branches/main   (exit 1)
pull-404        gh api repos/cybermonklabs/legion/pulls/999999
reviews-404     gh api repos/cybermonklabs/legion/pulls/999999/reviews
comments-404    gh api repos/cybermonklabs/legion/issues/999999/comments
run-404         gh api repos/cybermonklabs/legion/actions/runs/1
job-404         gh api repos/cybermonklabs/legion/actions/jobs/1
rerun-404       gh api -X POST repos/cybermonklabs/legion/actions/runs/1/rerun-failed-jobs
checkrun-404    gh api repos/cybermonklabs/legion/check-runs/1
gitref-404             gh api repos/cybermonklabs/legion/git/ref/heads/no-such-branch-infra-1303   (exit 1)
gitrefs-404            gh api repos/cybermonklabs/legion/git/refs/heads/no-such-branch-infra-1303   (exit 1)
annotations-404        gh api repos/cybermonklabs/legion/check-runs/1/annotations   (exit 1)
attempt-404            gh api repos/cybermonklabs/legion/actions/runs/44366233978/attempts/99   (exit 1)
attempt-jobs-404       gh api repos/cybermonklabs/legion/actions/runs/44366233978/attempts/99/jobs   (exit 1)
run-jobs-404           gh api repos/cybermonklabs/legion/actions/runs/1/jobs   (exit 1)
run-attempt-404        gh api repos/cybermonklabs/legion/actions/runs/1/attempts/1   (exit 1)
run-attempt-jobs-404   gh api repos/cybermonklabs/legion/actions/runs/1/attempts/1/jobs   (exit 1)
run-logs-404           gh api repos/cybermonklabs/legion/actions/runs/1/logs   (exit 1)
run-rerun-404          gh api -X POST repos/cybermonklabs/legion/actions/runs/1/rerun   (exit 1)
joblog-404             gh api repos/cybermonklabs/legion/actions/jobs/1/logs   (exit 1)
jobrerun-404           gh api -X POST repos/cybermonklabs/legion/actions/jobs/1/rerun   (exit 1)
review-404             gh api repos/cybermonklabs/legion/pulls/4435/reviews/1   (exit 1)
review-comments-404    gh api repos/cybermonklabs/legion/pulls/4435/reviews/1/comments   (exit 1)
status-ref-404         gh api repos/cybermonklabs/legion/commits/no-such-ref-infra-1303/status   (exit 1)
suites-ref-404         gh api repos/cybermonklabs/legion/commits/trunk/check-suites   (exit 1)
statuses-ref-404       gh api repos/cybermonklabs/legion/commits/trunk/statuses   (exit 1)
repo-404-rules             gh api repos/cybermonklabs/no-such-repo-infra-1303/rules/branches/main   (exit 1)
repo-404-protection        gh api repos/cybermonklabs/no-such-repo-infra-1303/branches/main/protection   (exit 1)
repo-404-required-checks   gh api repos/cybermonklabs/no-such-repo-infra-1303/branches/main/protection/required_status_checks   (exit 1)
repo-404-runs-list         gh api repos/cybermonklabs/no-such-repo-infra-1303/actions/runs?head_sha=1111111111111111111111111111111111111111&per_page=100   (exit 1)
repo-404-run-jobs          gh api repos/cybermonklabs/no-such-repo-infra-1303/actions/runs/1/jobs   (exit 1)
repo-404-reviews           gh api repos/cybermonklabs/no-such-repo-infra-1303/pulls/1/reviews   (exit 1)
repo-404-repo              gh api repos/cybermonklabs/no-such-repo-infra-1303   (exit 1)
repo-404-job               gh api repos/cybermonklabs/no-such-repo-infra-1303/actions/jobs/1   (exit 1)
repo-404-pulls-list        gh api repos/cybermonklabs/no-such-repo-infra-1303/pulls   (exit 1)
(repo-404-rules was answered byte for byte the same with --paginate, and repo-404-pulls-list with a ?state=all&head= query.)
repo-404-pull-patch   gh api -X PATCH repos/cybermonklabs/no-such-repo-infra-1303/pulls/1 -f title=x
repo-404-review-post  gh api -X POST repos/cybermonklabs/no-such-repo-infra-1303/pulls/1/reviews -f event=COMMENT -f body=x
GraphQL requests GitHub refuses before it reads anything (gh prints the body and its message, exit 1;
gh-graphql.ts writes these errors for the call's own query, in this shape):
graphql-variable-null     gh api graphql -f query='query($n:Int!){repository(owner:"cybermonklabs",name:"legion"){pullRequest(number:$n){number}}}'   (no n given)
graphql-variable-coerce   the same query with -f n=1003 (a String for Int!)
graphql-literal-type      gh api graphql -f query='query{repository(owner:"cybermonklabs",name:"legion"){pullRequest(number:"1003"){number}}}'
graphql-variable-unused   gh api graphql -F n=1003 -f query='query($n:Int!,$u:String){repository(owner:"cybermonklabs",name:"legion"){pullRequest(number:$n){number}}}'

What GitHub answered to the reads below, measured the same way on 2026-11-18, which the rig's
answers follow (no file: each is stated here and in the comment of the code that follows it):
- Check runs of a commit (05e64382b833c8dfdfbd8584261a2e2f3b7deaa5, whose CI run 44406867953 has
  two attempts): `commits/<sha>/check-runs` with no filter, and with `filter=latest`, 64 check
  runs, one per name (`ci-ok` its attempt-2 success alone); `filter=all` 122 (`ci-ok` its success
  and its attempt-1 failure); `filter=bogus` the same 64 as no filter, exit 0; `check_name=ci-ok`
  1, with `filter=all` 2; `status=completed` 64, `status=in_progress` 0.
- Jobs of that run: `actions/runs/44406867953/jobs` 58, all of attempt 2; with `filter=all` 116.
- A page past the end: `pulls?state=all&per_page=100&page=99999` answers `[]`;
  `actions/runs?...&page=2` past its runs answers `{"total_count":3,"workflow_runs":[]}`.
- `actions/runs?exclude_pull_requests=true`: a pull_request run that lists 1 pull request lists 0.
- A run's `previous_attempt_url`: null on attempt 1, `.../actions/runs/<id>/attempts/1` on attempt 2.
- The GraphQL rollup of 05e64382 (`statusCheckRollup{contexts(first:100, after:$endCursor){...}}`):
  64 contexts, `pageInfo` `{"endCursor":"NjQ","hasNextPage":false}` with no endCursor given;
  `first:10` gives `{"endCursor":"MTA","hasNextPage":true}` (a cursor is base64 of the 1-based
  index of the page's last node, unpadded); `after:"NjQ"` gives no node and
  `{"endCursor":null,"hasNextPage":false}`, totalCount still 64.
- GraphQL: a variable given that the query does not declare (`-f extra=1`) is ignored, the query
  answered; `repository(owner:"CyberMonkLabs",name:"Legion")` answers the repository, as does the
  REST `repos/CyberMonkLabs/Legion`: owner and name match without regard to case;
  `object(expression:"main~1")` answers a commit (a revision expression the rig does not model).
- Measured on 2026-11-19 through the fold's read-only measure-gh.sh (its request log names each):
  - `--paginate` of a REST read: `pulls/4148/comments?per_page=3` (six comments, two pages) printed
    one JSON array of the six, the second page's items joined to the first's with `,`;
    `commits/05e64382.../check-runs?check_name=ci-ok&filter=all&per_page=1` (two pages) printed the
    two objects one after the other with nothing between and no newline after; and a paged read that
    names no per_page, `--paginate pulls/4148/comments`, is sent as `pulls/4148/comments?per_page=100`
    (GH_DEBUG=api).
  - The jobs of each attempt of run 44406867953, whose attempt 2 re-ran its failed jobs:
    attempt 1 lists 58 jobs (2 failure, 41 success, 15 skipped), attempt 2 lists 58 (43 success,
    15 skipped), all at run_attempt 2. The 2 failed jobs were re-run; each of the 56 others is
    carried to attempt 2 with an id and a check run of its own (its check_run_url names its own id),
    its attempt-1 conclusion, steps, runner and started_at and completed_at, and a new created_at.
- Measured by the reviewer on 2026-11-19 (infra-1303-github-fields-measured): a check run, a job and
  a run of GitHub Actions as GitHub answers each, which red_model writes key for key; and that a
  commit's default check-runs read keeps the latest of each name in each check suite
  (`commits/b789479b99.../check-runs?check_name=pr-title` answers five pr-title runs of five suites,
  as `filter=all` does).
  - The pull requests a `workflow_dispatch` run names: each of the 30 latest workflow_dispatch runs
    of cybermonklabs/legion lists one; run 44457284390 (event workflow_dispatch, head 38c89d64...
    on legion/INFRA-3135, which is the current head of the open #3729) lists #3729 in the minimal
    shape, and the check runs of its check suite (120342313542) list #3729 too.
