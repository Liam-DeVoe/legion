# shellcheck shell=bash
# shellcheck disable=SC2034,SC2153,SC2154 # globals set and read across the sourced files, in operator-token-minters.sh's order
# operator-token-minters/admission-backends.sh: admission power outside the admission objects. A
# webhook configuration or an admission policy runs what something else holds: a webhook's backend
# Service and the workload behind it, a policy engine's cluster-scoped resources, an admission
# policy's parameter objects. Whoever may write those changes what the operator's next pod runs, or
# what the operator may apply. Sourced by operator-token-minters.sh, whose header says what each
# step checks.

# admission_power KUBECONFIG STEP: sets POWER_RIGHTS, the JSON array, as rights_json writes rights, of
# the writes that give admission power beside the admission objects' own on the cluster, and prints
# UNKNOWN on STEP for what it cannot read:
#   - each webhook whose namespaceSelector can match a guarded namespace (webhook-backends.jq): create,
#     update and patch of its Service, Endpoints, EndpointSlices, Secrets (its TLS key), pods and pod
#     controllers, in the Service's namespace; a webhook that calls a URL outside the cluster is
#     UNKNOWN, as is a refused list;
#   - each cluster-scoped custom resource of a policy engine (a CRD of kyverno.io, gatekeeper.sh or
#     its subgroups, or a group whose name holds "policy"): create, update and patch cluster-wide, as
#     Kyverno's ClusterPolicy or Gatekeeper's Assign mutates pods; a refused list is UNKNOWN;
#   - each parameter object an admission policy binding names (paramRef), of the policy's paramKind:
#     create, and update and patch by its name when the binding names one, in the binding's
#     namespace or cluster-wide; a parameter kind the discovery does not resolve, or a refused list,
#     is UNKNOWN. Reads $WORK/admission-resources, which admission_rights writes.
admission_power() {
  local kube="$1" step="$2" guarded id url policies
  POWER_RIGHTS='[]'
  guarded="$(printf '%s\n' "${GUARDED[@]%%/*}" | jq -R -s -c 'split("\n") | map(select(length > 0)) | unique')"
  if ! kubectl --kubeconfig "$kube" get mutatingwebhookconfigurations,validatingwebhookconfigurations -o json >"$WORK/webhooks.json" 2>"$WORK/webhooks.err"; then
    unknown "$step" "the API server refused the list of webhook configurations (it needs list on mutatingwebhookconfigurations and validatingwebhookconfigurations, container.mutatingWebhookConfigurations.list and container.validatingWebhookConfigurations.list on GKE), so no webhook's backend is reviewed: $(first_line "$WORK/webhooks.err")"
  elif ! jq -c --argjson guarded "$guarded" -f "$LIB/webhook-backends.jq" "$WORK/webhooks.json" >"$WORK/webhooks.out" 2>"$WORK/webhooks.err"; then
    unknown "$step" "the API server's list of webhook configurations is not JSON: $(first_line "$WORK/webhooks.err")"
  else
    POWER_RIGHTS="$(jq -c .rights "$WORK/webhooks.out")"
    while IFS=$'\t' read -r id url; do
      unknown "$step" "the webhook $id can match a guarded namespace and calls $url, outside the cluster, whose holders this check cannot read"
    done < <(jq -r '.urls[] | [.id, .url] | @tsv' "$WORK/webhooks.out")
  fi
  if ! kubectl --kubeconfig "$kube" get customresourcedefinitions -o json >"$WORK/crds.json" 2>"$WORK/crds.err"; then
    unknown "$step" "the API server refused the list of custom resource definitions (it needs list on customresourcedefinitions, container.customResourceDefinitions.list on GKE), so a policy engine's cluster-scoped resources are not reviewed: $(first_line "$WORK/crds.err")"
  elif ! POWER_RIGHTS="$(with_rights "$POWER_RIGHTS" "$(jq -c '[.items[]? | select(.spec.scope == "Cluster") | .spec.group as $g
      | select($g == "kyverno.io" or $g == "gatekeeper.sh" or ($g | endswith(".gatekeeper.sh")) or ($g | test("policy"; "i")))
      | .spec.names.plural as $p | ("create", "update", "patch")
      | {verb: ., group: $g, resource: $p, sub: "", ns: "", name: "", label: "\(.) \($p).\($g) cluster-wide, a policy engine'"'"'s resource"}
      | . + {key: ([.verb, .group, .resource, .sub, .ns, .name] | join("|"))}]' "$WORK/crds.json" 2>"$WORK/crds.err")")"; then
    unknown "$step" "the API server's list of custom resource definitions is not JSON: $(first_line "$WORK/crds.err")"
  fi
  policies="$(sed -n 's/^\([a-z]*admissionpolic\(y\|ies\)\(bindings\)\?\)\.admissionregistration\.k8s\.io$/\1/p' "$WORK/admission-resources" 2>/dev/null |
    sort -u | paste -sd, -)"
  [ -z "$policies" ] || param_rights "$kube" "$step" "$policies"
}

# param_rights KUBECONFIG STEP RESOURCES: adds to POWER_RIGHTS the writes to each parameter object an
# admission policy binding of RESOURCES (the admission policies and their bindings the discovery
# lists, comma-separated) names, as admission_power says.
param_rights() {
  local policy kind api resource namespaced pns pname path
  if ! kubectl --kubeconfig "$1" get "$3" -o json >"$WORK/policies.json" 2>"$WORK/policies.err"; then
    unknown "$2" "the API server refused the list of admission policies and their bindings (it needs list on $3), so a policy's parameter objects are not reviewed: $(first_line "$WORK/policies.err")"
    return
  fi
  # The unit separator, not a tab: read merges a run of tabs, and the namespace or name may be empty.
  while IFS="$US" read -r policy api kind pns pname; do
    case "$api" in */*) path="/apis/$api" ;; *) path="/api/$api" ;; esac
    if ! kubectl --kubeconfig "$1" get --raw "$path" >"$WORK/raw.json" 2>"$WORK/raw.err" ||
      ! IFS=$'\t' read -r resource namespaced < <(jq -r --arg k "$kind" 'first(.resources[] | select(.kind == $k and (.name | contains("/") | not))) | [.name, .namespaced] | @tsv' "$WORK/raw.json" 2>/dev/null); then
      unknown "$2" "the parameter kind $kind ($api) of the admission policy $policy is not resolved by the API server's discovery, so who may write its parameters is not reviewed"
      continue
    fi
    case "$api" in */*) resource="$resource.${api%/*}" ;; esac
    [ "$namespaced" = true ] || pns=""
    POWER_RIGHTS="$(with_rights "$POWER_RIGHTS" "$(jq -n -c --arg res "$resource" --arg ns "$pns" --arg name "$pname" --arg policy "$policy" '
      ($res | split(".")) as $parts | ($parts[0]) as $r | ($parts[1:] | join(".")) as $g
      | [{verb: "create", name: ""}, ({verb: ("update", "patch"), name: $name} | select($name != ""))]
      | map({verb, group: $g, resource: $r, sub: "", ns: $ns, name,
             label: "\(.verb) \($res)\(if .name != "" then " " + .name else "" end)\(if $ns != "" then " in " + $ns else " cluster-wide" end), a parameter of the admission policy \($policy)"}
            | . + {key: ([.verb, .group, .resource, .sub, .ns, .name] | join("|"))})')")"
  done < <(jq -r '(.items // []) as $all
    | $all[] | select(.kind | endswith("PolicyBinding")) | . as $b
    | first($all[] | select((.kind + "Binding") == $b.kind and .metadata.name == $b.spec.policyName)) as $p
    | select($p.spec.paramKind != null and $b.spec.paramRef != null)
    | ["\($p.kind) \($p.metadata.name)", $p.spec.paramKind.apiVersion, $p.spec.paramKind.kind,
       ($b.spec.paramRef.namespace // ""), ($b.spec.paramRef.name // "")] | join("\u001f")' "$WORK/policies.json")
}
