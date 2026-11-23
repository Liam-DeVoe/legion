# ci-ok's one reading of whether `changes` selected a job, and of which check runs are a job's, so
# no reading of either disagrees with another.
#
# `selection` is read by ci-ok.sh, the verdict ci.yaml's ci-ok job reaches over every job's result
# and ci-ok-report.sh posts at once on a merge-group run one job's failure has decided. $map is
# SELECTED_BY (job to the `changes` output its `if:` tests), $out is `changes`' outputs, $pr is
# PULL_REQUEST_ONLY and $event is `github.event_name`. A job is selected when its output is 'true'
# and, for a PULL_REQUEST_ONLY job, the run is a pull_request run; `why` says which. ci-ok.sh
# reads this file with `jq -L .github/scripts 'include "ci-ok"; …'`.
#
# `$out.filter_verdicts` is the paths filter's own answer for every filter, which the `changes`
# job publishes beside the outputs
# (.github/ci-notes/ci.md#the-paths-filters-own-verdict-for-every-filter-before-the-full-ci-term
# says why). It is what tells a job
# the `full-ci` label held back from one its paths never selected: the slow suites' outputs
# are written `false` on a pull request without that label whether the filter said yes or no, so
# the output alone reads the same in both cases. Only a pull request can be gated that way - on
# every other event the label term of those expressions is already true, so the output is the
# filter's verdict and this reading never fires. The verdict itself does not move: such a job
# was not selected, and ci-ok accepts its skip exactly as before.
def selection($job; $map; $out; $pr; $event):
  $map[$job] as $output
  | ($out[$output] // "unset") as $value
  | ($pr | any(. == $job)) as $pr_only
  | (($out.filter_verdicts | try (fromjson | objects) catch empty) // {}) as $verdicts
  | if $value != "true" then
      if $event == "pull_request" and $verdicts[$output] == "true" then
        {selected: false, why: "selected by paths, gated by full-ci: runs in the merge queue"}
      else {selected: false, why: "needs.changes.outputs.\($output) is \($value)"}
      end
    elif $pr_only and $event != "pull_request" then
      {selected: false, why: "it runs only on pull_request, and this run is \($event)"}
    else {selected: true,
          why: "needs.changes.outputs.\($output) is true\(if $pr_only then " on a \($event)" else "" end)"}
    end;

# The check runs of one job of ci.yaml among a run's check runs, or among the jobs route's entries
# (each an object with a `name`). $names is CI_OK_CHECK_RUN_JOBS from ci.yaml's env: each leg's and
# carrier's check-run name to its job id, for a leg whose `name:` is an expression, whose check run
# carries the name it evaluates to (an old check-run name such as `envoy-go-checks / envoy-go-checks`)
# and no job id. A name the map holds belongs to the job it gives and to no other; any other name
# belongs to the job GitHub names it after: the job itself (`job`), a job of the workflow it calls
# (`job / inner`, `job / inner (leg)`), or a leg of its matrix with no name of its own (`job (leg)`).
# ci-ok-report.sh reads a job's result this way, and runner-shutdown.sh the check runs of a job
# whose runner may have been shut down.
def runs_of($job; $names):
  map(select(($names[.name] // null) as $mapped
    | if $mapped != null then $mapped == $job
      else .name == $job or (.name | startswith($job + " / ") or startswith($job + " (")) end));

# Whether a job of a merge-group run is one no pull request that changes the group's files would
# have run: the paths filter's own verdict for its output, given the group's changed files, is
# false (`filter_verdicts`, which the changes job publishes on a merge group too), and the
# unnamed-file step found no changed file that matches no filter (`unnamed_files`), which would
# have selected every job. ci-ok.sh names such a job when it fails, so its reader checks the filter
# for a read it misses and the test for a flake. A run whose verdicts are missing, or that names
# no verdict for the output, names nothing.
def missed_by_filters($job; $map; $out; $event):
  (($out.filter_verdicts | try (fromjson | objects) catch empty) // {}) as $verdicts
  | $event == "merge_group" and ($map | has($job)) and ($out.unnamed_files // "") != "true"
    and $verdicts[$map[$job]] == "false";
