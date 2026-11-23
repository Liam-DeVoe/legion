# operator-token-minters/broker-fence.jq: the broker's pod fence (broker-fence.sh says what it is and
# what a credit rests on). A program reads this module with `include "broker-fence";` and jq's -L at
# this directory.
#
# fence_read($contract) reads an array of objects (a render's, or a cluster's admission policies and
# their bindings) against broker-fence.json ($contract): each of its policies and that policy's
# binding of the same name must be there once, bound Deny with failurePolicy Fail to the contract's
# namespace alone, and their specs, less what the API server fills in by default (fence_canon), are
# kept for broker-fence.sh to hold to the contract's digest. fence_refuses($fence; $r) names the
# policy of a credited fence that refuses the right $r, or nothing.

# A field the API server fills in by default reads as one left out: null, [] and {} alike.
def fence_strip: with_entries(select(.value != null and .value != [] and .value != {}));
# fence_selects($ns): whether a label selector picks the namespace $ns by kubernetes.io/metadata.name
# alone, the label the API server sets to a namespace's name and no one can change. A requirement on
# any other label can stop matching when that label changes, so it never selects here.
def fence_selects($ns):
  ((.matchLabels // {}) | to_entries | all(.key == "kubernetes.io/metadata.name" and .value == $ns))
  and ((.matchExpressions // []) | all(.key == "kubernetes.io/metadata.name"
    and ((.operator == "In" and any(.values[]?; . == $ns)) or (.operator == "NotIn" and all(.values[]?; . != $ns))
         or .operator == "Exists")));
# A policy's matchConstraints or a binding's matchResources less its defaults: a rule's scope "*",
# matchPolicy Equivalent and empty selectors. A policy's own namespaceSelector that picks $ns is left
# out too: GKE writes into every admission policy a selector that skips kube-system and its
# gke-managed-* namespaces (read on the dispatch cluster on 2026-11-23), and the binding is what
# holds the fence to $ns.
def fence_match($ns; $policy):
  def rule: (if .scope == "*" then del(.scope) else . end) | fence_strip;
  (. // {})
  | .resourceRules = [(.resourceRules // [])[] | rule]
  | .excludeResourceRules = [(.excludeResourceRules // [])[] | rule]
  | (if .matchPolicy == "Equivalent" then del(.matchPolicy) else . end)
  | (if $policy and .namespaceSelector != null and (.namespaceSelector | fence_selects($ns)) then del(.namespaceSelector) else . end)
  | fence_strip;
# fence_canon($ns): an admission policy's or binding's spec less its defaults; a policy's
# failurePolicy, Fail by default, is written out.
def fence_canon($ns):
  (.spec // {}) as $spec
  | if .kind == "ValidatingAdmissionPolicy" then
      $spec | .failurePolicy = (.failurePolicy // "Fail") | .matchConstraints = (.matchConstraints | fence_match($ns; true))
      | .validations = [(.validations // [])[] | fence_strip] | fence_strip
    else $spec | .matchResources = (.matchResources | fence_match($ns; false)) | fence_strip end;
# fence_shape($ns; $name): what keeps a canonical policy or binding spec named $name from refusing
# what its rules say, to every request in $ns alone, or "": a policy must fail closed and be narrowed
# by nothing beside its rules (no selector, match condition or parameter); a binding must name its
# policy, deny, select $ns alone by its name, and take no parameter.
def fence_shape($kind; $ns; $name):
  if $kind == "ValidatingAdmissionPolicy" then
    if .failurePolicy != "Fail" then "has failurePolicy \(.failurePolicy | tojson), not \"Fail\""
    elif ((.matchConstraints // {}) | keys - ["resourceRules"]) != [] or (keys - ["failurePolicy", "matchConstraints", "validations"]) != [] then
      "is narrowed or widened beside its rules (\([((.matchConstraints // {}) | keys - ["resourceRules"])[], (keys - ["failurePolicy", "matchConstraints", "validations"])[]] | join(", ")))"
    else "" end
  elif .validationActions != ["Deny"] then "binds with validationActions \(.validationActions | tojson), not [\"Deny\"]"
  elif .policyName != $name then "binds the policy \(.policyName | tojson), not its own"
  elif .matchResources != {namespaceSelector: {matchExpressions: [{key: "kubernetes.io/metadata.name", operator: "In", values: [$ns]}]}} then
    "does not select \($ns) alone by its name (matchResources \(.matchResources | tojson))"
  elif (keys - ["matchResources", "policyName", "validationActions"]) != [] then "takes \(keys - ["matchResources", "policyName", "validationActions"] | join(", "))"
  else "" end;
# objs: the objects of a render (a List, an array or a stream of objects) or of a cluster's list;
# code-rbac.jq reads its renders with it too.
def objs: if type == "array" then .[] | objs
          elif type == "object" and (.kind // "") == "List" then (.items // [])[] | objs
          elif type == "object" then . else empty end;
# fence_read($contract): {state: read, absent or mismatch, objects: [{kind, name, state, why, spec,
# generation, resourceVersion}]}, each policy of the contract and its binding in the contract's
# order; absent when none of them is there, read when each is there once in its shape, whose specs
# broker-fence.sh then holds to the contract's digest.
def fence_read($contract):
  . as $objects
  | [$contract.policies[] as $name | ("ValidatingAdmissionPolicy", "ValidatingAdmissionPolicyBinding") as $kind
     | [$objects[] | select(.kind == $kind and .metadata.name == $name)] as $found
     | {kind: $kind, name: $name}
       + if ($found | length) == 0 then {state: "absent", why: "is not there"}
         elif ($found | length) > 1 then {state: "mismatch", why: "is there \($found | length) times"}
         else ($found[0] | fence_canon($contract.namespace)) as $spec
           | ($spec | fence_shape($kind; $contract.namespace; $name)) as $why
           | {state: (if $why == "" then "read" else "mismatch" end), why: $why, spec: $spec,
              generation: ($found[0].metadata.generation // ""), resourceVersion: ($found[0].metadata.resourceVersion // "")}
         end]
  | {state: (if all(.[]; .state == "read") then "read" elif all(.[]; .state == "absent") then "absent" else "mismatch" end), objects: .};
# fence_digest: the canonical text broker-fence.sh hashes, from fence_read's answer: each object's
# kind, name and canonical spec, in the contract's order, its keys sorted (jq -S).
def fence_digest: [.objects[] | {kind, name, spec}];
# fence_refuses($fence; $r): the policy that refuses $r, a right as rights_json writes it, while
# $fence ({namespace, account, direct}) is credited; nothing for any other right. Only entry into a
# pod (exec, attach and port forward, by create or get, which the API server admits as CONNECT) and a
# change of a pod's spec or ephemeral containers (patch and update) in the fence's namespace, which
# the fence refuses on every pod of the broker's with no exception. A right with `targets` (a become
# right, become-rights.jq) is credited only when each target is the fence's account; one without,
# only when the fence's account is the one guarded pair in its namespace ($fence.direct): entry into
# any other pod there reaches what that pod runs as.
def fence_refuses($fence; $r):
  if $fence == null or ($r.group // "") != "" or $r.resource != "pods" or ($r.ns // "") != $fence.namespace then empty
  elif (if $r.targets then ($r.targets | length) > 0 and all($r.targets[]; . == "\($fence.namespace):\($fence.account)")
        else $fence.direct end) | not then empty
  elif ($r.verb == "create" or $r.verb == "get") and ($r.sub == "exec" or $r.sub == "attach" or $r.sub == "portforward") then "broker-pod-connect"
  elif ($r.verb == "patch" or $r.verb == "update") and ($r.sub == "" or $r.sub == "ephemeralcontainers") then "broker-pod-update"
  else empty end;
