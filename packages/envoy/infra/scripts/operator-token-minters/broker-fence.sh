# shellcheck shell=bash
# shellcheck disable=SC2034,SC2153,SC2154 # globals set and read across the sourced files, in operator-token-minters.sh's order
# operator-token-minters/broker-fence.sh: the broker's pod fence, the one admission refusal this check
# credits. Sourced by operator-token-minters.sh, whose header says what each step checks.
#
# RBAC answers whether a principal may make a request; admission can still refuse it. The program
# that declares the cluster's RBAC and admission policies (the one --rbac-render renders) declares
# five ValidatingAdmissionPolicies for the broker's pods in dispatch: broker-pod-connect refuses
# exec, attach and port forward to a pod named envoy-broker-*, broker-pod-update refuses any change
# but metadata to a pod of the broker's and any change of its ephemeral containers,
# broker-pod-create admits a pod running as envoy-broker from the broker's ReplicaSets alone, named
# envoy-broker-*, and broker-workload-create and -update hold who may name the account in a
# workload; each bound Deny with failurePolicy Fail, in dispatch alone, with no exception for any
# operator. broker-fence.json names that fence (its namespace, account, pod prefix and policies) and
# pins it as reviewed by a digest: the sha256 of its five policies and five bindings in canonical
# form (fence_read and fence_digest, broker-fence.jq), read from the program's committed render and
# from the dispatch cluster on 2026-11-23, both of which hash to it. The pin holds the rules
# themselves, an exception added to one included, without this repository holding their text.
#
# The fence is credited only when every part of that holds: the renders declare it as reviewed and
# the operator's cluster holds it as reviewed (each policy and binding once, bound Deny with
# failurePolicy Fail to dispatch alone, and their specs, less what the API server fills in by
# default, hashing to the pin), so the two agree; and the cluster's pods in dispatch keep the
# account's shape: every pod that runs as envoy-broker, or takes --db-secret or a token Secret of the
# namespace, is named envoy-broker-* (a pod made before the fence keeps any name), and at least one
# pod named so runs as envoy-broker (a list that saw nothing proves nothing). A fence the renders or
# the cluster lack, or hold otherwise (a binding of Warn or Audit, a policy of failurePolicy Ignore,
# a selector that misses dispatch, an exception added), credits nothing: what it would refuse stays
# a finding and the step prints NO CREDIT saying why; a fence or a pod list that cannot be read is
# UNKNOWN.
#
# A credited fence removes only the rights it refuses on every pod of the broker's (fence_refuses):
# entry into a pod of dispatch (create and get on pods/exec, attach and portforward) and a change of a
# pod's spec or ephemeral containers there (patch and update on pods and pods/ephemeralcontainers),
# outright or limited by name, live or declared, and in who can become envoy-broker by them; each
# such grant prints a CREDIT line and goes into --report's `credited`, the grant kept as evidence. In
# the guarded rights it removes them only when the broker's pair is the one guarded pair in dispatch:
# entry into another pod there reaches what that pod runs as. Everything else stays a finding: create
# pods in dispatch (a pod of another account there takes any Secret of the namespace, the broker's
# database login among them), every workload write, the token, impersonation, Secrets, the bindings,
# nodes/proxy, every Google IAM step and the hold writers. So does whoever can write the fence: its
# policies' and bindings' create, update and patch are among the admission rights, and their delete
# and deletecollection are asked beside them once the fence is credited (FENCE_WRITE_RIGHTS).

FENCE_CONTRACT="$(jq -c . "$LIB/broker-fence.json")"
IFS=$'\t' read -r FENCE_NS FENCE_ACCOUNT FENCE_PREFIX FENCE_PIN < <(jq -r '[.namespace, .account, .prefix, .digest] | @tsv' <<<"$FENCE_CONTRACT")
FENCE_PAIR="$FENCE_NS/$FENCE_ACCOUNT"
FENCE_POLICIES="$(jq -r '.policies | join(", ")' <<<"$FENCE_CONTRACT")"
# FENCE_GUARDED: whether the fence's pair is guarded, without which it is never read. FENCE_DIRECT:
# whether it is the one guarded pair in its namespace (fence_refuses).
FENCE_GUARDED=false FENCE_DIRECT=true
for pair in "${GUARDED[@]}"; do
  if [ "$pair" = "$FENCE_PAIR" ]; then
    FENCE_GUARDED=true
  elif [ "${pair%%/*}" = "$FENCE_NS" ]; then
    FENCE_DIRECT=false
  fi
done
# FENCE_DECLARED: the renders' fence's digest when they declare it as reviewed, else empty, with
# FENCE_DECLARED_WHY saying why. CREDIT_FENCE: the fence fence_refuses reads ({namespace, account,
# direct}, with what fence_credit reports), set while the operator's cluster is judged and the fence
# is credited, else empty. CREDIT_OF: the label of each right of the reviews being judged that the
# fence refuses, to the policy that refuses it (fence_credits).
FENCE_DECLARED="" FENCE_DECLARED_WHY="the renders were not read" CREDIT_FENCE="" FENCE_LIVE_READ=false
declare -A CREDIT_OF=()
# CODE_PENDING: the code step's grants the declared fence refuses, held until the live step reads
# the cluster's fence (fence_settle); CODE_PASS: the code step's PASS sentence while it waits on them.
CODE_PENDING=() CODE_PASS=""
FENCE_WRITE_RIGHTS="$(jq -n -c '[("validatingadmissionpolicies", "validatingadmissionpolicybindings") as $res
  | ("delete", "deletecollection")
  | {verb: ., group: "admissionregistration.k8s.io", resource: $res, sub: "", ns: "", name: "", why: "which removes the broker'"'"'s pod fence",
     label: "\(.) \($res).admissionregistration.k8s.io cluster-wide, which removes the broker'"'"'s pod fence"}
  | . + {key: ([.verb, .group, .resource, .sub, .ns, .name] | join("|"))}]')"

# no_credit STEP WHY: the fence is not credited, so what it would refuse stays a finding; printed once.
no_credit() {
  local line="NO CREDIT $1: the broker's pod fence ($FENCE_POLICIES in $FENCE_NS) is not credited, so what it would refuse stays a finding: ${2//[$'\t\r\n']/ }"
  [ -z "${RECORDED[N$line]:-}" ] || return 0
  RECORDED[N$line]=1
  echo "$line"
}
# fence_verdict FILE WHERE: fence_read's answer in FILE judged; sets FENCE_DIGEST, the sha256 of the
# canonical policies and bindings, when they are the reviewed fence (FENCE_PIN), else FENCE_WHY,
# naming WHERE.
fence_verdict() {
  local digest
  FENCE_DIGEST="" FENCE_WHY=""
  case "$(jq -r .state "$1")" in
    read)
      digest="sha256:$(jq -c -L "$LIB" 'include "broker-fence"; fence_digest' "$1" | jq -S -c . | sha256sum | cut -d' ' -f1)"
      if [ "$digest" = "$FENCE_PIN" ]; then
        FENCE_DIGEST="$digest"
      else
        FENCE_WHY="in $2, the policies' and bindings' rules hash to $digest, not to the reviewed fence's $FENCE_PIN"
      fi
      ;;
    absent) FENCE_WHY="$2 hold none of its policies and bindings" ;;
    *) FENCE_WHY="$(jq -r --arg where "$2" '"in \($where), " + ([.objects[] | select(.state != "read") | "the \(.kind) \(.name) \(.why)"] | join("; "))' "$1")" ;;
  esac
}
# fence_declared: the renders' fence (the code step reads them; a render that is not JSON is that
# step's UNKNOWN), into FENCE_DECLARED or FENCE_DECLARED_WHY.
fence_declared() {
  [ "$FENCE_GUARDED" = true ] || return 0
  if ! cat -- "${RBAC_RENDERS[@]}" | jq -c -n -L "$LIB" --argjson contract "$FENCE_CONTRACT" \
    'include "broker-fence"; [inputs | objs] | fence_read($contract)' >"$WORK/fence-declared.json" 2>"$WORK/fence-declared.err"; then
    FENCE_DECLARED_WHY="the renders could not be read for it: $(first_line "$WORK/fence-declared.err")"
    return 0
  fi
  fence_verdict "$WORK/fence-declared.json" "the renders"
  FENCE_DECLARED="$FENCE_DIGEST" FENCE_DECLARED_WHY="$FENCE_WHY"
}
# fence_declared_json: the fence code-rbac.jq reads in the code step: the declared one, or null.
fence_declared_json() {
  if [ -n "$FENCE_DECLARED" ]; then
    jq -n -c --arg ns "$FENCE_NS" --arg account "$FENCE_ACCOUNT" --argjson direct "$FENCE_DIRECT" '{namespace: $ns, account: $account, direct: $direct}'
  else
    echo null
  fi
}
# fence_live KUBECONFIG STEP: on the operator's cluster, after admission_rights read its discovery
# and the token Secrets of the guarded namespaces were listed: sets CREDIT_FENCE when the cluster's
# fence is the reviewed one the renders declare and its pods keep the account's shape.
fence_live() {
  local kube="$1" step="$2" resource missing="" outside witness
  CREDIT_FENCE=""
  [ "$FENCE_GUARDED" = true ] || return 0
  FENCE_LIVE_READ=true
  if [ -z "$FENCE_DECLARED" ]; then
    no_credit "$step" "$FENCE_DECLARED_WHY"
    return 0
  fi
  for resource in validatingadmissionpolicies validatingadmissionpolicybindings; do
    grep -qxF -- "$resource.admissionregistration.k8s.io" "$WORK/admission-resources" 2>/dev/null || missing="${missing:+$missing and }$resource"
  done
  if [ -n "$missing" ] && [ -n "$ADMISSION_ERROR" ]; then
    unknown "$step" "the broker's pod fence ($FENCE_POLICIES) is unread, since the API server's discovery of admissionregistration.k8s.io is not whole, so nothing it refuses is credited"
    return 0
  elif [ -n "$missing" ]; then
    no_credit "$step" "the API server serves no $missing"
    return 0
  fi
  if ! kubectl --kubeconfig "$kube" get validatingadmissionpolicies.admissionregistration.k8s.io,validatingadmissionpolicybindings.admissionregistration.k8s.io \
    -o json >"$WORK/fence-objects.json" 2>"$WORK/fence-objects.err"; then
    unknown "$step" "the API server refused the list of admission policies and their bindings (it needs list on validatingadmissionpolicies and validatingadmissionpolicybindings), so the broker's pod fence ($FENCE_POLICIES) is unread and nothing it refuses is credited: $(first_line "$WORK/fence-objects.err")"
    return 0
  fi
  if ! jq -c -L "$LIB" --argjson contract "$FENCE_CONTRACT" 'include "broker-fence"; [objs] | fence_read($contract)' \
    "$WORK/fence-objects.json" >"$WORK/fence-live.json" 2>"$WORK/fence-live.err"; then
    unknown "$step" "the API server's list of admission policies is not JSON, so the broker's pod fence is unread and nothing it refuses is credited: $(first_line "$WORK/fence-live.err")"
    return 0
  fi
  fence_verdict "$WORK/fence-live.json" "the cluster"
  if [ -z "$FENCE_DIGEST" ]; then
    no_credit "$step" "$FENCE_WHY"
    return 0
  fi
  # Each of the renders' and the cluster's objects equals the contract's, so the two agree.
  if ! kubectl --kubeconfig "$kube" get pods -n "$FENCE_NS" -o json >"$WORK/fence-pods.json" 2>"$WORK/fence-pods.err"; then
    unknown "$step" "the API server refused the list of pods in $FENCE_NS (it needs list on pods there, container.pods.list on GKE), so whether a pod outside the broker's pod fence runs as $FENCE_ACCOUNT or takes its Secrets is unread and nothing the fence refuses is credited: $(first_line "$WORK/fence-pods.err")"
    return 0
  fi
  # The Secrets that hold the broker's power: --db-secret when it is in the fence's namespace, and
  # each token Secret listed there. The unit separator, not a tab, parts the answer: read merges a
  # run of tabs, and the first field is empty when every pod keeps the account's shape.
  if ! IFS="$US" read -r outside witness < <(jq -r --arg prefix "$FENCE_PREFIX" --arg account "$FENCE_ACCOUNT" \
    --argjson secrets "$({ [ "${DB_SECRET%%/*}" != "$FENCE_NS" ] || printf '%s\n' "${DB_SECRET#*/}"
      awk -F'\t' -v ns="$FENCE_NS" '$1 == ns { print $2 }' "$WORK/token-secrets.tsv"; } | jq -R -s -c 'split("\n") | map(select(length > 0))')" '
      [.items[]? | {name: .metadata.name, account: (.spec.serviceAccountName // .spec.serviceAccount // "default"),
        takes: ([(.spec.volumes // [])[] | (.secret.secretName // empty), ((.projected.sources // [])[] | .secret.name // empty)]
          + [((.spec.containers // []) + (.spec.initContainers // []) + (.spec.ephemeralContainers // []))[]
             | ((.env // [])[] | .valueFrom.secretKeyRef.name // empty), ((.envFrom // [])[] | .secretRef.name // empty)]
          | map(select(. as $s | any($secrets[]; . == $s))) | unique)}] as $pods
      | [([$pods[] | select((.name | startswith($prefix) | not) and (.account == $account or (.takes | length) > 0))
           | "\(.name) (runs as \(.account)\(if (.takes | length) > 0 then ", takes \(.takes | join(", "))" else "" end))"] | join("; ")),
         ([$pods[] | select((.name | startswith($prefix)) and .account == $account)] | length | tostring)] | join("\u001f")' \
    "$WORK/fence-pods.json" 2>"$WORK/fence-pods.err"); then
    unknown "$step" "the API server's list of pods in $FENCE_NS is not JSON, so nothing the broker's pod fence refuses is credited: $(first_line "$WORK/fence-pods.err")"
    return 0
  fi
  if [ -n "$outside" ]; then
    no_credit "$step" "pods of $FENCE_NS run as $FENCE_ACCOUNT or take its Secrets under a name the fence's connect rule does not hold ($FENCE_PREFIX*): $outside"
    return 0
  fi
  if [ "$witness" -eq 0 ]; then
    no_credit "$step" "$FENCE_NS lists no pod named $FENCE_PREFIX* that runs as $FENCE_ACCOUNT, so its list of pods cannot be told from one that saw nothing"
    return 0
  fi
  CREDIT_FENCE="$(jq -c --arg ns "$FENCE_NS" --arg account "$FENCE_ACCOUNT" --argjson direct "$FENCE_DIRECT" \
    --arg declared "$FENCE_DECLARED" --arg live "$FENCE_DIGEST" '{namespace: $ns, account: $account, direct: $direct, declared: $declared, live: $live,
      versions: ([.objects[] | {name, v: "\(.kind) \(.name) generation \(.generation) resourceVersion \(.resourceVersion)"}]
        | group_by(.name) | map({(.[0].name): (map(.v) | join("; "))}) | add)}' "$WORK/fence-live.json")"
}
# fence_credits RIGHTS: CREDIT_OF, from the JSON array RIGHTS, while the fence is credited.
fence_credits() {
  local label policy
  CREDIT_OF=()
  [ -n "$CREDIT_FENCE" ] || return 0
  while IFS=$'\t' read -r label policy; do
    CREDIT_OF[$label]="$policy"
  done < <(jq -r -L "$LIB" --argjson fence "$CREDIT_FENCE" 'include "broker-fence"; .[] | fence_refuses($fence; .) as $p | [.label, $p] | @tsv' <<<"$1")
}
# fence_credit PRINCIPAL RIGHT RESOURCE GRANTED_AT POLICY: one grant the credited fence's POLICY
# refuses, with the declared and live versions of the policy and its binding of that name.
fence_credit() {
  credit "$1" "$2" "$3" "$4" "$5" "$5" "$(jq -r .declared <<<"$CREDIT_FENCE")" \
    "$(jq -r --arg p "$5" '"\(.live); \(.versions[$p])"' <<<"$CREDIT_FENCE")"
}
# fence_settle: the code step's held grants, once the live step has read the cluster's fence:
# credited when it is, else findings; then the code step's PASS, when it waited on them alone.
fence_settle() {
  local entry principal right detail id policy
  [ "${#CODE_PENDING[@]}" -gt 0 ] || return 0
  [ -n "$CREDIT_FENCE" ] || [ "$FENCE_LIVE_READ" = true ] ||
    no_credit "code: RBAC" "the operator's cluster's fence was not read (live: RBAC)"
  for entry in "${CODE_PENDING[@]}"; do
    IFS="$US" read -r principal detail id policy <<<"$entry"
    if [ -n "$CREDIT_FENCE" ]; then
      fence_credit "$principal" "$detail, declared by $id" "cluster:$cluster_name" "$id" "$policy"
    else
      finding "$principal" "$detail, declared by $id" "$detail" "cluster:$cluster_name" "$detail" "$id"
    fi
  done
  [ -z "$CODE_PASS" ] || [ -z "$CREDIT_FENCE" ] ||
    pass "code: RBAC" "$CODE_PASS, beside ${#CODE_PENDING[@]} grants the broker's pod fence refuses, declared and live alike"
  CODE_PENDING=() CODE_PASS=""
}
