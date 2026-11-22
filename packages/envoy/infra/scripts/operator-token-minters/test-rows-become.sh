# shellcheck shell=bash
# shellcheck disable=SC2016,SC2034,SC2154 # the world, its names, its helpers and the output and status a run sets are the test's; each single-quoted $name is a jq variable, not the shell's
# operator-token-minters/test-rows-become.sh: the rows of operator-token-minters.test.sh on who can
# become a principal with a finding through a right a live role limits by name. Sourced by the test
# after its world and helpers; its header says what the fakes model. The rows:
#
#   (ck) a system namespace's ServiceAccount allowed a guarded right by name alone is followed, never
#        reported itself: kube-system/x that may patch the Deployment op in envoy-operator (rv18),
#        gke-foo/agent that may update the webhook configuration existing-webhook, and gke-foo/agent
#        that may patch the Deployment secrets-broker in secrets-broker, each beside legion/worker,
#        which may create pods in that namespace: each exits 1 naming legion/worker's chain;
#   (cl) a copy of the script whose by-name read of who can become a found ServiceAccount fails
#        reads UNKNOWN naming the failure, not clean.

# --- (ck) A system namespace's ServiceAccount allowed a guarded right by name: who can become it holds
# that right, so it seeds the chain as one allowed the right outright does. ---
named_seed_row() {
  local ns sa verb group resource name rns want
  case "$1" in
    rv18) ns=kube-system sa=x verb=patch group=apps resource=deployments name=op rns=envoy-operator
      want="patch deployments.apps op in envoy-operator" ;;
    webhook) ns=gke-foo sa=agent verb=update group=admissionregistration.k8s.io resource=mutatingwebhookconfigurations name=existing-webhook rns=""
      want="update mutatingwebhookconfigurations.admissionregistration.k8s.io existing-webhook cluster-wide" ;;
    deployment) ns=gke-foo sa=agent verb=patch group=apps resource=deployments name=secrets-broker rns=secrets-broker
      want="patch deployments.apps secrets-broker in secrets-broker" ;;
  esac
  base_world
  add_sa operator-cluster "$ns" "$sa"
  subject_grant operator-cluster "$(sa_subject "$ns" "$sa")" "$verb" "$group" "$resource" "" "$rns" "$name"
  subject_grant operator-cluster "$(sa_subject legion worker)" create "" pods "" "$ns" ""
  run
  want_status 1 "$ns/$sa allowed $want by name, and legion/worker may create pods in $ns"
  want_text "FINDING system:serviceaccount:legion:worker: create pods in $ns, so it can become system:serviceaccount:$ns:$sa, which can $want" \
    "$ns/$sa allowed $want by name, and legion/worker may create pods in $ns"
  [ "$(count_prefix "FINDING system:serviceaccount:$ns:")" -eq 0 ] || fail "$ns/$sa, a system namespace's ServiceAccount, is itself a finding"
}
if row ck; then
  each named_seed_row rv18 webhook deployment
  passed "(ck) a system namespace's ServiceAccount allowed a guarded right by name alone seeds the chain, unreported itself, so who can become it exits 1"
fi

# --- (cl) The by-name read of who can become a found ServiceAccount fails: a copy whose code-rbac.jq
# breaks on a become right must read UNKNOWN, never clean. ---
if row cl; then
  copy="$temporary_dir/copy-broken-become"
  base_world
  subject_grant operator-cluster "$(sa_subject kube-system generic-garbage-collector)" create "" serviceaccounts token envoy-operator envoy-operator
  rm -rf "$copy"
  mkdir -p "$copy"
  cp "$check" "$copy/operator-token-minters.sh"
  cp -r "$(dirname "$check")/operator-token-minters" "$copy/"
  # A become right carries `targets`, which no other right the jq program is given does.
  sed -i 's/^\(        \)(\$rights | map(\.key)) as \$plain$/\1if any($named_rights[]; has("targets")) then error("a read broken by the test") else . end\n\1| ($rights | map(.key)) as $plain/' \
    "$copy/operator-token-minters/code-rbac.jq"
  grep -q 'a read broken by the test' "$copy/operator-token-minters/code-rbac.jq" || fail "the test could not break the copy's code-rbac.jq"
  mapfile -t args < <(full_args)
  status=0
  output="$(env PATH="$fake_bin:$PATH" OPERATOR_MINTERS_WORLD="$world" TMPDIR="$temporary_dir" bash "$copy/operator-token-minters.sh" "${args[@]}" 2>&1)" ||
    status=$?
  want_status 2 "a by-name read of who can become a found ServiceAccount that fails"
  want_text "UNKNOWN live: RBAC on operator-cluster: the live roles could not be read for who can become a found ServiceAccount by a right limited by name: " \
    "a by-name read of who can become a found ServiceAccount that fails"
  passed "(cl) a by-name read of who can become a found ServiceAccount that fails reads UNKNOWN"
fi
