# shellcheck shell=bash
# shellcheck disable=SC2016,SC2034,SC2154 # the world, its names, its helpers and the output and status a run sets are the test's; each single-quoted $name is a jq variable, not the shell's
# operator-token-minters/test-rows-fence.sh: the rows of operator-token-minters.test.sh on the
# broker's pod fence (broker-fence.sh), the admission refusal the check credits. Sourced by the test
# after its world and helpers; its header says what the fakes model. Each row's world (fence_world)
# puts the broker in dispatch, guarded as --broker-workload dispatch/envoy-broker with --db-secret
# dispatch/envoy-broker-db, with the fence of fence_objects (the reviewed rules broker-fence.json
# holds, and example workload policies), and gives the three grants the fence answers:
# comment-delivery's exec (create and get) and dispatch-measure's exec and pod patch in dispatch,
# and legion/worker's port forward there, live, and comment-delivery's exec declared by the render.
# The rows run fence_check, the check whose contract holds the example workload policies' digests,
# but (fg). The rows:
#
#   (fa) the fence the renders declare and the cluster holds as reviewed, its live objects carrying
#        what GKE and the API server fill in, credits each of those grants, live and declared: exit
#        0, a CREDIT line each naming the use it refuses, an ALLOWANCE line each naming the residual
#        of broker-fence.json, the code step's PASS, and --report's `credited` (the policy and the
#        declared and live versions) and `residuals` (the residual and INFRA-4604);
#   (fb) each way the fence fails credits nothing, each grant a finding again, and says why: the
#        fence removed, a binding of Warn, a policy of failurePolicy Ignore, a binding whose
#        selector misses dispatch, a policy whose own selector does, an exception added to the
#        connect rule, the cluster's fence differing from the renders', a list of admission
#        policies or of pods that cannot be read (UNKNOWN), a pod outside the fence's names running
#        as envoy-broker or taking --db-secret, a pod outside them taking another Secret a broker
#        pod takes (its NATS issuer seed), a list with no broker pod, a webhook backed by a Service
#        in dispatch, a list of webhooks that cannot be read, and another guarded pair in dispatch;
#   (fc) a principal that can write the fence stays a finding: patch of the binding, delete of a
#        policy, and delete of broker-pod-connect by name;
#   (fd) a ServiceAccount that becomes legion/dispatch-measure, whose exec and pod patch in dispatch
#        are credited and which can patch admission policies, is a finding naming the chain;
#        without the policy patch, nothing is found;
#   (fe) who can enter a pod of dispatch is credited for becoming envoy-broker, which has a finding,
#        with no ALLOWANCE of its own, and found once dispatch/dispatch has one too;
#   (ff) a Google IAM minter of the broker's account stays a finding beside the credits;
#   (fg) the check itself, whose broker-fence.json holds the reviewed workload policies by digest,
#        credits nothing of the example fence: its workload policies hash to other digests;
#   (fh) patch, or update, of pods/ephemeralcontainers in dispatch stays a finding beside the
#        credits: on a pod not the broker's an ephemeral container takes any Secret of the
#        namespace, --db-secret among them;
#   (fr) each right fence_refuses credits is one the reviewed rules in broker-fence.json admit as
#        CONNECT or UPDATE on pods and refuse by the envoy-broker- prefix; the same reading of a
#        contract whose connect policy lacks port forward fails.
#
# Each control is the row's own other half: (fa) against (fb), (fc), (fd), (ff), (fg) and (fh) the
# same world with one fault, one grant more or the reviewed digests, (fd) and (fe) a half without
# it, and (fr) a contract with one rule taken out.

# The CREDIT and ALLOWANCE lines (fa) wants, and the FINDING lines the same grants are once the fence
# is not credited. A credit's sentence names the use the policy refuses; the allowance's, the
# residual of broker-fence.json.
credit_line() { echo "CREDIT $1: $2, its use on envoy-broker-* pods refused by the ValidatingAdmissionPolicy $3 (bound Deny, failurePolicy Fail), declared and live alike"; }
fence_residual="$(jq -r .residual.text "$fence_contract")"
readonly fence_residual
allowance_line() { echo "ALLOWANCE $1: $2, $fence_residual"; }
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
    want_line "$(allowance_line "$principal" "$right")" "$1"
  done
}
want_fence_findings() {
  local entry principal right policy
  for entry in "${fence_credits_want[@]}"; do
    IFS='|' read -r principal right policy <<<"$entry"
    want_line "FINDING $principal: $right" "$1"
  done
  [ "$(count_prefix "CREDIT ")" -eq 0 ] || fail "$1: a CREDIT line"
  [ "$(count_prefix "ALLOWANCE ")" -eq 0 ] || fail "$1: an ALLOWANCE line"
}

# --- (fa) The fence as reviewed credits what it refuses, live and declared. ---
if row fa; then
  jq -e '.namespace == "dispatch" and .account == "envoy-broker" and .prefix == "envoy-broker-"
    and [.objects[] | "\(.kind) \(.name)"] == [("broker-pod-connect", "broker-pod-update", "broker-pod-create", "broker-workload-create", "broker-workload-update")
      | "ValidatingAdmissionPolicy \(.)", "ValidatingAdmissionPolicyBinding \(.)"]
    and [.objects[] | select(.digest) | .name] == ["broker-workload-create", "broker-workload-update"]
    and all(.objects[] | select(.digest); (.digest | test("^sha256:[0-9a-f]{64}$")) and .spec == null)
    and all(.objects[] | select(.digest | not); .spec != null)
    and .residual.tracked_by == "INFRA-4604" and (.residual.text | test("INFRA-4604"))' "$fence_contract" >/dev/null ||
    fail "broker-fence.json does not hold the broker's fence in dispatch, each object by its spec but the workload policies by a sha256, and its residual"
  fence_world
  report="$temporary_dir/fence-report.json"
  fence_run --report "$report"
  want_status 0 "the fence as reviewed"
  want_fence_credits "the fence as reviewed"
  [ "$(count_prefix "FINDING ")" -eq 0 ] || fail "the fence as reviewed: a FINDING line"
  want_text "PASS code: RBAC: the renders give the rights" "the fence as reviewed"
  want_text ", beside 2 grants whose use on the broker's pods its pod fence refuses, declared and live alike" "the fence as reviewed"
  want_text "PASS live: RBAC on operator-cluster: " "the fence as reviewed"
  jq -e --arg cd "serviceAccount:$comment_delivery" --arg residual "$fence_residual" '.findings == [] and (.credited | length) == 7
      and all(.credited[]; . as $c | keys == ["declared", "granted_at", "live", "policy", "principal", "resource", "right"]
              and ($c.declared | test("^sha256:[0-9a-f]{64}$")) and ($c.live | startswith($c.declared + "; "))
              and ($c.live | endswith("; ValidatingAdmissionPolicy \($c.policy) generation 3 resourceVersion 7001; ValidatingAdmissionPolicyBinding \($c.policy) generation 1 resourceVersion 7001")))
      and any(.credited[]; .principal == $cd and .right == "get pods/exec in dispatch" and .policy == "broker-pod-connect"
              and .resource == "cluster:operator-cluster" and (.granted_at | startswith("RBAC: allowed by ")))
      and any(.credited[]; .principal == $cd and .granted_at == "RoleBinding dispatch/comment-delivery")
      and ([.residuals[] | {principal, right, resource, granted_at}] == [.credited[] | {principal, right, resource, granted_at}])
      and all(.residuals[]; keys == ["granted_at", "principal", "residual", "resource", "right", "tracked_by"]
              and .residual == $residual and .tracked_by == "INFRA-4604")' "$report" >/dev/null ||
    fail "the fence as reviewed: --report's credited and residuals are not the seven credits and their residuals: $(jq -c '{credited, residuals}' "$report")"
  passed "(fa) the fence as reviewed, declared and live, credits comment-delivery's exec, dispatch-measure's exec and pod patch and legion/worker's port forward on the broker's pods, allows their use on dispatch's other pods as INFRA-4604's residual, and reports each with its evidence"
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
      want="in the renders, the ValidatingAdmissionPolicy broker-pod-connect differs from the reviewed fence in validations"
      ;;
    drift)
      fence_edit live 'if .kind == "ValidatingAdmissionPolicy" and .metadata.name == "broker-pod-update"
        then .spec.matchConstraints.resourceRules |= map(select(.resources != ["pods/ephemeralcontainers"])) else . end'
      want="NO CREDIT live: RBAC on operator-cluster: the broker's pod fence (broker-pod-connect, broker-pod-update, broker-pod-create, broker-workload-create, broker-workload-update in dispatch) is not credited, so what it would refuse stays a finding: in the cluster, the ValidatingAdmissionPolicy broker-pod-update differs from the reviewed fence in matchConstraints"
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
      want="pods of dispatch run as envoy-broker or take a Secret of the broker's under a name the fence's connect rule does not hold (envoy-broker-*): debug-7f9c (runs as dispatch, takes envoy-broker-db)"
      ;;
    other-secret)
      # A Secret the broker's pod takes, as it takes its NATS issuer seed, mounted by Dispatch's pod too.
      edit "$world/clusters/operator-cluster/pods.json" 'map(.spec.volumes = ((.spec.volumes // []) + [{name: "issuer", secret: {secretName: "envoy-broker-nats-issuer"}}]))'
      want="pods of dispatch run as envoy-broker or take a Secret of the broker's under a name the fence's connect rule does not hold (envoy-broker-*): dispatch-6b8f4c-fghij (runs as dispatch, takes envoy-broker-nats-issuer)"
      ;;
    no-pod)
      echo '[]' >"$world/clusters/operator-cluster/pods.json"
      want="dispatch lists no pod named envoy-broker-* that runs as envoy-broker, so its list of pods cannot be told from one that saw nothing"
      ;;
    webhook)
      # A mutating webhook that can match every namespace, backed by a Service in dispatch.
      jq -n '[{apiVersion: "admissionregistration.k8s.io/v1", kind: "MutatingWebhookConfiguration", metadata: {name: "hooks"},
        webhooks: [{name: "inject.example.com", clientConfig: {service: {namespace: "dispatch", name: "hook"}}}]}]' \
        >"$world/clusters/operator-cluster/webhooks.json"
      want="a webhook that can match a guarded namespace calls a Service in dispatch, whose pods the fence does not keep anyone from entering: MutatingWebhookConfiguration hooks/inject.example.com calls the Service dispatch/hook"
      ;;
    webhooks-unread)
      : >"$world/clusters/operator-cluster/refuse-webhooks"
      want="the webhook configurations were not read (above), so whether a webhook's backend is in dispatch, where the fence does not refuse entry into it, is unknown"
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
  each fence_fail_row removed warn ignore selector policy-selector exception drift unread pods-unread rogue-pod other-secret no-pod \
    webhook webhooks-unread shared
  passed "(fb) the fence removed, bound Warn, failing open, missing dispatch in a binding's or a policy's selector, with an exception, differing live from declared, unread, beside a pod outside its names that runs as the broker or takes its database login or another of its Secrets, with no broker pod, beside a webhook backed in dispatch or webhooks unread, or beside another guarded pair in dispatch, credits nothing"
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
  chain_right="create pods/portforward in dispatch, so it can become system:serviceaccount:dispatch:envoy-broker, which can create pods in dispatch"
  want_line "$(credit_line system:serviceaccount:legion:worker "$chain_right" broker-pod-connect)" "envoy-broker with a finding"
  ! has_prefix "ALLOWANCE system:serviceaccount:legion:worker: $chain_right" ||
    fail "envoy-broker with a finding: the chain carries an ALLOWANCE of its own, beside its grant's"
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

# --- (fg) The check's own contract holds the reviewed workload policies by digest, which the
# example ones do not hash to. ---
if row fg; then
  fence_world
  run --broker-workload dispatch/envoy-broker --db-secret dispatch/envoy-broker-db
  want_status 1 "the example fence against the reviewed digests"
  want_fence_findings "the example fence against the reviewed digests"
  want_text "in the renders, the ValidatingAdmissionPolicy broker-workload-create's rules hash to $(jq -r '.["broker-workload-create"]' <<<"$fence_example_digests"), not to the reviewed $(jq -r '.objects[] | select(.name == "broker-workload-create" and .digest) | .digest' "$fence_contract"); the ValidatingAdmissionPolicy broker-workload-update's rules hash to " \
    "the example fence against the reviewed digests"
  passed "(fg) the check itself credits nothing of a fence whose workload policies hash to other digests than its contract's"
fi

# --- (fr) What fence_refuses credits is what the reviewed rules refuse. ---
# fence_rule_cover CONTRACT: for each right on pods in the contract's namespace that fence_refuses
# credits, a record of the verb, the subresource, the policy, whether that policy's committed rules
# hold the operation and resource the API server admits it as (CONNECT for exec, attach and port
# forward; UPDATE for patch and update), and whether every validation of it names the pod prefix.
fence_rule_cover() {
  jq -r -n -L "$(dirname "$fence_check")/operator-token-minters" --slurpfile contract "$1" '
    include "rec"; include "broker-fence";
    $contract[0] as $c
    | {namespace: $c.namespace, account: $c.account, direct: true, residual: $c.residual} as $fence
    | ("create", "get", "patch", "update", "delete", "deletecollection", "list", "watch") as $verb
    | ("", "exec", "attach", "portforward", "ephemeralcontainers", "log", "proxy", "eviction", "binding", "status") as $sub
    | {verb: $verb, group: "", resource: "pods", sub: $sub, ns: $c.namespace, name: ""} as $r
    | fence_refuses($fence; $r) as $policy
    | (if $verb == "create" or $verb == "get" then "CONNECT" else "UPDATE" end) as $op
    | (if $sub == "" then "pods" else "pods/\($sub)" end) as $res
    | ([$c.objects[] | select(.kind == "ValidatingAdmissionPolicy" and .name == $policy) | .spec][0]) as $spec
    | [$verb, $sub, $policy,
       ($spec != null and any($spec.matchConstraints.resourceRules[];
         any(.operations[]; . == $op) and any(.resources[]; . == $res) and any(.apiGroups[]; . == ""))),
       ($spec != null and all($spec.validations[]; .expression | contains("startsWith(\u0027\($c.prefix)\u0027)")))] | @tsv'
}
if row fr; then
  cover="$(fence_rule_cover "$fence_contract")"
  want="$(printf '%s\n' "create	exec	broker-pod-connect	true	true" "create	attach	broker-pod-connect	true	true" \
    "create	portforward	broker-pod-connect	true	true" "get	exec	broker-pod-connect	true	true" "get	attach	broker-pod-connect	true	true" \
    "get	portforward	broker-pod-connect	true	true" "patch		broker-pod-update	true	true" "update		broker-pod-update	true	true" | sort)"
  [ "$(sort <<<"$cover")" = "$want" ] ||
    fail "fence_refuses credits other rights than the reviewed rules refuse on the broker's pods: $cover"
  # The control: the same reading of a contract whose connect policy lacks port forward fails.
  jq '(.objects[] | select(.kind == "ValidatingAdmissionPolicy" and .name == "broker-pod-connect") | .spec.matchConstraints.resourceRules)
    |= map(select(.resources != ["pods/portforward"]))' "$fence_contract" >"$temporary_dir/fence-contract-no-portforward.json"
  grep -qxF "create	portforward	broker-pod-connect	false	true" <<<"$(fence_rule_cover "$temporary_dir/fence-contract-no-portforward.json")" ||
    fail "the reading of a contract whose connect policy lacks port forward still finds port forward covered"
  passed "(fr) each right fence_refuses credits is one the reviewed broker-pod-connect or broker-pod-update rules admit as CONNECT or UPDATE on pods and refuse by the envoy-broker- prefix, and a contract without the port forward rule shows it"
fi

# --- (fh) An ephemeral container in dispatch is no credit: on a pod not the broker's it takes any
# Secret of the namespace. ---
fence_ephemeral_row() {
  fence_world
  subject_grant operator-cluster "$(sa_subject legion worker)" "$1" "" pods ephemeralcontainers dispatch ""
  fence_run
  want_status 1 "legion/worker with $1 pods/ephemeralcontainers in dispatch"
  want_line "FINDING system:serviceaccount:legion:worker: $1 pods/ephemeralcontainers in dispatch" \
    "legion/worker with $1 pods/ephemeralcontainers in dispatch"
  ! has_prefix "CREDIT system:serviceaccount:legion:worker: $1 pods/ephemeralcontainers" ||
    fail "legion/worker with $1 pods/ephemeralcontainers in dispatch: the grant is credited"
  want_fence_credits "legion/worker with $1 pods/ephemeralcontainers in dispatch"
}
if row fh; then
  each fence_ephemeral_row patch update
  passed "(fh) patch or update of pods/ephemeralcontainers in dispatch stays a finding beside the fence's credits"
fi
