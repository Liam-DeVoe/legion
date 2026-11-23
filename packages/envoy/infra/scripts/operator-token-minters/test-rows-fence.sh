# shellcheck shell=bash
# shellcheck disable=SC2016,SC2034,SC2154 # the world, its names, its helpers and the output and status a run sets are the test's; each single-quoted $name is a jq variable, not the shell's
# operator-token-minters/test-rows-fence.sh: the rows of operator-token-minters.test.sh on the
# broker's pod fence (broker-fence.sh), the admission refusal the check credits. Sourced by the test
# after its world and helpers; its header says what the fakes model. Each row's world (fence_world)
# puts the broker in dispatch, guarded as --broker-workload dispatch/envoy-broker with --db-secret
# dispatch/envoy-broker-db, with the test's example fence (fence_objects), and gives the three
# grants the fence answers: comment-delivery's exec (create and get) and dispatch-measure's exec and
# pod patch in dispatch, and legion/worker's port forward there, live, and comment-delivery's exec
# declared by the render. The rows run fence_check, the check pinned to that example, but (fg). The
# rows:
#
#   (fa) the fence the renders declare and the cluster holds as pinned, its live objects carrying
#        what GKE and the API server fill in, credits each of those grants, live and declared: exit
#        0, a CREDIT line each, the code step's PASS, and --report's `credited` naming the policy,
#        its binding and their declared and live versions;
#   (fb) each way the fence fails credits nothing, each grant a finding again, and says why: the
#        fence removed, a binding of Warn, a policy of failurePolicy Ignore, a binding whose
#        selector misses dispatch, a policy whose own selector does, an exception added to the
#        connect rule, the cluster's fence differing from the renders', a list of admission
#        policies or of pods that cannot be read (UNKNOWN), a pod outside the fence's names running
#        as envoy-broker, a list with no broker pod, and another guarded pair in dispatch;
#   (fc) a principal that can write the fence stays a finding: patch of the binding, delete of a
#        policy, and delete of broker-pod-connect by name;
#   (fd) a ServiceAccount that becomes legion/dispatch-measure, whose exec and pod patch in dispatch
#        are credited and which can patch admission policies, is a finding naming the chain;
#        without the policy patch, nothing is found;
#   (fe) who can enter a pod of dispatch is credited for becoming envoy-broker, which has a finding,
#        and found once dispatch/dispatch has one too;
#   (ff) a Google IAM minter of the broker's account stays a finding beside the credits;
#   (fg) the check itself, whose broker-fence.json pins the reviewed fence, credits nothing of the
#        example fence: its rules hash to another digest.
#
# Each control is the row's own other half: (fa) against (fb), (fc), (fd), (ff) and (fg) the same
# world with one fault, one grant more or the reviewed pin, and (fd) and (fe) a half without it.

# The CREDIT lines (fa) wants, and the FINDING lines the same grants are once the fence is not
# credited. A credit's sentence ends naming the policy that refuses it.
credit_line() { echo "CREDIT $1: $2, refused by the ValidatingAdmissionPolicy $3 (binding $3: Deny, failurePolicy Fail), declared and live alike"; }
fence_credits_want=(
  "serviceAccount:$comment_delivery|create pods/exec in dispatch|broker-pod-connect"
  "serviceAccount:$comment_delivery|get pods/exec in dispatch|broker-pod-connect"
  "system:serviceaccount:legion:dispatch-measure|create pods/exec in dispatch|broker-pod-connect"
  "system:serviceaccount:legion:dispatch-measure|patch pods in dispatch|broker-pod-update"
  "system:serviceaccount:legion:worker|create pods/portforward in dispatch|broker-pod-connect"
  "serviceAccount:$comment_delivery|create pods/exec in dispatch, declared by RoleBinding dispatch/comment-delivery|broker-pod-connect"
  "serviceAccount:$comment_delivery|get pods/exec in dispatch, declared by RoleBinding dispatch/comment-delivery|broker-pod-connect"
)
want_fence_credits() {
  local entry principal right policy
  for entry in "${fence_credits_want[@]}"; do
    IFS='|' read -r principal right policy <<<"$entry"
    want_line "$(credit_line "$principal" "$right" "$policy")" "$1"
  done
}
want_fence_findings() {
  local entry principal right policy
  for entry in "${fence_credits_want[@]}"; do
    IFS='|' read -r principal right policy <<<"$entry"
    want_line "FINDING $principal: $right" "$1"
  done
  [ "$(count_prefix "CREDIT ")" -eq 0 ] || fail "$1: a CREDIT line"
}

# --- (fa) The fence as pinned credits what it refuses, live and declared. ---
if row fa; then
  jq -e --arg pin "$fence_pin" '.namespace == "dispatch" and .account == "envoy-broker" and .prefix == "envoy-broker-"
    and .policies == ["broker-pod-connect", "broker-pod-update", "broker-pod-create", "broker-workload-create", "broker-workload-update"]
    and (.digest | test("^sha256:[0-9a-f]{64}$")) and .digest != $pin' "$fence_contract" >/dev/null ||
    fail "broker-fence.json does not name the broker's fence in dispatch with a sha256 pin other than this test's example's"
  fence_world
  report="$temporary_dir/fence-report.json"
  fence_run --report "$report"
  want_status 0 "the fence as pinned"
  want_fence_credits "the fence as pinned"
  [ "$(count_prefix "FINDING ")" -eq 0 ] || fail "the fence as pinned: a FINDING line"
  want_text "PASS code: RBAC: the renders give the rights" "the fence as pinned"
  want_text ", beside 2 grants the broker's pod fence refuses, declared and live alike" "the fence as pinned"
  want_text "PASS live: RBAC on operator-cluster: " "the fence as pinned"
  jq -e --arg cd "serviceAccount:$comment_delivery" '.findings == [] and (.credited | length) == 7
      and all(.credited[]; . as $c | keys == ["binding", "declared", "granted_at", "live", "policy", "principal", "resource", "right"]
              and ($c.declared | test("^sha256:[0-9a-f]{64}$")) and ($c.live | startswith($c.declared + "; "))
              and ($c.live | endswith("; ValidatingAdmissionPolicy \($c.policy) generation 3 resourceVersion 7001; ValidatingAdmissionPolicyBinding \($c.policy) generation 1 resourceVersion 7001")))
      and any(.credited[]; .principal == $cd and .right == "get pods/exec in dispatch" and .policy == "broker-pod-connect"
              and .binding == "broker-pod-connect" and .resource == "cluster:operator-cluster" and (.granted_at | startswith("RBAC: allowed by ")))
      and any(.credited[]; .principal == $cd and .granted_at == "RoleBinding dispatch/comment-delivery")' "$report" >/dev/null ||
    fail "the fence as pinned: --report's credited is not the seven credits with their policy, binding and versions: $(jq -c '.credited // "absent"' "$report")"
  passed "(fa) the fence as pinned, declared and live, credits comment-delivery's exec, dispatch-measure's exec and pod patch and legion/worker's port forward, each with its evidence in --report"
fi

# --- (fb) A fence that fails any part of its credit credits nothing. ---
fence_fail_row() {
  local want
  fence_world
  case "$1" in
    removed)
      edit "$world/render.json" '.items |= map(select(.kind | startswith("ValidatingAdmissionPolicy") | not))'
      echo '[]' >"$world/clusters/operator-cluster/admission-policies.json"
      want="NO CREDIT live: RBAC on operator-cluster: the broker's pod fence (broker-pod-connect, broker-pod-update, broker-pod-create, broker-workload-create, broker-workload-update in dispatch) is not credited, so what it would refuse stays a finding: the renders hold none of its policies and bindings"
      ;;
    warn)
      fence_edit both 'if .kind == "ValidatingAdmissionPolicyBinding" and .metadata.name == "broker-pod-connect" then .spec.validationActions = ["Warn"] else . end'
      want="in the renders, the ValidatingAdmissionPolicyBinding broker-pod-connect binds with validationActions [\"Warn\"], not [\"Deny\"]"
      ;;
    ignore)
      fence_edit both 'if .kind == "ValidatingAdmissionPolicy" and .metadata.name == "broker-pod-update" then .spec.failurePolicy = "Ignore" else . end'
      want="in the renders, the ValidatingAdmissionPolicy broker-pod-update has failurePolicy \"Ignore\", not \"Fail\""
      ;;
    selector)
      fence_edit both 'if .kind == "ValidatingAdmissionPolicyBinding" and .metadata.name == "broker-pod-connect"
        then .spec.matchResources.namespaceSelector.matchExpressions[0].values = ["dispatch-staging"] else . end'
      want="in the renders, the ValidatingAdmissionPolicyBinding broker-pod-connect does not select dispatch alone by its name"
      ;;
    policy-selector)
      fence_edit both 'if .kind == "ValidatingAdmissionPolicy" and .metadata.name == "broker-pod-connect"
        then .spec.matchConstraints.namespaceSelector = {matchExpressions: [{key: "kubernetes.io/metadata.name", operator: "NotIn", values: ["dispatch"]}]} else . end'
      want="in the renders, the ValidatingAdmissionPolicy broker-pod-connect is narrowed or widened beside its rules (namespaceSelector)"
      ;;
    exception)
      fence_edit both 'if .kind == "ValidatingAdmissionPolicy" and .metadata.name == "broker-pod-connect"
        then .spec.validations[0].expression = "request.userInfo.username == '"'system:serviceaccount:legion:worker'"' || " + .spec.validations[0].expression else . end'
      want="in the renders, the policies' and bindings' rules hash to sha256:"
      ;;
    drift)
      fence_edit live 'if .kind == "ValidatingAdmissionPolicy" and .metadata.name == "broker-pod-update"
        then .spec.matchConstraints.resourceRules |= map(select(.resources != ["pods/ephemeralcontainers"])) else . end'
      want="NO CREDIT live: RBAC on operator-cluster: the broker's pod fence (broker-pod-connect, broker-pod-update, broker-pod-create, broker-workload-create, broker-workload-update in dispatch) is not credited, so what it would refuse stays a finding: in the cluster, the policies' and bindings' rules hash to sha256:"
      ;;
    unread)
      : >"$world/clusters/operator-cluster/refuse-policies"
      want="UNKNOWN live: RBAC on operator-cluster: the API server refused the list of admission policies and their bindings (it needs list on validatingadmissionpolicies and validatingadmissionpolicybindings), so the broker's pod fence (broker-pod-connect, broker-pod-update, broker-pod-create, broker-workload-create, broker-workload-update) is unread and nothing it refuses is credited: "
      ;;
    pods-unread)
      : >"$world/clusters/operator-cluster/refuse-pods"
      want="UNKNOWN live: RBAC on operator-cluster: the API server refused the list of pods in dispatch (it needs list on pods there, container.pods.list on GKE), so whether a pod outside the broker's pod fence runs as envoy-broker or takes its Secrets is unread and nothing the fence refuses is credited: "
      ;;
    rogue-pod)
      edit "$world/clusters/operator-cluster/pods.json" '. + [{metadata: {namespace: "dispatch", name: "debug-7f9c"},
        spec: {serviceAccountName: "dispatch", volumes: [{name: "db", secret: {secretName: "envoy-broker-db"}}], containers: [{name: "sh"}]}}]'
      want="pods of dispatch run as envoy-broker or take its Secrets under a name the fence's connect rule does not hold (envoy-broker-*): debug-7f9c (runs as dispatch, takes envoy-broker-db)"
      ;;
    no-pod)
      echo '[]' >"$world/clusters/operator-cluster/pods.json"
      want="dispatch lists no pod named envoy-broker-* that runs as envoy-broker, so its list of pods cannot be told from one that saw nothing"
      ;;
    shared)
      fence_run --guarded dispatch/dispatch
      want_status 1 "another guarded pair in dispatch"
      want_fence_findings "another guarded pair in dispatch"
      return
      ;;
  esac
  fence_run
  want_status 1 "the fence $1"
  want_fence_findings "the fence $1"
  want_text "$want" "the fence $1"
}
if row fb; then
  each fence_fail_row removed warn ignore selector policy-selector exception drift unread pods-unread rogue-pod no-pod shared
  passed "(fb) the fence removed, bound Warn, failing open, missing dispatch in a binding's or a policy's selector, with an exception, differing live from declared, unread, beside a pod outside its names or with no broker pod, or beside another guarded pair in dispatch, credits nothing"
fi

# --- (fc) Whoever can write the fence stays a finding while it is credited. ---
fence_writer_row() {
  local want
  fence_world
  case "$1" in
    patch-binding)
      subject_grant operator-cluster "$(sa_subject legion worker)" patch admissionregistration.k8s.io validatingadmissionpolicybindings "" "" ""
      want="FINDING system:serviceaccount:legion:worker: patch validatingadmissionpolicybindings.admissionregistration.k8s.io cluster-wide"
      ;;
    delete-policy)
      subject_grant operator-cluster "$(sa_subject legion worker)" delete admissionregistration.k8s.io validatingadmissionpolicies "" "" ""
      want="FINDING system:serviceaccount:legion:worker: delete validatingadmissionpolicies.admissionregistration.k8s.io cluster-wide, which removes the broker's pod fence"
      ;;
    delete-named)
      subject_grant operator-cluster "$(sa_subject legion worker)" delete admissionregistration.k8s.io validatingadmissionpolicies "" "" broker-pod-connect
      want="FINDING system:serviceaccount:legion:worker: delete validatingadmissionpolicies.admissionregistration.k8s.io broker-pod-connect cluster-wide, which removes the broker's pod fence"
      ;;
  esac
  fence_run
  want_status 1 "a writer of the fence ($1)"
  want_line "$want" "a writer of the fence ($1)"
  want_fence_credits "a writer of the fence ($1)"
}
if row fc; then
  each fence_writer_row patch-binding delete-policy delete-named
  passed "(fc) a principal that can patch the fence's binding or delete its policy, outright or by name, stays a finding beside the credits"
fi

# --- (fd) A chain through dispatch-measure to a writer of the admission policies. ---
if row fd; then
  chain="FINDING system:serviceaccount:legion:worker: create serviceaccounts/token dispatch-measure in legion, so it can become system:serviceaccount:legion:dispatch-measure, which can patch validatingadmissionpolicies.admissionregistration.k8s.io cluster-wide"
  fence_world
  subject_grant operator-cluster "$(sa_subject legion worker)" create "" serviceaccounts token legion dispatch-measure
  fence_run
  want_status 0 "dispatch-measure with its credited rights alone"
  want_fence_credits "dispatch-measure with its credited rights alone"
  ! has_line "$chain" || fail "dispatch-measure with its credited rights alone: who can become it is a finding"
  subject_grant operator-cluster "$(sa_subject legion dispatch-measure)" patch admissionregistration.k8s.io validatingadmissionpolicies "" "" ""
  fence_run
  want_status 1 "dispatch-measure that can patch admission policies"
  want_line "FINDING system:serviceaccount:legion:dispatch-measure: patch validatingadmissionpolicies.admissionregistration.k8s.io cluster-wide" \
    "dispatch-measure that can patch admission policies"
  want_line "$chain" "dispatch-measure that can patch admission policies"
  want_fence_credits "dispatch-measure that can patch admission policies"
  passed "(fd) who can become dispatch-measure is a finding once dispatch-measure can patch the admission policies, and not for its credited exec and pod patch alone"
fi

# --- (fe) Becoming envoy-broker by entering a pod of dispatch, which the fence refuses. ---
if row fe; then
  fence_world
  subject_grant operator-cluster "$(sa_subject dispatch envoy-broker)" create "" pods "" dispatch ""
  fence_run
  want_status 1 "envoy-broker with a finding"
  want_line "FINDING system:serviceaccount:dispatch:envoy-broker: create pods in dispatch" "envoy-broker with a finding"
  want_line "$(credit_line system:serviceaccount:legion:worker "create pods/portforward in dispatch, so it can become system:serviceaccount:dispatch:envoy-broker, which can create pods in dispatch" broker-pod-connect)" \
    "envoy-broker with a finding"
  [ "$(count_prefix "FINDING system:serviceaccount:legion:worker:")" -eq 0 ] || fail "envoy-broker with a finding: legion/worker is a finding"
  subject_grant operator-cluster "$(sa_subject dispatch dispatch)" create "" pods "" dispatch ""
  fence_run
  want_status 1 "envoy-broker and dispatch with a finding"
  want_text "FINDING system:serviceaccount:legion:worker: create pods/portforward in dispatch, so it can become system:serviceaccount:dispatch:dispatch, which can create pods in dispatch, or system:serviceaccount:dispatch:envoy-broker" \
    "envoy-broker and dispatch with a finding"
  passed "(fe) who can enter a pod of dispatch is credited for becoming envoy-broker, and found once it can become dispatch/dispatch too"
fi

# --- (ff) A Google IAM minter of the broker's account is no pod route: the fence excuses it not. ---
if row ff; then
  fence_world
  iam "$broker_res" roles/iam.serviceAccountTokenCreator "serviceAccount:$comment_delivery"
  fence_run
  want_status 1 "comment-delivery minting the broker's token"
  want_text "FINDING serviceAccount:$comment_delivery: iam.serviceAccounts.getAccessToken," "comment-delivery minting the broker's token"
  want_text " on $broker_res through roles/iam.serviceAccountTokenCreator" "comment-delivery minting the broker's token"
  want_fence_credits "comment-delivery minting the broker's token"
  passed "(ff) a holder of roles/iam.serviceAccountTokenCreator on the broker's account stays a finding beside the fence's credits"
fi

# --- (fg) The check's own pin is the reviewed fence's, which the example fence does not hash to. ---
if row fg; then
  fence_world
  run --broker-workload dispatch/envoy-broker --db-secret dispatch/envoy-broker-db
  want_status 1 "the example fence against the reviewed pin"
  want_fence_findings "the example fence against the reviewed pin"
  want_text "in the renders, the policies' and bindings' rules hash to $fence_pin, not to the reviewed fence's $(jq -r .digest "$fence_contract")" \
    "the example fence against the reviewed pin"
  passed "(fg) the check itself credits nothing of a fence whose rules hash to another digest than its pin"
fi
