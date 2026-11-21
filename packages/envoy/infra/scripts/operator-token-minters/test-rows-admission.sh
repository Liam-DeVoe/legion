# shellcheck shell=bash
# shellcheck disable=SC2016,SC2154 # the world, its names and its helpers are the test's; each single-quoted $name is a jq variable, not the shell's
# operator-token-minters/test-rows-admission.sh: the rows of operator-token-minters.test.sh on
# admission power outside the admission objects: a webhook's backend, a policy engine's
# cluster-scoped resources, and an admission policy's parameter objects. Sourced by the test after
# its world and helpers; its header says what the fakes model. The rows:
#
#   (ce) a mutating webhook whose selector can match a guarded namespace, backed by the Service
#        hooks/hook: apps/deployer that may patch deployments in hooks exits 1 naming the webhook,
#        on the operator's cluster or another; the same webhook whose namespaceSelector excludes the
#        guarded namespaces by name exits 0; a webhook that calls a URL, and a refused list of
#        webhook configurations, read UNKNOWN;
#   (cf) Kyverno's ClusterPolicy, cluster-scoped: legion/worker that may create clusterpolicies
#        exits 1 naming it; the CRD with no holder exits 0; a refused list of CRDs reads UNKNOWN;
#   (cg) a MutatingAdmissionPolicy whose binding names the ConfigMap hooks/sidecar-params as its
#        parameter: legion/worker that may update that ConfigMap by name exits 1 naming the policy;
#        a parameter kind the discovery does not resolve reads UNKNOWN.

# --- (ce) A webhook's backend: whoever may write its Service, the workload behind it, its endpoints
# or its TLS Secret decides what the webhook does to the operator's next pod. ---
hook_config() {
  jq -n --arg selector "${1:-}" --argjson client "$2" '[{kind: "MutatingWebhookConfiguration", metadata: {name: "hook-config"},
    webhooks: [{name: "pods.example.com", clientConfig: $client}
      + (if $selector == "exclude" then {namespaceSelector: {matchExpressions: [{key: "kubernetes.io/metadata.name", operator: "NotIn",
          values: ["envoy-operator", "secrets-broker"]}]}} else {} end)]}]'
}
backend_row() {
  local service='{"service": {"namespace": "hooks", "name": "hook"}}'
  base_world
  case "$1" in
    backend)
      hook_config "" "$service" >"$world/clusters/operator-cluster/webhooks.json"
      subject_grant operator-cluster "$(sa_subject apps deployer)" patch apps deployments "" hooks ""
      run
      want_status 1 "apps/deployer may patch the webhook backend's Deployment"
      want_line "FINDING system:serviceaccount:apps:deployer: patch deployments.apps in hooks, backing the webhook MutatingWebhookConfiguration hook-config/pods.example.com" \
        "apps/deployer may patch the webhook backend's Deployment"
      ;;
    other-cluster)
      add_cluster other-cluster us-east1
      add_sa other-cluster ci runner
      hook_config "" "$service" >"$world/clusters/other-cluster/webhooks.json"
      subject_grant other-cluster "$(sa_subject ci runner)" update "" services "" hooks ""
      run
      want_status 1 "ci/runner may update the webhook backend's Service on another cluster"
      want_line "FINDING system:serviceaccount:ci:runner: update services in hooks, backing the webhook MutatingWebhookConfiguration hook-config/pods.example.com on cluster $project/us-east1/other-cluster" \
        "ci/runner may update the webhook backend's Service on another cluster"
      ;;
    excluded)
      hook_config exclude "$service" >"$world/clusters/operator-cluster/webhooks.json"
      subject_grant operator-cluster "$(sa_subject apps deployer)" patch apps deployments "" hooks ""
      run
      want_status 0 "a webhook whose selector excludes the guarded namespaces"
      ;;
    url)
      hook_config "" '{"url": "https://hooks.example.com/mutate"}' >"$world/clusters/operator-cluster/webhooks.json"
      run
      want_status 2 "a webhook that calls a URL"
      want_line "UNKNOWN live: RBAC on operator-cluster: the webhook MutatingWebhookConfiguration hook-config/pods.example.com can match a guarded namespace and calls https://hooks.example.com/mutate, outside the cluster, whose holders this check cannot read" \
        "a webhook that calls a URL"
      ;;
    refused)
      : >"$world/clusters/operator-cluster/refuse-webhooks"
      run
      want_status 2 "a refused list of webhook configurations"
      want_text "UNKNOWN live: RBAC on operator-cluster: the API server refused the list of webhook configurations" "a refused list of webhook configurations"
      ;;
  esac
}
if row ce; then
  each backend_row backend other-cluster excluded url refused
  passed "(ce) a write to a matching webhook's backend exits 1 naming the webhook, on the operator's cluster or another; an excluding selector exits 0; a URL webhook and a refused list read UNKNOWN"
fi

# --- (cf) A policy engine's cluster-scoped resources: Kyverno's ClusterPolicy mutates pods. ---
policy_crd_row() {
  base_world
  jq -n '[{spec: {group: "kyverno.io", scope: "Cluster", names: {plural: "clusterpolicies", kind: "ClusterPolicy"}}}]' \
    >"$world/clusters/operator-cluster/crds.json"
  case "$1" in
    holder)
      subject_grant operator-cluster "$(sa_subject legion worker)" create kyverno.io clusterpolicies "" "" ""
      run
      want_status 1 "legion/worker may create Kyverno ClusterPolicies"
      want_line "FINDING system:serviceaccount:legion:worker: create clusterpolicies.kyverno.io cluster-wide, a policy engine's resource" \
        "legion/worker may create Kyverno ClusterPolicies"
      ;;
    no-holder)
      run
      want_status 0 "Kyverno's ClusterPolicy with no holder"
      ;;
    refused)
      : >"$world/clusters/operator-cluster/refuse-crds"
      run
      want_status 2 "a refused list of CRDs"
      want_text "UNKNOWN live: RBAC on operator-cluster: the API server refused the list of custom resource definitions" "a refused list of CRDs"
      ;;
  esac
}
if row cf; then
  each policy_crd_row holder no-holder refused
  passed "(cf) a holder of create on Kyverno's ClusterPolicy exits 1, the CRD with no holder exits 0, and a refused list of CRDs reads UNKNOWN"
fi

# --- (cg) An admission policy's parameter object, named by its binding's paramRef. ---
param_row() {
  local dir="$world/clusters/operator-cluster"
  base_world
  printf '%s.admissionregistration.k8s.io\n' mutatingadmissionpolicies mutatingadmissionpolicybindings mutatingwebhookconfigurations \
    validatingadmissionpolicies validatingadmissionpolicybindings validatingwebhookconfigurations >"$dir/admission-resources"
  jq -n --arg api "$([ "$1" = unresolved ] && echo example.com/v1 || echo v1)" --arg kind "$([ "$1" = unresolved ] && echo Widget || echo ConfigMap)" '[
    {kind: "MutatingAdmissionPolicy", metadata: {name: "inject-sidecar"}, spec: {paramKind: {apiVersion: $api, kind: $kind}}},
    {kind: "MutatingAdmissionPolicyBinding", metadata: {name: "inject-sidecar-binding"},
     spec: {policyName: "inject-sidecar", paramRef: {name: "sidecar-params", namespace: "hooks"}}}]' >"$dir/admission-policies.json"
  jq -n '{"/api/v1": {resources: [{name: "configmaps", kind: "ConfigMap", namespaced: true}]}}' >"$dir/raw.json"
  case "$1" in
    holder)
      subject_grant operator-cluster "$(sa_subject legion worker)" update "" configmaps "" hooks sidecar-params
      run
      want_status 1 "legion/worker may update the admission policy's parameter by name"
      want_line "FINDING system:serviceaccount:legion:worker: update configmaps sidecar-params in hooks, a parameter of the admission policy MutatingAdmissionPolicy inject-sidecar" \
        "legion/worker may update the admission policy's parameter by name"
      ;;
    unresolved)
      run
      want_status 2 "a parameter kind the discovery does not resolve"
      want_line "UNKNOWN live: RBAC on operator-cluster: the parameter kind Widget (example.com/v1) of the admission policy MutatingAdmissionPolicy inject-sidecar is not resolved by the API server's discovery, so who may write its parameters is not reviewed" \
        "a parameter kind the discovery does not resolve"
      ;;
  esac
}
if row cg; then
  each param_row holder unresolved
  passed "(cg) a holder of update on an admission policy's parameter object by name exits 1, and an unresolved parameter kind reads UNKNOWN"
fi
