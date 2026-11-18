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
