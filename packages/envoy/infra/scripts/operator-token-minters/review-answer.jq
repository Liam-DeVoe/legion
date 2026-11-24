# operator-token-minters/review-answer.jq: review's answer (reviews.sh), a record per review of the
# principal, the right's label, allowed, refused or error, the evaluation error, the API server's
# reason, and, for an allowed review, the fence's verdict on the right (broker-fence.jq's
# fence_fields under $fence, the credited fence or null: the policy that refuses its use on the
# broker's pods, and `residual` when its use on the namespace's other pods carries the fence's
# residual; "" and "" for any other review), matched to its question by the subject and the
# attributes the answer echoes; one `short` record when the answer holds fewer reviews than were
# asked. $principals and $rights each come from a file (--slurpfile), and must hold one JSON array
# (one_array).
include "rec";
include "broker-fence";
      one_array($principals; "the principals") as $principals | one_array($rights; "the rights") as $rights
      | ($rights | map({(.key): .}) | add) as $by_key
      | ($principals | map({("\(.user)|\(.groups | join(","))"): .principal}) | add) as $names
      | [(if .kind == "List" then .items[] else . end)
         | (.spec.resourceAttributes // {}) as $a
         | ([$a.verb, $a.group, $a.resource, $a.subresource, $a.namespace, $a.name] | map(. // "") | join("|")) as $key
         | ("\(.spec.user // "")|\((.spec.groups // []) | join(","))") as $who
         | (if .status.allowed == true then "allowed" elif (.status.evaluationError // "") != "" then "error" else "refused" end) as $verdict
         | [($names[$who] // ""), ($by_key[$key].label // ""), $verdict, (.status.evaluationError // ""), (.status.reason // "")]
           + (if $verdict == "allowed" and $by_key[$key] then fence_fields($fence; $by_key[$key]) else ["", ""] end)] as $rows
      | ([$rows[] | select(.[0] != "" and .[1] != "")] | length) as $answered
      | (($principals | length) * ($rights | length)) as $asked
      | if $answered != $asked or ($rows | length) != $asked then ["short", $answered, $asked] | rec else $rows[] | rec end
