# operator-token-minters/code-rbac.jq: the step "code: RBAC" (step-code-rbac.sh) reads the renders
# as the RBAC authorizer does, and live_named (reviews.sh) reads a cluster's live bindings and roles
# the same way. Each binding gives a `grant` record per right of $rights its role holds (the binding,
# the right's label, and the subject's kind, namespace and name), or a `role` record when the input
# does not hold its role (or aggregates it); input with no binding gives one `empty` record with the
# number of objects it holds. A rule limited by resourceNames on any right of $rights or
# $named_rights is a grant too, its label naming the names: a review that names nothing never
# matches it, yet impersonating the group system:masters by name is cluster-admin, and updating one
# webhook configuration, one Deployment or one pod's exec by name is the same path as the unnamed
# right. RBAC never limits a create of a whole object (it has no name yet) or a deletecollection
# by name, so neither is read so. $named_rights are read by name alone; $live true drops the `role`
# and `empty` records, which only a render's reader needs.
include "rec";
        def objs: if type == "array" then .[] | objs
                  elif type == "object" and (.kind // "") == "List" then (.items // [])[] | objs
                  elif type == "object" then . else empty end;
        def has_($list; $x): any(($list // [])[]; . == $x);
        def matches_but_name($r): (has_(.verbs; $r.verb) or has_(.verbs; "*"))
          and (has_(.apiGroups; $r.group) or has_(.apiGroups; "*"))
          and (has_(.resources; "*") or has_(.resources; $r.resource + (if $r.sub != "" then "/" + $r.sub else "" end))
               or ($r.sub != "" and has_(.resources; "*/" + $r.sub)));
        def matches($r): matches_but_name($r)
          and (((.resourceNames // []) | length) == 0 or ($r.name != "" and has_(.resourceNames; $r.name)));
        def named($r): $r.name == "" and (($r.verb == "create" and $r.sub == "") or $r.verb == "deletecollection" | not)
          and ((.resourceNames // []) | length) > 0 and matches_but_name($r);
        def named_label($r; $names): "\($r.verb) \($r.resource)\(if $r.sub != "" then "/" + $r.sub else "" end)\(if $r.group != "" then "." + $r.group else "" end)"
          + " \($names | join(", "))" + (if $r.ns != "" then " in " + $r.ns else " cluster-wide" end);
        ($rights | map(.key)) as $plain
        | ([$rights[], $named_rights[]] | reduce .[] as $r ([]; if any(.[]; .key == $r.key) then . else . + [$r] end)) as $all
        | [inputs | objs] as $objects
        | [$objects[] | select(.kind == "RoleBinding" or .kind == "ClusterRoleBinding")] as $bindings
        | if ($bindings | length) == 0 then (if $live then empty else ["empty", ($objects | length)] | rec end)
          else $bindings[] as $b
          | ($b.metadata.namespace // "") as $bns
          | (if $b.kind == "RoleBinding" then "RoleBinding \($bns)/\($b.metadata.name)" else "ClusterRoleBinding \($b.metadata.name)" end) as $id
          | ([$objects[] | select(.kind == ($b.roleRef.kind // "") and .metadata.name == $b.roleRef.name
                                   and (.kind == "ClusterRole" or (.metadata.namespace // "") == $bns))]) as $found
          # A live aggregated ClusterRole's rules are the aggregation's, filled in by its controller; a
          # render's are not.
          | (if ($found | length) > 0 then (if $found[0].aggregationRule and ($live | not) then null else ($found[0].rules // []) end)
             elif $b.roleRef.kind == "ClusterRole" and $b.roleRef.name == "cluster-admin" then [{verbs: ["*"], apiGroups: ["*"], resources: ["*"]}]
             elif $b.roleRef.kind == "ClusterRole" and $b.roleRef.name == "view" then []
             else null end) as $rules
          | ($b.subjects // [])[] as $s
          | [$s.kind, ($s.namespace // (if $s.kind == "ServiceAccount" then $bns else "" end)), $s.name] as $subject
          | if $rules == null then (if $live then empty else (["role", $id, "\($b.roleRef.kind) \($b.roleRef.name)"] + $subject) | rec end)
            else $all[] as $r
            | select($b.kind == "ClusterRoleBinding" or ($r.ns != "" and $r.ns == $bns))
            | ([$rules[] | select(named($r)) | .resourceNames[]] | unique) as $names
            | if any($plain[]; . == $r.key) and any($rules[]; matches($r)) then (["grant", $id, $r.label] + $subject) | rec
              elif ($names | length) > 0 then (["grant", $id, named_label($r; $names)] + $subject) | rec
              else empty end
            end
          end
