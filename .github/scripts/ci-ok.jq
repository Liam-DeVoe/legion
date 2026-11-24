# ci-ok's one reading of whether `changes` selected a job, and of which check runs are a job's, so
# no reading of either disagrees with another.
#
# `selection` is read by ci-ok.sh, the verdict ci.yaml's ci-ok job reaches over every job's result
# and ci-ok-report.sh posts at once on a merge-group run one job's failure has decided. $map is
# SELECTED_BY, the exceptions: a job to the `changes` output its `if:` tests where that output is
# not named after the job's own id; every other job is selected by the output of its own id. $out
# is `changes`' outputs, $pr is PULL_REQUEST_ONLY and $event is `github.event_name`. A job is
# selected when its output is 'true' and, for a PULL_REQUEST_ONLY job, the run is a pull_request
# run; `why` says which. ci-ok.sh reads this file with `jq -L .github/scripts 'include "ci-ok"; …'`.
#
# `$out.filter_verdicts` is the paths filter's own answer for every filter, which the `changes`
# job publishes beside the outputs
# (.github/ci-notes/ci.md#the-paths-filters-own-verdict-for-every-filter
# says why). It is what tells a job a pull request held back from one its paths never selected:
# a slow suite's output is written `false` on a pull request without the label full-ci or the
# job's own lane:<output> label whether the filter said yes or no, so the output alone reads the
# same in both cases. Only a pull request can hold a job back that way - on every other event the
# label term of those expressions is already true, so the output is the filter's verdict and this
# reading never fires. The verdict itself does not move: such a job was not selected, and ci-ok
# accepts its skip exactly as before. `gated` marks it, so ci-ok.sh can name it apart from the
# jobs that ran: its paths chose it and it did not run at this head.
def output_of($job; $map): $map[$job] // $job;

# The labels that lift each held output's hold on a pull request, read off the workflow's changes
# job (its JSON, as `yq -o=json` gives it): every label a
# `contains(github.event.pull_request.labels.*.name, '<label>')` term of the output's expression
# names, full-ci first as the expressions write it. ci-ok.sh hands the map to selection below, so
# the labels a not-run line names are the terms the changes job tests and no list beside them.
def hold_labels:
  (.jobs.changes.outputs // {})
  | with_entries(.value |= [tostring | match("contains\\(github\\.event\\.pull_request\\.labels\\.\\*\\.name, '([^']+)'\\)"; "g") | .captures[0].string]);

# A held job's labels as the not-run line says them: "full-ci, lane:a or lane:b".
def label_words($labels):
  if ($labels | length) == 0 then "any label"
  elif ($labels | length) == 1 then $labels[0]
  else ($labels[:-1] | join(", ")) + " or " + $labels[-1] end;

def selection($job; $map; $out; $pr; $event; $holds):
  output_of($job; $map) as $output
  | ($out[$output] // "unset") as $value
  | ($pr | any(. == $job)) as $pr_only
  | (($out.filter_verdicts | try (fromjson | objects) catch empty) // {}) as $verdicts
  | if $value != "true" then
      if $event == "pull_request" and $verdicts[$output] == "true" then
        {selected: false, gated: true,
         why: "selected by paths, held back on a pull request without \(label_words($holds[$output] // [])): runs in the merge queue"}
      else {selected: false, why: "needs.changes.outputs.\($output) is \($value)"}
      end
    elif $pr_only and $event != "pull_request" then
      {selected: false, why: "it runs only on pull_request, and this run is \($event)"}
    else {selected: true,
          why: "needs.changes.outputs.\($output) is true\(if $pr_only then " on a \($event)" else "" end)"}
    end;

# The check runs of one job of ci.yaml among a run's check runs, or among the jobs route's entries
# (each an object with a `name`). $names is what check_run_jobs below reads off ci.yaml: each
# check-run name a leg or a carrier gives through its `name:`, to its job id, an old check-run name
# such as `envoy-go-checks / envoy-go-checks` that no job id carries. A name the map holds belongs
# to the job it gives and to no other; any other name belongs to the job GitHub names it after:
# the job itself (`job`), a job of the workflow it calls (`job / inner`, `job / inner (leg)`), or a
# leg of its matrix with no name of its own (`job (leg)`). ci-ok-report.sh reads a job's result
# this way, and runner-shutdown.sh the check runs of a job whose runner may have been shut down.
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
  | $event == "merge_group" and ($out.unnamed_files // "") != "true"
    and $verdicts[output_of($job; $map)] == "false";

# The legs GitHub makes of one job of a workflow, each as its matrix values: the product of the
# matrix's lists, each `include` entry added to every leg of that product it fits without changing
# a value of those lists, or a leg of its own where it fits none (so a matrix of `include` alone
# is one leg an entry). A job with no matrix is one leg with no values. `exclude` is not read: it
# takes legs away, and a name a run never shows ties nothing.
def matrix_legs:
  (.strategy.matrix // null) as $m
  | if ($m | type) != "object" then [{}]
    else
      [$m | to_entries[] | select(.key != "include" and .key != "exclude" and (.value | type) == "array")] as $axes
      | ($axes | map(.key)) as $keys
      | (if ($axes | length) == 0 then []
         else reduce $axes[] as $axis ([{}]; [.[] as $leg | $axis.value[] | $leg + {($axis.key): .}]) end) as $product
      | reduce (($m.include // []) | if type == "array" then .[] else empty end | objects) as $inc ($product;
          . as $legs
          | [range(0; $product | length)
             | select(. as $i | all($keys[]; . as $k | ($inc | has($k) | not) or $inc[$k] == $legs[$i][$k]))] as $fits
          | if ($fits | length) > 0 then reduce $fits[] as $i ($legs; .[$i] += $inc) else $legs + [$inc] end)
      | if length == 0 then [{}] else . end
    end;

# One term of a name's expression, for one leg: a string literal, `matrix.<key>`, or
# `format('<text>', <term>, …)`. null for any other form.
def name_term($leg):
  if test("^'(?:[^']|'')*'$") then .[1:-1] | gsub("''"; "'")
  elif test("^matrix\\.[A-Za-z0-9_-]+$") then ($leg[.[7:]] // "") | tostring
  elif test("^format\\(") then
    (capture("^format\\(\\s*'(?<f>(?:[^']|'')*)'\\s*(?<a>(?:,[^,()]*)*)\\)$") // null) as $c
    | if $c == null then null
      else [$c.a | splits(",") | gsub("^\\s+|\\s+$"; "") | select(. != "") | name_term($leg)] as $args
        | if any($args[]; . == null) then null
          else reduce range(0; $args | length) as $i ($c.f | gsub("''"; "'"); gsub("\\{\($i)\\}"; $args[$i]))
          end
      end
  else null end;

# The check-run name a job's `name:` gives one leg: the text itself, or the value of the one
# expression it is, `<term> || <term> …` giving its first term that is not empty. null for any
# other form, such as one that reads a context other than `matrix`.
def leg_name($leg):
  if type != "string" then null
  elif test("\\$\\{\\{") | not then .
  else (capture("^\\$\\{\\{\\s*(?<e>.*?)\\s*\\}\\}$") // null) as $c
    | if $c == null then null
      else [$c.e | splits("\\|\\|") | gsub("^\\s+|\\s+$"; "") | name_term($leg)] as $terms
        | if any($terms[]; . == null) then null else (first($terms[] | select(. != "")) // "") end
      end
  end;

# ci-ok's map of check-run names to job ids, read off the workflow itself (its JSON, as
# `yq -o=json` gives it): for each leg of each job with a `name:`, the name that leg's check run
# carries, unless it is the job's own id. The names live in ci.yaml alone, so a leg added there is
# read here with no list to keep beside it. A job of $judged, the jobs ci-ok judges, whose name
# this cannot read is an error, since its legs' check runs would then belong to no job; any other
# job's (pr-title's, ci-ok's own) is passed over.
def check_run_jobs($judged):
  reduce ((.jobs // {}) | to_entries[] | select(.value | type == "object" and has("name"))) as $job ({};
    reduce ($job.value | matrix_legs[]) as $leg (.;
      ($job.value.name | leg_name($leg)) as $name
      | if $name == null then
          if ($judged | any(. == $job.key)) then
            error("job \($job.key)'s name: \($job.value.name) is no form ci-ok.jq reads: a string, or one expression of string literals, matrix.<key>, format(…) and ||")
          else . end
        elif $name == "" or $name == $job.key then .
        else .[$name] = $job.key end));
