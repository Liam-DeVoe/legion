#!/usr/bin/env bash
# packages/envoy/infra/scripts/operator-token-minters.sh
#
# Who, other than a person, can come to hold the NATS auth service's operator identity.
#
# The broker's `operator` entry of BROKER_NATS_SERVICES names one Kubernetes ServiceAccount
# (envoy-operator/envoy-operator by default). NATS takes that account's token, and the account acts,
# through Workload Identity, as one Google service account, whose token reaches an agent only as the
# administrator-tagged broker secret ENVOY_OPERATOR, released when a person approves; the secrets
# broker mints that token, so the broker's own ServiceAccount (--broker-workload) is guarded as the
# operator's is. So holding the operator's power takes one of: minting a guarded ServiceAccount's
# token, running or entering a pod that runs as it, becoming a principal that can, minting the Google
# service account's token, making a namespace and ServiceAccount of the same names on another
# cluster of the project's workload pool, or registering a cluster to the pool's fleet. This check
# lists every principal that is not a person and holds such a right, and is the precondition checked
# before the operator entry is applied, and again before go-live.
#
# It runs with the gcloud, kubectl and agent-secrets sign-ins of the shell it runs in: an identity
# already held, never a person's administrator credential. It requests no credential, impersonates
# no one, and writes nothing but its own work directory and --report.
#
# The steps, in order; each prints PASS, a FINDING per principal and right, or UNKNOWN naming why:
#
#   permission names  Each permission name the IAM steps ask about is in the predefined role that
#                     grants it (`gcloud iam roles describe`): every container.* name in
#                     roles/container.admin's includedPermissions, the Google service account ones
#                     in roles/iam.serviceAccountTokenCreator, ...KeyAdmin and ...User, the hold
#                     writers' in roles/cloudsql.admin, roles/secretmanager.secretAccessor and
#                     roles/storage.objectViewer, and the fleet's in roles/gkehub.admin. A name the
#                     role does not hold would find no holder, which reads as clean, so it fails the
#                     run instead.
#   code: RBAC        What code declares, first: each --rbac-render file (the JSON render of the
#                     program that declares the cluster's Roles and bindings, a List, an array or a
#                     stream of objects) is read as the RBAC authorizer reads it, and each binding
#                     that gives a right below to a subject that is not a person, a system
#                     namespace's, a system component or the operator's own Google service account
#                     is a finding. A binding to a role the render does not hold (other than the
#                     built-in cluster-admin, which gives every right, and view, which gives none)
#                     or to an aggregated ClusterRole is UNKNOWN: its rights are not in the render.
#   code: IAM         Each --iam-preview file (`pulumi preview --json --show-sames` of a program that
#                     declares IAM) is read for its organization, folder, project, service account,
#                     secret and bucket bindings, judged as the live IAM steps judge them, with each
#                     role's permissions from the preview's own custom roles or `gcloud iam roles
#                     describe`. A binding the preview does not know all of (a value computed at
#                     apply) and a resource type there that can grant on those resources but that
#                     this check does not read are UNKNOWN. A group the code names is judged by its
#                     members in the live steps, which expand it.
#   live: clusters    `gcloud container clusters list` of --project, which must hold --cluster. The
#                     operator's cluster is --cluster in --location, or, without --location, the
#                     list's one cluster of that name; a name the list holds more than once with no
#                     --location is UNKNOWN, and no cluster of that name is read as the operator's or
#                     as another. A list gcloud says is incomplete (a zone that did not respond) is
#                     UNKNOWN: a cluster there is neither read nor counted.
#   live: RBAC        On the operator's cluster, one SubjectAccessReview per principal and right,
#                     the principals being every ServiceAccount outside the system namespaces
#                     (kube-system, gke-*, gmp-*), as on every other cluster of the pool, with the
#                     groups the API server gives it (system:serviceaccounts,
#                     system:serviceaccounts:<namespace>, system:authenticated), the groups of the
#                     agent namespaces (--agent-namespace) each alone, the Google service account
#                     each is bound to (its iam.gke.io/gcp-service-account annotation) with
#                     system:authenticated, the cluster's binding subjects (below), and anyone
#                     unauthenticated (system:anonymous with system:unauthenticated), always. The
#                     rights are the list below for each guarded pair, and create on each custom
#                     resource the API server's discovery (`kubectl api-resources --namespaced
#                     --verbs=create`) lists, in each guarded pair's namespace: a custom resource's
#                     controller may make a pod from it, as an agent sandbox does; a discovery that
#                     cannot be read whole is UNKNOWN. A review without the groups would miss every
#                     grant made to a group. Its witness is a review for the group the built-in
#                     cluster-admin binding names, system:masters, which must be allowed: an API
#                     server whose answers cannot say yes proves nothing by saying no. Each
#                     --agent-namespace pattern that matches no namespace holding a ServiceAccount
#                     is UNKNOWN, so a mistyped one cannot drop its agents in silence. Then who can
#                     become a ServiceAccount with a finding, in its namespace (create pods, create
#                     or get pods/exec, get, list or watch secrets, and by its name create
#                     serviceaccounts/token or impersonate), is a finding naming the chain, followed
#                     in turn up to 5 steps.
#   live: GKE IAM     GKE authorizes a Google identity's Kubernetes call through IAM beside RBAC, so
#                     Policy Analyzer's holders of the container.* permission of each right, on the
#                     project, and of resourcemanager.projects.setIamPolicy and iam.roles.update
#                     there, by which a holder grants itself any of them.
#   live: minters     Policy Analyzer's holders, on --gsa or above it, of
#                     iam.serviceAccounts.getAccessToken, getOpenIdToken, signJwt, signBlob, actAs,
#                     implicitDelegation, iam.serviceAccountKeys.create and setIamPolicy. They must
#                     be people and the Workload Identity member serviceAccount:<pool>.svc.id.goog[
#                     <namespace>/<service account>], which is the witness: the answer must list it.
#                     The broker needs roles/iam.serviceAccountTokenCreator on that account to mint
#                     the secret ENVOY_OPERATOR's token, so --broker-account allows that one binding,
#                     on that account and in that role alone, and then the broker account's own
#                     minters are checked the same way, its Workload Identity member
#                     (--broker-workload) the witness and the one non-person allowed.
#   impersonation     Every IAM step analyses with --analyze-service-account-impersonation: who can
#                     mint a holder's token, sign as it or act as it holds what the holder holds, so
#                     the outermost principal of each such chain, through any number of service
#                     accounts, is judged as a holder, its finding written as the chain
#                     (serviceAccount:a -> impersonates serviceAccount:b (<role>) -> <b's role>).
#                     The design's own links are no finding: a Workload Identity member acting as
#                     the account it is bound to, and the broker minting --gsa's token.
#   live: Workload    The Workload Identity member names the workload pool (<pool>.svc.id.goog,
#     Identity        --pool-project, by default --project), the namespace and the ServiceAccount,
#                     never the cluster, so every cluster that uses the pool can act as the Google
#                     service account a pair is bound to. The guarded pairs are the operator's
#                     (--namespace/--service-account), the broker's (--broker-workload) and each
#                     --guarded <namespace>/<service account>, such as a route password reader
#                     reached the same way. On every
#                     cluster of the pool (the operator's, every other cluster of the project, and
#                     every member cluster of the pool project's fleet) no principal the step
#                     reviews may create a namespace cluster-wide, or a ServiceAccount or a pod in a
#                     guarded pair's namespace; each finding's right names the pairs it guards. On
#                     every cluster but the operator's, a guarded pair's namespace must not exist
#                     either, and the principals are that cluster's ServiceAccounts outside its
#                     system namespaces (kube-system, gke-*, gmp-*), with their groups, and its
#                     binding subjects (below); on the operator's cluster they are the RBAC step's.
#                     A project with the operator's cluster alone says so. The fleet's memberships
#                     are read from the GKE Hub API (projects/<pool>/locations/-/memberships, every
#                     page) with the running identity's access token, not through `gcloud container
#                     fleet memberships list`, which drops the answer's `unreachable` locations: a
#                     listing that could not reach a location reads complete there. A refused
#                     listing, and one with an unreachable location, is UNKNOWN; a fleet the API
#                     answers with no membership says so. A member that is a GKE cluster is read
#                     through `gcloud container clusters get-credentials` in its own project, any
#                     other through `gcloud container fleet memberships get-credentials` (the
#                     Connect gateway); the operator's cluster, and a cluster the project step read,
#                     are not read twice. GKE authorizes IAM beside RBAC, so for each GKE member in
#                     a project other than --project, Policy Analyzer's holders of
#                     container.namespaces.create, container.serviceAccounts.create and
#                     container.pods.create on that project are judged as the GKE IAM step judges
#                     its own (--project's are in that step).
#   live: fleet       Policy Analyzer's holders of gkehub.memberships.create and update on the pool
#     registration    project, each judged as in the GKE IAM step: a cluster registered to the
#                     pool's fleet shares the pool's Workload Identity, so its ServiceAccount
#                     envoy-operator/envoy-operator is the operator's member there.
#   live: IAM on      For each folder and organization above --project and the pool project (`gcloud
#     ancestors       projects get-ancestors`), Policy Analyzer's holders of its setIamPolicy, and of
#                     the organization's iam.roles.update: each can grant itself any role below it.
#   live: hold        Who can write the writer hold's tables beside the broker's routes, each holder a
#     writers         finding: the holders of cloudsql.users.create or update on --sql-instance, of
#                     get on the Secret --db-secret (a SubjectAccessReview for each principal of the
#                     RBAC step, and container.secrets.get through IAM), of storage.objects.get on the
#                     bucket of --broker-state, and of secretmanager.versions.access or setIamPolicy
#                     on --signing-secret, which the operator's Google service account alone may read
#                     and must be listed on.
#   admin tag         `agent-secrets secret show <--secret> --json`, under the running session, must
#                     show "admin": "true" and "tier": "human": the operator's credential is an
#                     administrator's, released only when a person approves and never decided by a
#                     model judge. No such secret is a finding too.
#
# Binding subjects. The Google identities a cluster's RBAC names are principals of that cluster
# too: GKE authenticates a Google account as a User of its email, so `grade-ci@<p>.iam.
# gserviceaccount.com` bound to cluster-admin on a member of the pool's fleet can make the
# operator's namespace and ServiceAccount there. On the operator's cluster and on each other
# cluster of the pool, every User subject of its ClusterRoleBindings and RoleBindings
# (`kubectl get clusterrolebindings,rolebindings -A`) that is not a person is reviewed with
# system:authenticated (system:anonymous with system:unauthenticated): a Google service account,
# or any User the RBAC rule below does not allow, can be a finding. A person's email User subject
# is never reviewed, so never a finding, as iam_verdict never makes a `user:` member one and the
# accepted list names no person. A ServiceAccount subject is left out: the cluster's own
# ServiceAccounts are reviewed with their groups already, and one named but not made does not exist
# to act. A refused list of bindings is UNKNOWN, naming the permission.
#
# Group subjects. A Group other than Kubernetes' own system groups is never reviewed, so never a
# finding, in the code step as in the live steps. That holds while GKE's Google Groups for RBAC is
# off on the cluster: the API server then gives no request a Google group, so such a Group names no
# one who can act. The system groups that hold ServiceAccounts or anyone (system:serviceaccounts,
# system:serviceaccounts:<namespace>, system:authenticated, system:unauthenticated) are reviewed,
# each alone. When Google Groups for RBAC is turned on (a cluster's --security-group), a Group
# subject naming a Google group's email gives its members its rights. Then change this step: list
# each such group's members (`gcloud identity groups memberships list --group-email`, through
# nested groups), review each member that is not a person as a User of its email, and make an
# unreadable group UNKNOWN, as Policy Analyzer's expanded groups are judged by their members.
#
# In every IAM step a `user:` member is a person and allowed; a group the analysis expands is judged
# by its members, and one it does not expand is a finding; Google's own service agents
# (service-<number>@gcp-sa-*.iam.gserviceaccount.com, ...@container-engine-robot and
# ...@compute-system) are allowed; every other member is a finding. A Policy Analyzer answer that
# lists no holder, or that was not fully explored, is UNKNOWN: a resource always has holders (its
# project's owners at least), so an answer listing none saw nothing. The analysis runs over the
# project's top ancestor (`gcloud projects get-ancestors`), or --analysis-scope: a scope at the
# project does not see a grant made on its folder or its organization.
#
# The rights, in each guarded pair's namespace unless cluster-wide: create serviceaccounts/token of
# the pair's ServiceAccount; impersonate that ServiceAccount, and users and groups cluster-wide;
# escalate and bind roles and clusterroles, create and patch rolebindings and clusterrolebindings;
# create, and get (which authorizes an exec, attach or port forward over WebSocket on API servers
# before 1.35), pods/exec, pods/attach and pods/portforward; create, patch and update pods, and patch
# and update pods/ephemeralcontainers; create, patch and update replicationcontrollers, deployments,
# replicasets, statefulsets, daemonsets, jobs and cronjobs, each of which runs a pod as the
# ServiceAccount; create and get nodes/proxy cluster-wide, whose kubelet API enters any pod on the
# node; get, list and watch secrets.
#
# Exit 0: no finding and no UNKNOWN. Exit 1: at least one finding, each line naming the principal
# and the right (UNKNOWN lines may stand beside them). Exit 2: no finding, and at least one UNKNOWN,
# each naming its step and why, such as the permission the running identity lacks; also a usage
# error, and a run that stops before its end (a command failing under set -e), which prints the
# UNKNOWN `run` and writes it to --report. --report is deleted when the run starts, so a report is
# this run's or none. --report <file> writes {"run_at": "<RFC 3339>", "findings": [{"principal",
# "right", "role", "resource", "permissions", "granted_at"}], "unknown": [{"step", "reason"}]} beside
# the text, run_at being when the run began. `right` is the sentence the text prints; the rest are
# its parts, for a reader that matches a finding against an accepted entry by principal and role:
#   role         the IAM role (roles/container.admin), for an impersonation chain the role of its
#                outermost link; for an RBAC finding the right's label (create
#                serviceaccounts/token envoy-operator in envoy-operator); for the namespace and the
#                secret's tags, what is wrong (namespace envoy-operator, admin, tier, secret);
#   resource     what the right is on: the analysed resource's full name, `cluster:<name>` (the
#                operator's cluster; `<project>/<location>/<name>` for every other) or
#                `membership:<pool>/<location>/<id>`, or `secret:<name>`;
#   permissions  the IAM permissions the role grants among those asked, for a chain the holder's;
#                for an RBAC finding the right's label; empty for the namespace and the secret's tags;
#   granted_at   the resource the IAM binding is made on, for a chain its outermost link's; for an
#                RBAC finding the API server's reason (`RBAC: allowed by ClusterRoleBinding "x" of
#                ...`) or, from code, the binding; empty where there is none.
#
# The fixture testdata/operator-token-minters-report.json, beside this script, is this script's own
# --report from a world of operator-token-minters.test.sh's, with run_at fixed: an IAM finding, RBAC
# findings of a Google service account bound on a fleet member for each of three guarded pairs (the
# operator's, the broker's and a --guarded one), and an
# UNKNOWN. The writer hold's preflight, which reads --report, tests its reader against it, and the
# test fails when the file differs from a fresh run (OPERATOR_MINTERS_WRITE_FIXTURE=1 rewrites it).
#
#   bash packages/envoy/infra/scripts/operator-token-minters.sh --project <id> --cluster <name> \
#     --gsa <email> --agent-namespace <ns|glob>... --broker-account <email> \
#     --broker-workload <ns>/<sa> --sql-instance <name> --db-secret <ns>/<name> \
#     --broker-state gs://<bucket>[/<path>] --signing-secret <name> \
#     --rbac-render <file>... --iam-preview <file>... [--guarded <ns>/<sa>]... [--report <file>]
#
# The controls run against scratch targets: --namespace envoy-operator-control --gsa
# envoy-operator-control@<project>.iam.gserviceaccount.com --secret ENVOY_OPERATOR_CONTROL.
# operator-token-minters.test.sh runs every plant and control against a fake kubectl, gcloud and
# agent-secrets. Needs bash 4.4, jq, and for the live steps gcloud, kubectl, curl and agent-secrets.
set -euo pipefail

usage() {
  cat <<'USAGE'
usage: operator-token-minters.sh [flags]
  --project ID               the project that runs the operator's cluster
  --cluster NAME             the operator's cluster, one of the project's
  --location LOCATION        the operator's cluster's location (a region or a zone); needed when the
                             project holds more than one cluster named --cluster
  --pool-project ID          the project whose workload pool (ID.svc.id.goog) the operator's and
                             the broker's ServiceAccounts are in, and whose fleet's member clusters
                             share it (default --project)
  --namespace NS             the operator's namespace (default envoy-operator)
  --service-account NAME     the operator's Kubernetes ServiceAccount (default envoy-operator)
  --guarded NS/NAME          one more namespace and ServiceAccount the pool's Workload Identity binds
                             to a Google service account, guarded as the operator's pair is, which is
                             always guarded, as the broker's (--broker-workload) is when it is named;
                             repeatable
  --gsa EMAIL                the Google service account the operator's ServiceAccount acts as
  --agent-namespace NS|GLOB  a namespace whose ServiceAccounts agents run as; repeatable. Every
                             ServiceAccount outside the system namespaces is reviewed; these name
                             the groups reviewed alone, and one must hold a ServiceAccount
  --broker-account EMAIL     the secrets broker's Google service account, which mints the
                             operator's token for the secret ENVOY_OPERATOR
  --broker-workload NS/NAME  the broker's Kubernetes ServiceAccount, bound to --broker-account
  --sql-instance NAME        the Cloud SQL instance of the broker's database, in --project
  --db-secret NS/NAME        the Secret holding the broker's database login
  --broker-state gs://BUCKET[/PATH]  where the broker program's Pulumi state is kept
  --signing-secret NAME      the Secret Manager secret of the writer hold's signing key
  --secret NAME              the broker secret of the operator's credential (default ENVOY_OPERATOR)
  --rbac-render FILE         a JSON render of the cluster's RBAC program; repeatable
  --iam-preview FILE         `pulumi preview --json --show-sames` of an IAM program; repeatable
  --analysis-scope organizations/N|folders/N|projects/ID
                             Policy Analyzer's scope (default: the project's top ancestor)
  --credentials-arg ARG      passed to each `gcloud container clusters get-credentials`; repeatable
  --report FILE              also write the result as JSON
USAGE
}
usage_error() {
  echo "operator-token-minters: $1" >&2
  usage >&2
  exit 2
}

# A run's report is this run's or none: an older one would be read as this run's if the run stopped
# before writing its own, so it goes before anything, a usage error included, can stop the run.
for ((arg = 1; arg <= $#; arg++)); do
  case "${!arg}" in
    --report)
      next=$((arg + 1))
      [ "$next" -gt "$#" ] || rm -f -- "${!next}"
      ;;
    --report=*) report_arg="${!arg}" && rm -f -- "${report_arg#--report=}" ;;
  esac
done
PROJECT="" CLUSTER="" LOCATION="" POOL_PROJECT="" NAMESPACE=envoy-operator KSA=envoy-operator GSA="" BROKER="" BROKER_WORKLOAD=""
SQL_INSTANCE="" DB_SECRET="" BROKER_STATE="" SIGNING_SECRET="" SECRET=ENVOY_OPERATOR ANALYSIS_SCOPE="" REPORT=""
AGENT_NAMESPACES=() RBAC_RENDERS=() IAM_PREVIEWS=() CREDENTIALS_ARGS=() GUARDED_FLAGS=()
while [ "$#" -gt 0 ]; do
  flag="$1"
  shift
  case "$flag" in
    -h | --help) usage; exit 0 ;;
    --*=*) set -- "${flag#*=}" "$@"; flag="${flag%%=*}" ;;
  esac
  case "$flag" in
    --project | --cluster | --location | --pool-project | --namespace | --service-account | --gsa | --agent-namespace | --broker-account | \
      --broker-workload | --sql-instance | --db-secret | --broker-state | --signing-secret | --secret | \
      --rbac-render | --iam-preview | --analysis-scope | --credentials-arg | --report | --guarded)
      [ "$#" -gt 0 ] || usage_error "$flag needs a value"
      value="$1"
      shift
      ;;
    *) usage_error "unknown argument $flag" ;;
  esac
  case "$flag" in
    --project) PROJECT="$value" ;;
    --cluster) CLUSTER="$value" ;;
    --location) LOCATION="$value" ;;
    --pool-project) POOL_PROJECT="$value" ;;
    --namespace) NAMESPACE="$value" ;;
    --service-account) KSA="$value" ;;
    --gsa) GSA="$value" ;;
    --agent-namespace) AGENT_NAMESPACES+=("$value") ;;
    --broker-account) BROKER="$value" ;;
    --broker-workload) BROKER_WORKLOAD="$value" ;;
    --sql-instance) SQL_INSTANCE="$value" ;;
    --db-secret) DB_SECRET="$value" ;;
    --broker-state) BROKER_STATE="$value" ;;
    --signing-secret) SIGNING_SECRET="$value" ;;
    --secret) SECRET="$value" ;;
    --rbac-render) RBAC_RENDERS+=("$value") ;;
    --iam-preview) IAM_PREVIEWS+=("$value") ;;
    --analysis-scope) ANALYSIS_SCOPE="$value" ;;
    --credentials-arg) CREDENTIALS_ARGS+=("$value") ;;
    --report) REPORT="$value" ;;
    --guarded) GUARDED_FLAGS+=("$value") ;;
  esac
done
[ -n "$POOL_PROJECT" ] || POOL_PROJECT="$PROJECT"
# The guarded pairs: the operator's first, then the broker's (--broker-workload), then each
# --guarded, each once.
GUARDED=("$NAMESPACE/$KSA")
pair_name='^[a-z0-9]([-a-z0-9]*[a-z0-9])?/[a-z0-9]([-.a-z0-9]*[a-z0-9])?$'
if [ -n "$BROKER_WORKLOAD" ]; then
  [[ "$BROKER_WORKLOAD" =~ $pair_name ]] || usage_error "--broker-workload is <namespace>/<name>, each a Kubernetes name, not $BROKER_WORKLOAD"
  [ "$BROKER_WORKLOAD" = "${GUARDED[0]}" ] || GUARDED+=("$BROKER_WORKLOAD")
fi
for pair in ${GUARDED_FLAGS[@]+"${GUARDED_FLAGS[@]}"}; do
  [[ "$pair" =~ $pair_name ]] || usage_error "--guarded is <namespace>/<serviceaccount>, each a Kubernetes name, not $pair"
  case " ${GUARDED[*]} " in *" $pair "*) ;; *) GUARDED+=("$pair") ;; esac
done
case "$DB_SECRET" in "" | */*) ;; *) usage_error "--db-secret is <namespace>/<name>, not $DB_SECRET" ;; esac
case "$BROKER_STATE" in "" | gs://?*) ;; *) usage_error "--broker-state is gs://<bucket>[/<path>], not $BROKER_STATE" ;; esac
case "$ANALYSIS_SCOPE" in "" | organizations/?* | folders/?* | projects/?*) ;; *) usage_error "--analysis-scope is organizations/N, folders/N or projects/ID" ;; esac
command -v jq >/dev/null || usage_error "jq is not on PATH"

RUN_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/operator-token-minters.XXXXXX")"
# on_exit, the EXIT trap: a run that stops before its end (a command that failed under set -e)
# reads as UNKNOWN, never as clean or as a finding. crash_report, below, prints the UNKNOWN naming
# that, writes --report with it, and the run exits 2. The work directory goes either way.
FINISHED=false
on_exit() {
  local status=$?
  set +e
  if [ "$FINISHED" != true ] && declare -F crash_report >/dev/null; then
    crash_report "$status"
    status=2
  fi
  rm -rf "$WORK"
  exit "$status"
}
trap on_exit EXIT
mkdir -p "$WORK/kube" "$WORK/roles"

# --- What a run prints and reports. Each finding and each UNKNOWN is printed once, its fields with
# tabs and line breaks turned to spaces, the form every report field takes, so a refusal's
# multi-line text stays one line and one field. ---

FINDINGS=() UNKNOWNS=()
declare -A RECORDED=()
first_line() { sed -n '1{s/[[:cntrl:]]/ /g;p;}' "$1"; }
# last_line: a command's refusal where it prints lines before it, as agent-secrets prints the
# sign-in it uses before the failure.
last_line() { tail -n 1 "$1" | sed 's/[[:cntrl:]]/ /g'; }
# finding PRINCIPAL RIGHT ROLE RESOURCE PERMISSIONS GRANTED_AT: one finding, printed as PRINCIPAL and
# RIGHT, the sentence; the report carries the four parts beside it (the header says each), with
# PERMISSIONS separated by the unit separator ($US), never a comma: an RBAC label can hold one
# ("create pods in ns, guarding ns/a, ns/b"). Two findings of one principal and one sentence are one.
finding() {
  local principal="${1//[$'\t\r\n']/ }" right="${2//[$'\t\r\n']/ }" key
  local role="${3//[$'\t\r\n']/ }" resource="${4//[$'\t\r\n']/ }" permissions="${5//[$'\t\r\n']/ }" granted="${6//[$'\t\r\n']/ }"
  key="F$principal"$'\t'"$right"
  [ -z "${RECORDED[$key]:-}" ] || return 0
  RECORDED[$key]=1
  FINDINGS+=("$principal"$'\t'"$right"$'\t'"$role"$'\t'"$resource"$'\t'"$permissions"$'\t'"$granted")
  echo "FINDING $principal: $right"
}
unknown() {
  local step="${1//[$'\t\r\n']/ }" reason="${2//[$'\t\r\n']/ }" key
  key="U$step"$'\t'"$reason"
  [ -z "${RECORDED[$key]:-}" ] || return 0
  RECORDED[$key]=1
  UNKNOWNS+=("$step"$'\t'"$reason")
  echo "UNKNOWN $step: $reason"
}
pass() { echo "PASS $1: $2"; }
# write_report: --report, when it is named, from the findings and the UNKNOWNs so far.
write_report() {
  [ -n "$REPORT" ] || return 0
  printf '%s\n' ${FINDINGS[@]+"${FINDINGS[@]}"} >"$WORK/findings"
  printf '%s\n' ${UNKNOWNS[@]+"${UNKNOWNS[@]}"} >"$WORK/unknown"
  jq -n --arg run_at "$RUN_AT" --rawfile findings "$WORK/findings" --rawfile unknown "$WORK/unknown" '{
    run_at: $run_at,
    findings: [$findings | split("\n")[] | select(length > 0) | split("\t")
      | {principal: .[0], right: .[1], role: .[2], resource: .[3],
         permissions: ((.[4] // "") | split("\u001f") | map(select(length > 0))), granted_at: (.[5] // "")}],
    unknown: [$unknown | split("\n")[] | select(length > 0) | split("\t") | {step: .[0], reason: .[1]}]}' >"$WORK/report.json"
  cp "$WORK/report.json" "$REPORT"
}
# crash_report STATUS: on_exit's work for a run that stopped before its end.
crash_report() {
  unknown run "the run stopped before its end (exit $1), so every step after the last it printed is unread"
  write_report
  echo "operator-token-minters: stopped before its end, ${#UNKNOWNS[@]} unknown: exit 2"
}
# mark, then unchanged: whether a step recorded no finding and no UNKNOWN since its mark, so it
# prints PASS only then.
mark() { MARK_F="${#FINDINGS[@]}" MARK_U="${#UNKNOWNS[@]}"; }
unchanged() { [ "${#FINDINGS[@]}" -eq "$MARK_F" ] && [ "${#UNKNOWNS[@]}" -eq "$MARK_U" ]; }
have() { command -v "$1" >/dev/null; }
# A record passed between this script's steps is its fields joined by the unit separator, never by
# tabs: read splits on a tab as whitespace, so an empty field (a group's empty user, a secret's
# unnamed project) would shift every field after it. rec, prepended to a jq program, writes one.
US=$'\x1f'
JQ_REC='def rec: map(tostring | gsub("[[:cntrl:]]"; " ")) | join("\u001f");'

# --- The rights and the permissions. ---

# Each right: verb, API group, resource, subresource, and scope: `ns` for a guarded pair's
# namespace, `cluster` for cluster-wide, `named` for the pair's namespace with its ServiceAccount as
# the resource name ("-" is empty). Every guarded pair (the operator's, the broker's, each
# --guarded) gets each right on the operator's cluster. A namespaced right is asked in the
# namespace, so a ClusterRoleBinding that grants it is found too; escalate on clusterroles, the
# bindings of clusterroles, impersonating users and groups and a node's proxy are cluster-scoped,
# which only a ClusterRoleBinding grants. A node's proxy reaches the kubelet's API, whose exec and
# attach enter any pod on the node, the guarded ones included.
RIGHTS_TABLE='
create - serviceaccounts token named
impersonate - serviceaccounts - named
impersonate - users - cluster
impersonate - groups - cluster
escalate rbac.authorization.k8s.io roles - ns
bind rbac.authorization.k8s.io roles - ns
escalate rbac.authorization.k8s.io clusterroles - cluster
bind rbac.authorization.k8s.io clusterroles - ns
create rbac.authorization.k8s.io rolebindings - ns
patch rbac.authorization.k8s.io rolebindings - ns
create rbac.authorization.k8s.io clusterrolebindings - cluster
patch rbac.authorization.k8s.io clusterrolebindings - cluster
create - pods exec ns
create - pods attach ns
create - pods portforward ns
get - pods exec ns
get - pods attach ns
get - pods portforward ns
create - pods - ns
patch - pods - ns
update - pods - ns
create - replicationcontrollers - ns
patch - replicationcontrollers - ns
update - replicationcontrollers - ns
create - nodes proxy cluster
get - nodes proxy cluster
patch - pods ephemeralcontainers ns
update - pods ephemeralcontainers ns
create apps deployments - ns
patch apps deployments - ns
update apps deployments - ns
create apps replicasets - ns
patch apps replicasets - ns
update apps replicasets - ns
create apps statefulsets - ns
patch apps statefulsets - ns
update apps statefulsets - ns
create apps daemonsets - ns
patch apps daemonsets - ns
update apps daemonsets - ns
create batch jobs - ns
patch batch jobs - ns
update batch jobs - ns
create batch cronjobs - ns
patch batch cronjobs - ns
update batch cronjobs - ns
get - secrets - ns
list - secrets - ns
watch - secrets - ns
'
# The container.* permission of each right: what GKE's IAM authorizer checks for the same call,
# with container.namespaces.create and container.serviceAccounts.create for the Workload Identity
# step's other clusters. watch secrets is container.secrets.list's, as list is.
CONTAINER_PERMISSIONS=(
  container.serviceAccounts.createToken container.clusters.impersonate
  container.roles.escalate container.roles.bind container.clusterRoles.escalate container.clusterRoles.bind
  container.roleBindings.create container.roleBindings.update container.clusterRoleBindings.create
  container.clusterRoleBindings.update container.pods.exec container.pods.attach container.pods.portForward
  container.pods.create container.pods.update container.replicationControllers.create
  container.replicationControllers.update container.nodes.proxy container.deployments.create container.deployments.update
  container.replicaSets.create container.replicaSets.update container.statefulSets.create
  container.statefulSets.update container.daemonSets.create container.daemonSets.update container.jobs.create
  container.jobs.update container.cronJobs.create container.cronJobs.update container.secrets.get
  container.secrets.list container.namespaces.create container.serviceAccounts.create
)
GSA_PERMISSIONS=(
  iam.serviceAccounts.getAccessToken iam.serviceAccounts.getOpenIdToken iam.serviceAccounts.signJwt
  iam.serviceAccounts.signBlob iam.serviceAccounts.actAs iam.serviceAccounts.implicitDelegation
  iam.serviceAccountKeys.create
)
# Who can write IAM policy over the checked resources, or change a custom role a principal holds:
# each can grant itself any permission below, so each is a holder of every one.
IAM_WRITE_PERMISSIONS=(resourcemanager.projects.setIamPolicy resourcemanager.folders.setIamPolicy
  resourcemanager.organizations.setIamPolicy iam.serviceAccounts.setIamPolicy secretmanager.secrets.setIamPolicy
  iam.roles.update)
SQL_PERMISSIONS=(cloudsql.users.create cloudsql.users.update)
# Who can register a cluster to the pool project's fleet: a registered cluster shares the pool's
# Workload Identity, so its ServiceAccount envoy-operator/envoy-operator is the operator's member.
FLEET_PERMISSIONS=(gkehub.memberships.create gkehub.memberships.update)
# granting_role sets GRANTING_ROLE to the predefined role whose includedPermissions must hold a
# checked permission.
granting_role() {
  case "$1" in
    container.*) GRANTING_ROLE=roles/container.admin ;;
    resourcemanager.projects.setIamPolicy) GRANTING_ROLE=roles/resourcemanager.projectIamAdmin ;;
    resourcemanager.folders.setIamPolicy) GRANTING_ROLE=roles/resourcemanager.folderIamAdmin ;;
    resourcemanager.organizations.setIamPolicy) GRANTING_ROLE=roles/resourcemanager.organizationAdmin ;;
    iam.serviceAccounts.setIamPolicy) GRANTING_ROLE=roles/iam.serviceAccountAdmin ;;
    secretmanager.secrets.setIamPolicy) GRANTING_ROLE=roles/secretmanager.admin ;;
    iam.roles.update) GRANTING_ROLE=roles/iam.roleAdmin ;;
    iam.serviceAccounts.actAs) GRANTING_ROLE=roles/iam.serviceAccountUser ;;
    iam.serviceAccountKeys.*) GRANTING_ROLE=roles/iam.serviceAccountKeyAdmin ;;
    iam.serviceAccounts.*) GRANTING_ROLE=roles/iam.serviceAccountTokenCreator ;;
    cloudsql.*) GRANTING_ROLE=roles/cloudsql.admin ;;
    secretmanager.*) GRANTING_ROLE=roles/secretmanager.secretAccessor ;;
    storage.*) GRANTING_ROLE=roles/storage.objectViewer ;;
    gkehub.*) GRANTING_ROLE=roles/gkehub.admin ;;
  esac
}

# rights_json [db]: the rights as a JSON array of {verb, group, resource, sub, ns, name, key,
# label}, each right of the table for each guarded pair, each once, with `db` the hold writers' get
# on --db-secret too. key is how a review's answer, which echoes its attributes, finds its right.
rights_json() {
  local verb group resource sub scope ns name pair
  {
    for pair in "${GUARDED[@]}"; do
      while read -r verb group resource sub scope; do
        [ -n "$verb" ] || continue
        [ "$group" != - ] || group=""
        [ "$sub" != - ] || sub=""
        ns="${pair%%/*}" name=""
        case "$scope" in cluster) ns="" ;; named) name="${pair#*/}" ;; esac
        printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$verb" "$group" "$resource" "$sub" "$ns" "$name"
      done <<<"$RIGHTS_TABLE"
    done
    if [ "${1:-}" = db ] && [ -n "$DB_SECRET" ]; then
      printf 'get\t\tsecrets\t\t%s\t%s\n' "${DB_SECRET%%/*}" "${DB_SECRET#*/}"
    fi
  } | jq -R -s -c '[split("\n")[] | select(length > 0) | split("\t")
      | {verb: .[0], group: .[1], resource: .[2], sub: .[3], ns: .[4], name: .[5]}
      | . + {key: ([.verb, .group, .resource, .sub, .ns, .name] | join("|")),
             label: ("\(.verb) \(.resource)\(if .sub != "" then "/" + .sub else "" end)\(if .group != "" then "." + .group else "" end)"
                     + (if .name != "" then " " + .name else "" end) + (if .ns != "" then " in " + .ns else " cluster-wide" end))}]
    | reduce .[] as $r ([]; if any(.[]; .key == $r.key) then . else . + [$r] end)'
}
# GUARD_RIGHTS: the rights no principal the Workload Identity step reviews may hold on a cluster of
# the pool, as rights_json writes rights: create namespaces cluster-wide, and for each guarded
# pair's namespace, create serviceaccounts and create pods there. A pod in the namespace runs as
# any ServiceAccount of it, and the ServiceAccount is the pair the pool binds, so either lets its
# maker act as the pair's Google service account. Each label names the pairs the namespace holds.
GUARD_RIGHTS="$(printf '%s\n' "${GUARDED[@]}" | jq -R -s -c '
  [split("\n")[] | select(length > 0) | split("/") | {ns: .[0], sa: .[1]}] as $pairs
  | [{verb: "create", group: "", resource: "namespaces", sub: "", ns: "", name: "", label: "create namespaces cluster-wide"}]
    + [([$pairs[].ns] | unique)[] as $n
       | ([$pairs[] | select(.ns == $n) | "\(.ns)/\(.sa)"] | join(", ")) as $guarding
       | {verb: "create", group: "", resource: "serviceaccounts", sub: "", ns: $n, name: "", label: "create serviceaccounts in \($n), guarding \($guarding)"},
         {verb: "create", group: "", resource: "pods", sub: "", ns: $n, name: "", label: "create pods in \($n), guarding \($guarding)"}]
  | map(. + {key: ([.verb, .group, .resource, .sub, .ns, .name] | join("|"))})')"
GUARDED_TEXT="$(printf '%s, ' "${GUARDED[@]}")"
GUARDED_TEXT="${GUARDED_TEXT%, }"

# --- Principals. ---

system_namespace() { case "$1" in kube-system | gke-* | gmp-*) return 0 ;; *) return 1 ;; esac; }
# service_agent: a Google-managed service agent, which no principal of the project can act as.
service_agent() {
  [[ "$1" =~ ^serviceAccount:service-[0-9]+@(gcp-sa-[a-z0-9-]+|container-engine-robot|compute-system)\.iam\.gserviceaccount\.com$ ]]
}
# account_project EMAIL sets ACCOUNT_PROJECT to the project a Google service account's email
# names, or --project.
account_project() {
  if [[ "$1" =~ @([a-z][a-z0-9-]*)\.iam\.gserviceaccount\.com$ ]]; then ACCOUNT_PROJECT="${BASH_REMATCH[1]}"; else ACCOUNT_PROJECT="$PROJECT"; fi
}
account_project "$GSA"
GSA_PROJECT="$ACCOUNT_PROJECT"
account_project "$BROKER"
BROKER_PROJECT="$ACCOUNT_PROJECT"
# The members a workload pool gives a Kubernetes ServiceAccount: <pool>.svc.id.goog[<ns>/<name>],
# whatever cluster of the pool the ServiceAccount is on.
WI_MEMBER="serviceAccount:$POOL_PROJECT.svc.id.goog[$NAMESPACE/$KSA]"
BROKER_WI_MEMBER=""
[ -z "$BROKER_WORKLOAD" ] || BROKER_WI_MEMBER="serviceAccount:$POOL_PROJECT.svc.id.goog[$BROKER_WORKLOAD]"

# k8s_subject KIND NAMESPACE NAME: sets SUBJECT, the principal's name in a finding, and SUBJECT_OK,
# whether it is allowed: a person, a system component, a system namespace's ServiceAccount, a Group
# other than the system groups that hold ServiceAccounts or anyone (the header's "Group subjects"),
# Google's service agent, or the operator's own Google service account.
k8s_subject() {
  local kind="$1" ns="$2" name="$3"
  SUBJECT_OK=false
  case "$kind" in
    ServiceAccount)
      SUBJECT="system:serviceaccount:$ns:$name"
      if system_namespace "$ns"; then SUBJECT_OK=true; fi
      ;;
    Group)
      SUBJECT="group:$name"
      case "$name" in
        system:serviceaccounts | system:serviceaccounts:* | system:authenticated | system:unauthenticated) ;;
        *) SUBJECT_OK=true ;;
      esac
      ;;
    User)
      case "$name" in
        system:serviceaccount:*:*)
          SUBJECT="$name"
          ns="${name#system:serviceaccount:}"
          if system_namespace "${ns%%:*}"; then SUBJECT_OK=true; fi
          ;;
        system:anonymous) SUBJECT="user:$name" ;;
        system:*) SUBJECT="user:$name" SUBJECT_OK=true ;;
        *.gserviceaccount.com)
          SUBJECT="serviceAccount:$name"
          if { [ -n "$GSA" ] && [ "$name" = "$GSA" ]; } || service_agent "$SUBJECT"; then SUBJECT_OK=true; fi
          ;;
        *) SUBJECT="user:$name" SUBJECT_OK=true ;;
      esac
      ;;
    *) SUBJECT="$kind:$name" ;;
  esac
}

# --- The IAM checks: each a step, a resource, the permissions asked and a witness. ---

declare -A CHECK_STEP=() CHECK_RESOURCE=() CHECK_PERMS=() CHECK_WITNESS=() CHECK_MISSING=()
CHECKS=(gke gsa broker sql dbsecret state signing fleet)
PROJECT_RES="//cloudresourcemanager.googleapis.com/projects/$PROJECT"
BUCKET=""
if [ -n "$BROKER_STATE" ]; then
  BUCKET="${BROKER_STATE#gs://}"
  BUCKET="${BUCKET%%/*}"
fi
project_name="the operator's project"
[ -z "$PROJECT" ] || project_name="project $PROJECT"
CHECK_STEP[gke]="live: GKE IAM on $project_name"
CHECK_RESOURCE[gke]="$PROJECT_RES"
CHECK_PERMS[gke]="${CONTAINER_PERMISSIONS[*]} resourcemanager.projects.setIamPolicy iam.roles.update"
# The names steps print: a flag's value, or what it would name. A default holding an apostrophe
# cannot be written as ${NAME:-...} inside double quotes, where bash reads the apostrophe as a quote.
operator_account="the operator's Google service account" sql_name="the broker's instance"
db_name="of the broker's database login" cluster_name="the operator's cluster"
[ -z "$GSA" ] || operator_account="$GSA"
[ -z "$SQL_INSTANCE" ] || sql_name="$SQL_INSTANCE"
[ -z "$DB_SECRET" ] || db_name="$DB_SECRET"
[ -z "$CLUSTER" ] || cluster_name="$CLUSTER"
CHECK_STEP[gsa]="live: minters of $operator_account"
CHECK_RESOURCE[gsa]="//iam.googleapis.com/projects/$GSA_PROJECT/serviceAccounts/$GSA"
CHECK_PERMS[gsa]="${GSA_PERMISSIONS[*]} iam.serviceAccounts.setIamPolicy"
CHECK_WITNESS[gsa]="$WI_MEMBER"
[ -n "$GSA" ] || CHECK_MISSING[gsa]="no --gsa: the operator's Google service account is not named"
CHECK_STEP[broker]="live: minters of the broker account $BROKER"
CHECK_RESOURCE[broker]="//iam.googleapis.com/projects/$BROKER_PROJECT/serviceAccounts/$BROKER"
CHECK_PERMS[broker]="${GSA_PERMISSIONS[*]} iam.serviceAccounts.setIamPolicy"
CHECK_WITNESS[broker]="$BROKER_WI_MEMBER"
[ -n "$BROKER_WORKLOAD" ] || CHECK_MISSING[broker]="no --broker-workload: the broker account's Workload Identity member cannot be told from another's"
CHECK_STEP[sql]="live: hold writers: Cloud SQL users of $sql_name"
CHECK_RESOURCE[sql]="//sqladmin.googleapis.com/projects/$PROJECT/instances/$SQL_INSTANCE"
CHECK_PERMS[sql]="${SQL_PERMISSIONS[*]}"
[ -n "$SQL_INSTANCE" ] || CHECK_MISSING[sql]="no --sql-instance: the broker database's instance is not named"
CHECK_STEP[dbsecret]="live: hold writers: Secret $db_name"
CHECK_RESOURCE[dbsecret]="$PROJECT_RES"
CHECK_PERMS[dbsecret]="container.secrets.get"
[ -n "$DB_SECRET" ] || CHECK_MISSING[dbsecret]="no --db-secret: the Secret of the broker's database login is not named"
state_name="the broker program's state"
[ -z "$BUCKET" ] || state_name="the broker program's state in gs://$BUCKET"
CHECK_STEP[state]="live: hold writers: $state_name"
CHECK_RESOURCE[state]="//storage.googleapis.com/projects/_/buckets/$BUCKET"
CHECK_PERMS[state]="storage.objects.get"
[ -n "$BUCKET" ] || CHECK_MISSING[state]="no --broker-state: where the broker program's state is kept is not named"
CHECK_STEP[signing]="live: hold writers: the signing key ${SIGNING_SECRET:-of the writer hold}"
CHECK_PERMS[signing]="secretmanager.versions.access secretmanager.secrets.setIamPolicy"
CHECK_WITNESS[signing]="serviceAccount:$GSA"
if [ -z "$SIGNING_SECRET" ]; then
  CHECK_MISSING[signing]="no --signing-secret: the writer hold's signing key is not named"
elif [ -z "$GSA" ]; then
  CHECK_MISSING[signing]="no --gsa: the signing key's one reader, the operator's Google service account, is not named"
fi
CHECK_STEP[fleet]="live: who can register a cluster to the fleet of project ${POOL_PROJECT:-?}"
CHECK_RESOURCE[fleet]="//cloudresourcemanager.googleapis.com/projects/$POOL_PROJECT"
CHECK_PERMS[fleet]="${FLEET_PERMISSIONS[*]}"
# The pool project's own IAM writers, when it is not --project's, whose are the GKE IAM step's.
[ -z "$POOL_PROJECT" ] || [ "$POOL_PROJECT" = "$PROJECT" ] || CHECK_PERMS[fleet]+=" resourcemanager.projects.setIamPolicy"
if [ -z "$PROJECT" ]; then
  for check in "${CHECKS[@]}"; do CHECK_MISSING[$check]="no --project: the project that runs the operator's cluster is not named"; done
fi
[ -n "$BROKER" ] || CHECK_MISSING[broker]=skip

# iam_verdict CHECK MEMBER ROLE ATTACHED HOW: sets VERDICT to ok (allowed), skip (a group judged by
# its members) or finding, with NOTE beside a finding. ATTACHED is the resource the binding is
# made on; HOW is `expanded` for a group the analysis lists the members of, `code` for a member a
# preview declares (its group, if any, is the live steps' to expand) or empty.
iam_verdict() {
  local check="$1" member="$2" role="$3" attached="$4" how="$5"
  VERDICT=finding NOTE=""
  case "$member" in
    user:*) VERDICT=ok; return ;;
    group:*)
      if [ "$how" = expanded ] || [ "$how" = code ]; then VERDICT=skip; else NOTE=" (a group whose members the analysis does not list)"; fi
      return
      ;;
  esac
  if service_agent "$member"; then VERDICT=ok; return; fi
  case "$check" in
    gsa)
      if [ "$member" = "$WI_MEMBER" ]; then VERDICT=ok; return; fi
      if [ -n "$BROKER" ] && [ "$member" = "serviceAccount:$BROKER" ] && [ "$role" = roles/iam.serviceAccountTokenCreator ] &&
        [[ "$attached" == */serviceAccounts/"$GSA" ]]; then
        VERDICT=ok
      fi
      ;;
    broker) if [ -n "$BROKER_WI_MEMBER" ] && [ "$member" = "$BROKER_WI_MEMBER" ]; then VERDICT=ok; fi ;;
    signing) if [ "$member" = "serviceAccount:$GSA" ]; then VERDICT=ok; fi ;;
  esac
}
# held_text CHECK PERMISSIONS_CSV: the check's permissions among PERMISSIONS, in the check's order.
held_text() {
  local permission out=""
  for permission in ${CHECK_PERMS[$1]}; do
    case ",$2," in *",$permission,"*) out="${out:+$out, }$permission" ;; esac
  done
  printf '%s' "${out:-a permission of ${CHECK_PERMS[$1]// /, }}"
}
# held_list CHECK PERMISSIONS_CSV: the same, separated by $US, for the report: every permission the
# check asks about where the answer names none of them.
held_list() {
  local permission out=""
  for permission in ${CHECK_PERMS[$1]}; do
    case ",$2," in *",$permission,"*) out="${out:+$out$US}$permission" ;; esac
  done
  [ -n "$out" ] || out="${CHECK_PERMS[$1]// /$US}"
  printf '%s' "$out"
}

# --- Roles. ---

# role_perms ROLE: sets ROLE_FILE to a file listing the role's permissions, from the previews'
# custom roles or `gcloud iam roles describe`; returns 1 with ROLE_ERROR.
role_perms() {
  ROLE_FILE="$WORK/roles/$(printf '%s' "$1" | tr -c 'A-Za-z0-9._-' '_')"
  ROLE_ERROR=""
  if [ -e "$ROLE_FILE.error" ]; then
    ROLE_ERROR="$(cat "$ROLE_FILE.error")"
    return 1
  fi
  [ ! -e "$ROLE_FILE" ] || return 0
  if ! have gcloud; then
    ROLE_ERROR="gcloud is not on PATH, so the role $1 cannot be described"
  elif ! gcloud iam roles describe "$1" --format=json --quiet >"$WORK/role.json" 2>"$WORK/role.err"; then
    ROLE_ERROR="gcloud iam roles describe $1 was refused (it needs iam.roles.get for a custom role): $(first_line "$WORK/role.err")"
  elif ! jq -r '.includedPermissions[]?' "$WORK/role.json" >"$WORK/role.list" 2>"$WORK/role.err"; then
    ROLE_ERROR="gcloud iam roles describe $1 answered no JSON role: $(first_line "$WORK/role.err")"
  fi
  if [ -n "$ROLE_ERROR" ]; then
    printf '%s' "$ROLE_ERROR" >"$ROLE_FILE.error"
    return 1
  fi
  sort -u "$WORK/role.list" >"$ROLE_FILE"
}

# --- permission names ---
step="permission names"
mark
checked_names=("${CONTAINER_PERMISSIONS[@]}" "${GSA_PERMISSIONS[@]}" "${SQL_PERMISSIONS[@]}" "${FLEET_PERMISSIONS[@]}"
  "${IAM_WRITE_PERMISSIONS[@]}" secretmanager.versions.access storage.objects.get)
declare -A roles_used=() role_holds=()
for permission in "${checked_names[@]}"; do
  granting_role "$permission"
  role="$GRANTING_ROLE"
  if [ -z "${roles_used[$role]:-}" ]; then
    roles_used[$role]="read"
    if role_perms "$role"; then
      while IFS= read -r held; do role_holds["$role $held"]=1; done <"$ROLE_FILE"
    else
      roles_used[$role]=failed
      unknown "$step" "$ROLE_ERROR"
    fi
  fi
  [ "${roles_used[$role]}" != failed ] || continue
  [ -n "${role_holds["$role $permission"]:-}" ] ||
    unknown "$step" "$permission is not in $role's includedPermissions, so no check that names it can find a holder"
done
if unchanged; then
  pass "$step" "each of the ${#checked_names[@]} permission names the IAM steps ask about is in the role that grants it ($(printf '%s\n' "${!roles_used[@]}" | sort | paste -sd ' ' | sed 's/ /, /g'))"
fi

# --- code: RBAC ---
step="code: RBAC"
if [ "${#RBAC_RENDERS[@]}" -eq 0 ]; then
  unknown "$step" "no --rbac-render: what the cluster's RBAC program declares is not read"
else
  readable=true
  for file in "${RBAC_RENDERS[@]}"; do
    [ -r "$file" ] || { unknown "$step" "the render $file cannot be read"; readable=false; }
  done
  if [ "$readable" = true ]; then
    mark
    if ! jq -r -n --argjson rights "$(rights_json db)" "$JQ_REC"'
        def objs: if type == "array" then .[] | objs
                  elif type == "object" and (.kind // "") == "List" then (.items // [])[] | objs
                  elif type == "object" then . else empty end;
        def has_($list; $x): any(($list // [])[]; . == $x);
        def matches($r): (has_(.verbs; $r.verb) or has_(.verbs; "*"))
          and (has_(.apiGroups; $r.group) or has_(.apiGroups; "*"))
          and (has_(.resources; "*") or has_(.resources; $r.resource + (if $r.sub != "" then "/" + $r.sub else "" end))
               or ($r.sub != "" and has_(.resources; "*/" + $r.sub)))
          and (((.resourceNames // []) | length) == 0 or ($r.name != "" and has_(.resourceNames; $r.name)));
        [inputs | objs] as $objects
        | [$objects[] | select(.kind == "RoleBinding" or .kind == "ClusterRoleBinding")] as $bindings
        | if ($bindings | length) == 0 then ["empty", ($objects | length)] | rec
          else $bindings[] as $b
          | ($b.metadata.namespace // "") as $bns
          | (if $b.kind == "RoleBinding" then "RoleBinding \($bns)/\($b.metadata.name)" else "ClusterRoleBinding \($b.metadata.name)" end) as $id
          | ([$objects[] | select(.kind == ($b.roleRef.kind // "") and .metadata.name == $b.roleRef.name
                                   and (.kind == "ClusterRole" or (.metadata.namespace // "") == $bns))]) as $found
          | (if ($found | length) > 0 then (if $found[0].aggregationRule then null else ($found[0].rules // []) end)
             elif $b.roleRef.kind == "ClusterRole" and $b.roleRef.name == "cluster-admin" then [{verbs: ["*"], apiGroups: ["*"], resources: ["*"]}]
             elif $b.roleRef.kind == "ClusterRole" and $b.roleRef.name == "view" then []
             else null end) as $rules
          | ($b.subjects // [])[] as $s
          | [$s.kind, ($s.namespace // (if $s.kind == "ServiceAccount" then $bns else "" end)), $s.name] as $subject
          | if $rules == null then (["role", $id, "\($b.roleRef.kind) \($b.roleRef.name)"] + $subject) | rec
            else $rights[] as $r
            | select($b.kind == "ClusterRoleBinding" or ($r.ns != "" and $r.ns == $bns))
            | select(any($rules[]; matches($r)))
            | (["grant", $id, $r.label] + $subject) | rec
            end
          end' "${RBAC_RENDERS[@]}" >"$WORK/render.rec" 2>"$WORK/render.err"; then
      unknown "$step" "a render is not JSON: $(first_line "$WORK/render.err")"
    else
      declared=0
      while IFS="$US" read -r -u 3 what id detail kind ns name; do
        case "$what" in
          empty) unknown "$step" "the renders declare no RoleBinding or ClusterRoleBinding (they hold $id objects), so they show nothing" ;;
          grant)
            declared=$((declared + 1))
            k8s_subject "$kind" "$ns" "$name"
            [ "$SUBJECT_OK" = true ] || finding "$SUBJECT" "$detail, declared by $id" "$detail" "cluster:$cluster_name" "$detail" "$id"
            ;;
          role)
            k8s_subject "$kind" "$ns" "$name"
            [ "$SUBJECT_OK" = true ] ||
              unknown "$step" "$id binds $SUBJECT to $detail, which the render does not hold (or aggregates), so the rights it gives are unknown"
            ;;
        esac
      done 3<"$WORK/render.rec"
      if unchanged; then
        pass "$step" "the renders give the rights to no one but people, system namespaces and components, and the operator's own account ($declared grants read)"
      fi
    fi
  fi
fi

# --- code: IAM ---

# covers CHECK KIND ID PROJECT: whether a binding the code declares on KIND ID (with the project its
# resource names, for a secret) reaches the check's resource.
covers() {
  local check="$1" kind="$2" id="$3" in_project="$4"
  case "$kind" in organization | folder) return 0 ;; esac
  case "$check:$kind" in
    gke:project | sql:project | dbsecret:project | state:project | signing:project) [ "${id#projects/}" = "$PROJECT" ] ;;
    gsa:project) [ "${id#projects/}" = "$GSA_PROJECT" ] ;;
    broker:project) [ "${id#projects/}" = "$BROKER_PROJECT" ] ;;
    fleet:project) [ "${id#projects/}" = "$POOL_PROJECT" ] ;;
    gsa:serviceaccount) [ "${id##*/}" = "$GSA" ] ;;
    broker:serviceaccount) [ "${id##*/}" = "$BROKER" ] ;;
    signing:secret)
      [ "${id##*/}" = "$SIGNING_SECRET" ] || return 1
      case "$id" in projects/*) in_project="${id#projects/}" in_project="${in_project%%/*}" ;; esac
      [ -z "$in_project" ] || [ "$in_project" = "$PROJECT" ] || [ "$in_project" = "${PROJECT_NUMBER:-}" ]
      ;;
    state:bucket) [ "${id#b/}" = "$BUCKET" ] ;;
    *) return 1 ;;
  esac
}
resource_of() { if [ "$1" = signing ]; then echo "//secretmanager.googleapis.com/projects/${PROJECT_NUMBER:-$PROJECT}/secrets/$SIGNING_SECRET"; else echo "${CHECK_RESOURCE[$1]}"; fi; }

# The project number, which Secret Manager's resource names use.
PROJECT_NUMBER="" PROJECT_NUMBER_ERROR=""
if [ -z "$PROJECT" ]; then
  PROJECT_NUMBER_ERROR="no --project"
elif ! have gcloud; then
  PROJECT_NUMBER_ERROR="gcloud is not on PATH"
elif ! gcloud projects describe "$PROJECT" --format=json --quiet >"$WORK/project.json" 2>"$WORK/project.err"; then
  PROJECT_NUMBER_ERROR="gcloud projects describe $PROJECT was refused (it needs resourcemanager.projects.get): $(first_line "$WORK/project.err")"
else
  PROJECT_NUMBER="$(jq -r '.projectNumber // ""' "$WORK/project.json" 2>/dev/null || true)"
  [ -n "$PROJECT_NUMBER" ] || PROJECT_NUMBER_ERROR="gcloud projects describe $PROJECT answered no projectNumber"
fi
CHECK_RESOURCE[signing]="$(resource_of signing)"

step="code: IAM"
if [ "${#IAM_PREVIEWS[@]}" -eq 0 ]; then
  unknown "$step" "no --iam-preview: what the IAM programs declare is not read"
else
  mark
  : >"$WORK/preview.rec"
  for file in "${IAM_PREVIEWS[@]}"; do
    if [ ! -r "$file" ]; then
      unknown "$step" "the preview $file cannot be read"
      continue
    fi
    # shellcheck disable=SC2016 # $-names are jq's
    if ! jq -r --arg file "$file" "$JQ_REC"'
        def sentinel: "04da6b54-80e4-46f7-96ec-b56ff0331ba9";
        def unknown_: type == "string" and contains(sentinel);
        def binding($urn; $scope; $role; $member):
          if ($role | unknown_) or $role == null then ["unknown", $urn, "role"]
          elif ($member | unknown_) or $member == null then ["unknown", $urn, "member"]
          elif any($scope[]; unknown_) then ["unknown", $urn, "resource"]
          else ["binding", $scope[0], $scope[1], $scope[2], $role, $member, $urn] end;
        if (.steps | type) != "array" or (.steps | length) == 0 then ["empty", $file] | rec
        elif ((.changeSummary.same // 0) > 0) and ([.steps[] | select(.op == "same")] | length) == 0 then ["nosames", $file] | rec
        else .steps[] | select(.newState != null) | .urn as $urn | .newState.type as $t | (.newState.inputs // {}) as $i
        | if ($t | test("auditconfig|memberremove"; "i")) or ($t | test("^gcp:iam/"; "i")) then empty
          elif ($t | test("^gcp:(projects|organizations)/iamcustomrole:"; "i")) then
            ["role", (if ($t | test("^gcp:projects/"; "i")) then "projects/\($i.project)/roles/\($i.roleId)"
                      else "organizations/\($i.orgId)/roles/\($i.roleId)" end), (($i.permissions // []) | join(",")), $urn] | rec
          else
            (if ($t | test("^gcp:projects/iam(member|binding|policy):"; "i")) then ["project", ($i.project // ""), ""]
             elif ($t | test("^gcp:organizations/iam(member|binding|policy):"; "i")) then ["organization", ($i.orgId // ""), ""]
             elif ($t | test("^gcp:folder/iam(member|binding|policy):"; "i")) then ["folder", ($i.folder // ""), ""]
             elif ($t | test("^gcp:serviceaccount/iam(member|binding|policy):"; "i")) then ["serviceaccount", ($i.serviceAccountId // ""), ""]
             elif ($t | test("^gcp:secretmanager/secretiam(member|binding|policy):"; "i")) then ["secret", ($i.secretId // ""), ($i.project // "")]
             elif ($t | test("^gcp:storage/bucketiam(member|binding|policy):"; "i")) then ["bucket", ($i.bucket // ""), ""]
             else null end) as $scope
          | if $scope == null then
              if ($t | test("^gcp:(projects|organizations|folder|serviceaccount|secretmanager|storage)/"; "i"))
                 and (($t | split(":"))[1] // "" | test("iam|acl|accesscontrol"; "i"))
              then ["unread", $urn, $t] | rec else empty end
            elif ($t | test("policy:"; "i")) then
              if ($i.policyData | unknown_) or $i.policyData == null then ["unknown", $urn, "policy"] | rec
              else ($i.policyData | fromjson | .bindings[]?) as $b | ($b.members // [])[] as $m | binding($urn; $scope; $b.role; $m) | rec end
            elif ($t | test("binding:"; "i")) then
              if ($i.members | unknown_) then ["unknown", $urn, "member"] | rec
              else ($i.members // [])[] as $m | binding($urn; $scope; $i.role; $m) | rec end
            else binding($urn; $scope; $i.role; $i.member) | rec end
          end
        end' "$file" >>"$WORK/preview.rec" 2>"$WORK/preview.err"; then
      unknown "$step" "the preview $file is not the JSON pulumi preview --json prints: $(first_line "$WORK/preview.err")"
    fi
  done
  # The previews' own custom roles, so a role the code declares is read as the code declares it.
  while IFS="$US" read -r -u 3 what name permissions _; do
    [ "$what" = role ] || continue
    role_perms_file="$WORK/roles/$(printf '%s' "$name" | tr -c 'A-Za-z0-9._-' '_')"
    printf '%s\n' "${permissions//,/$'\n'}" | sed '/^$/d' | sort -u >"$role_perms_file"
  done 3<"$WORK/preview.rec"
  judged=0
  while IFS="$US" read -r -u 3 what a b c d e f; do
    case "$what" in
      empty) unknown "$step" "the preview $a lists no resource, so it shows nothing" ;;
      nosames) unknown "$step" "the preview $a was made without --show-sames, so it leaves out every binding it does not change" ;;
      unknown) unknown "$step" "$a: the preview does not know its $b (it is computed when the program applies), so what it grants is unknown" ;;
      unread) unknown "$step" "$a: the check does not read $b, which can grant on the resources these checks are about" ;;
      binding)
        # a: kind, b: id, c: the secret's project, d: role, e: member, f: urn
        for check in "${CHECKS[@]}"; do
          [ -z "${CHECK_MISSING[$check]:-}" ] || continue
          covers "$check" "$a" "$b" "$c" || continue
          if ! role_perms "$d"; then
            unknown "$step" "$f grants $d, whose permissions are unknown: $ROLE_ERROR"
            continue
          fi
          granted="$(tr ' ' '\n' <<<"${CHECK_PERMS[$check]}" | grep -xF -f "$ROLE_FILE" | paste -sd, || true)"
          [ -n "$granted" ] || continue
          judged=$((judged + 1))
          attached="$a:$b"
          [ "$a" != serviceaccount ] || attached="//iam.googleapis.com/projects/-/serviceAccounts/${b##*/}"
          iam_verdict "$check" "$e" "$d" "$attached" code
          [ "$VERDICT" != finding ] ||
            finding "$e" "$(held_text "$check" "$granted") on $(resource_of "$check") through $d, declared by $f$NOTE" \
              "$d" "$(resource_of "$check")" "$(held_list "$check" "$granted")" "$attached"
        done
        ;;
    esac
  done 3<"$WORK/preview.rec"
  if unchanged; then
    pass "$step" "the previews grant the checked permissions to no one but people, Google's service agents and the accounts allowed by name ($judged grants read; a group they name is judged by its members in the live steps)"
  fi
fi

# --- live: clusters ---
# The operator's cluster is one name in one location. Two clusters of the project can share a name
# in two locations, and the one that is not the operator's, holding the namespace envoy-operator, is
# another cluster of the pool. --location says which; without it, the list's one cluster of that
# name is the operator's, and with two or more none is: the run says so, and reads none of them as
# the operator's or as another cluster.
step="live: clusters"
CLUSTERS_LISTED=false CLUSTERS_COMPLETE=false OPERATOR_FOUND=false OPERATOR_LOCATION="$LOCATION"
# PROJECT_CLUSTERS: <project>/<location>/<name> of each cluster the project's list holds, which the
# Workload Identity step reads, so its fleet part does not read one twice.
declare -A PROJECT_CLUSTERS=()
mark
if [ -z "$PROJECT" ]; then
  unknown "$step" "no --project: the project that runs the operator's cluster is not named"
elif [ -z "$CLUSTER" ]; then
  unknown "$step" "no --cluster: the operator's cluster is not named"
elif ! have gcloud; then
  unknown "$step" "gcloud is not on PATH"
elif ! gcloud container clusters list --project="$PROJECT" --format=json --quiet >"$WORK/clusters.json" 2>"$WORK/clusters.err"; then
  unknown "$step" "gcloud container clusters list was refused (it needs container.clusters.list): $(first_line "$WORK/clusters.err")"
elif ! jq -r '.[] | [.name, .location] | @tsv' "$WORK/clusters.json" >"$WORK/clusters.tsv" 2>"$WORK/clusters.err.jq"; then
  unknown "$step" "gcloud container clusters list answered no JSON list: $(first_line "$WORK/clusters.err.jq")"
elif [ ! -s "$WORK/clusters.tsv" ]; then
  unknown "$step" "the project lists no cluster, so the operator's cluster cannot be read"
else
  CLUSTERS_LISTED=true
  while IFS=$'\t' read -r name location; do
    PROJECT_CLUSTERS["$PROJECT/$location/$name"]=1
  done <"$WORK/clusters.tsv"
  clusters_text="$(awk -F'\t' '{ printf "%s%s (%s)", (NR > 1 ? ", " : ""), $1, $2 }' "$WORK/clusters.tsv")"
  same_name="$(awk -F'\t' -v c="$CLUSTER" '$1 == c { n++; l = l (n > 1 ? ", " : "") $2 } END { print n + 0 "\t" l }' "$WORK/clusters.tsv")"
  if [ -n "$LOCATION" ]; then
    if [ -n "${PROJECT_CLUSTERS["$PROJECT/$LOCATION/$CLUSTER"]:-}" ]; then
      OPERATOR_FOUND=true
    else
      unknown "$step" "--cluster $CLUSTER --location $LOCATION is not among the project's clusters ($clusters_text)"
    fi
  elif [ "${same_name%%$'\t'*}" -eq 1 ]; then
    OPERATOR_FOUND=true OPERATOR_LOCATION="${same_name#*$'\t'}"
  elif [ "${same_name%%$'\t'*}" -eq 0 ]; then
    unknown "$step" "--cluster $CLUSTER is not among the project's clusters ($clusters_text)"
  else
    unknown "$step" "--cluster $CLUSTER names ${same_name%%$'\t'*} clusters of the project (in ${same_name#*$'\t'}), and no --location says which is the operator's, so none of them is read"
  fi
  # gcloud lists what the zones that answered hold, and says on stderr which did not ("The following
  # zones did not respond: ... List results may be incomplete."), exiting 0.
  if grep -q 'did not respond' "$WORK/clusters.err"; then
    unknown "$step" "gcloud container clusters list says its list is incomplete, so a cluster in a zone that did not answer is neither read nor counted: $(grep -m 1 'did not respond' "$WORK/clusters.err" | sed 's/[[:cntrl:]]/ /g')"
  else
    CLUSTERS_COMPLETE=true
  fi
  ! unchanged || pass "$step" "the project runs $clusters_text; the operator's is $CLUSTER in $OPERATOR_LOCATION"
fi

# kube_for NAME LOCATION PROJECT: sets KUBE to a kubeconfig of this run's own for the GKE cluster;
# returns 1 with KUBE_ERROR. The caller's own kubeconfig is never read or written.
kube_for() {
  KUBE="$WORK/kube/$(printf 'gke_%s_%s_%s' "$3" "$2" "$1" | tr -c 'A-Za-z0-9._-' '_')" KUBE_ERROR=""
  if ! have kubectl; then
    KUBE_ERROR="kubectl is not on PATH"
    return 1
  fi
  if ! KUBECONFIG="$KUBE" gcloud container clusters get-credentials "$1" --location="$2" --project="$3" --quiet \
    ${CREDENTIALS_ARGS[@]+"${CREDENTIALS_ARGS[@]}"} >/dev/null 2>"$WORK/credentials.err"; then
    KUBE_ERROR="gcloud container clusters get-credentials $1 --location $2 --project $3 was refused (it needs container.clusters.get): $(first_line "$WORK/credentials.err")"
    return 1
  fi
}
# kube_for_membership ID LOCATION: the same for a member of the pool project's fleet that is no GKE
# cluster, through the Connect gateway.
kube_for_membership() {
  KUBE="$WORK/kube/$(printf 'membership_%s_%s' "$2" "$1" | tr -c 'A-Za-z0-9._-' '_')" KUBE_ERROR=""
  if ! have kubectl; then
    KUBE_ERROR="kubectl is not on PATH"
    return 1
  fi
  if ! KUBECONFIG="$KUBE" gcloud container fleet memberships get-credentials "$1" --location="$2" --project="$POOL_PROJECT" --quiet \
    >/dev/null 2>"$WORK/credentials.err"; then
    KUBE_ERROR="gcloud container fleet memberships get-credentials $1 --location $2 --project $POOL_PROJECT was refused (it needs gkehub.gateway.get and gkehub.gateway.generateCredentials): $(first_line "$WORK/credentials.err")"
    return 1
  fi
}

# review KUBECONFIG PRINCIPALS RIGHTS: one SubjectAccessReview for each principal of the JSON array
# PRINCIPALS ({principal, user, groups}) and each right of the JSON array RIGHTS, all sent as one
# List, so a cluster is asked in one kubectl call. A review's answer is matched to its question by
# the subject and the attributes the API server echoes in its spec. Writes a record of principal,
# right label, allowed|refused|error and the evaluation error per review to $WORK/review.rec;
# returns 1 with REVIEW_ERROR when the API server refused the call or did not answer every review.
review() {
  REVIEW_ERROR=""
  if ! jq -n --argjson principals "$2" --argjson rights "$3" '{apiVersion: "v1", kind: "List", items: [$principals[] as $p | $rights[] | {
      apiVersion: "authorization.k8s.io/v1", kind: "SubjectAccessReview",
      spec: ({resourceAttributes: ({verb, resource} + (if .group != "" then {group} else {} end)
                                   + (if .sub != "" then {subresource: .sub} else {} end)
                                   + (if .ns != "" then {namespace: .ns} else {} end)
                                   + (if .name != "" then {name} else {} end))}
             + (if $p.user != "" then {user: $p.user} else {} end)
             + (if ($p.groups | length) > 0 then {groups: $p.groups} else {} end))}]}' >"$WORK/review-in.json" 2>"$WORK/review.err"; then
    REVIEW_ERROR="this script could not build the reviews: $(first_line "$WORK/review.err")"
    return 1
  fi
  if ! kubectl --kubeconfig "$1" create -f - -o json <"$WORK/review-in.json" >"$WORK/review.json" 2>"$WORK/review.err"; then
    REVIEW_ERROR="the API server refused the review (it needs create on subjectaccessreviews, container.subjectAccessReviews.create on GKE): $(first_line "$WORK/review.err")"
    return 1
  fi
  if ! jq -r --argjson principals "$2" --argjson rights "$3" "$JQ_REC"'
      ($rights | map({(.key): .label}) | add) as $labels
      | ($principals | map({("\(.user)|\(.groups | join(","))"): .principal}) | add) as $names
      | [(if .kind == "List" then .items[] else . end)
         | (.spec.resourceAttributes // {}) as $a
         | ([$a.verb, $a.group, $a.resource, $a.subresource, $a.namespace, $a.name] | map(. // "") | join("|")) as $key
         | ("\(.spec.user // "")|\((.spec.groups // []) | join(","))") as $who
         | [($names[$who] // ""), ($labels[$key] // ""),
            (if .status.allowed == true then "allowed" elif (.status.evaluationError // "") != "" then "error" else "refused" end),
            (.status.evaluationError // ""), (.status.reason // "")]] as $rows
      | ([$rows[] | select(.[0] != "" and .[1] != "")] | length) as $answered
      | (($principals | length) * ($rights | length)) as $asked
      | if $answered != $asked or ($rows | length) != $asked then ["short", $answered, $asked] | rec else $rows[] | rec end' \
    "$WORK/review.json" >"$WORK/review.rec" 2>"$WORK/review.err"; then
    REVIEW_ERROR="the API server's answer to the reviews is not JSON: $(first_line "$WORK/review.err")"
    return 1
  fi
  local what answered asked
  IFS="$US" read -r what answered asked <"$WORK/review.rec" || true
  if [ "$what" = short ]; then
    REVIEW_ERROR="the API server answered $answered of the $asked reviews asked"
    return 1
  fi
}
# principals_json FILE: the principals of FILE's records (principal, user, groups joined by
# commas) as review's JSON array.
principals_json() {
  jq -R -s -c '[split("\n")[] | select(length > 0) | split("\u001f")
    | {principal: .[0], user: .[1], groups: (.[2] | split(",") | map(select(length > 0)))}]' "$1"
}
# The witness, the first principal of every cluster's reviews: the group the built-in cluster-admin
# binding names, which every review must allow. An API server whose answers cannot say yes proves
# nothing by saying no.
WITNESS_RECORD=$'witness\x1f\x1fsystem:masters'
# Anyone the API server cannot name: every cluster's reviews hold it, so a grant to the group
# system:unauthenticated is a finding whether or not a binding names system:anonymous.
ANONYMOUS_RECORD=$'user:system:anonymous\x1fsystem:anonymous\x1fsystem:unauthenticated'
# FOUND_SAS: each ServiceAccount (<namespace>:<name>) with a finding on the cluster read last, and
# the right of its first finding, for follow_becomes.
declare -A FOUND_SAS=()
# judge_reviews STEP SUFFIX RESOURCE: each review of $WORK/review.rec. The witness's must all be
# allowed; every other allowed review is a finding on RESOURCE (the cluster), its right followed by
# SUFFIX, granted where the API server's reason says. Returns 1 when the witness was refused.
judge_reviews() {
  local principal label verdict error reason witness_refused=false
  FOUND_SAS=()
  while IFS="$US" read -r -u 4 principal label verdict error reason; do
    if [ "$principal" = witness ]; then
      [ "$verdict" = allowed ] || witness_refused=true
      continue
    fi
    case "$verdict" in
      allowed)
        finding "$principal" "$label$2" "$label" "$3" "$label" "$reason"
        if [[ "$principal" =~ ^system:serviceaccount:([^:]+):([^:]+)$ ]] && [ -z "${FOUND_SAS[${BASH_REMATCH[1]}:${BASH_REMATCH[2]}]:-}" ]; then
          FOUND_SAS["${BASH_REMATCH[1]}:${BASH_REMATCH[2]}"]="$label"
        fi
        ;;
      error) unknown "$1" "the review of $principal for $label ended in an evaluation error, so its refusal proves nothing: $error" ;;
    esac
  done 4<"$WORK/review.rec"
  if [ "$witness_refused" = true ]; then
    unknown "$1" "the witness review, the cluster-admin binding's group system:masters, was not allowed, so a refusal from this API server proves nothing"
    return 1
  fi
}
# follow_becomes KUBECONFIG STEP SUFFIX RESOURCE: who can become a ServiceAccount with a finding
# holds what it holds. In the ServiceAccount's namespace a principal becomes it by create pods (a
# pod runs as any ServiceAccount of its namespace), create or get pods/exec (entering a pod that
# runs as it), get, list or watch secrets (a token Secret of it), and, by its name, create
# serviceaccounts/token or impersonate. Each principal of $WORK/principals.rec allowed one of these
# is a finding naming the chain, and each ServiceAccount so found is followed in turn, up to 5
# steps; one found in the fifth and not followed is UNKNOWN. A ServiceAccount that becomes only
# itself is no finding.
follow_becomes() {
  local kube="$1" step="$2" suffix="$3" resource="$4" round sa rights label targets principal verdict error reason
  local -A followed=() targets_of=()
  for ((round = 0; round < 5; round++)); do
    : >"$WORK/become.tsv"
    while IFS= read -r sa; do
      [ -z "${followed[$sa]:-}" ] || continue
      followed[$sa]=1
      printf '%s\t%s\t%s\n' "${sa%%:*}" "${sa#*:}" "${FOUND_SAS[$sa]}" >>"$WORK/become.tsv"
    done < <(printf '%s\n' ${FOUND_SAS[@]+"${!FOUND_SAS[@]}"} | sed '/^$/d' | sort)
    [ -s "$WORK/become.tsv" ] || return 0
    rights="$(jq -R -s -c '
      [split("\n")[] | select(length > 0) | split("\t") | {ns: .[0], name: .[1], why: .[2]}] as $sas
      | def become($list): $list | map("system:serviceaccount:\(.ns):\(.name), which can \(.why)") | join(", or ");
        def right($verb; $res; $sub; $ns; $name; $list):
          {verb: $verb, group: "", resource: $res, sub: $sub, ns: $ns, name: $name,
           key: ([$verb, "", $res, $sub, $ns, $name] | join("|")),
           label: ("\($verb) \($res)\(if $sub != "" then "/" + $sub else "" end)\(if $name != "" then " " + $name else "" end) in \($ns), so it can become " + become($list)),
           targets: [$list[] | "\(.ns):\(.name)"]};
        [(([$sas[].ns] | unique)[] as $n | [$sas[] | select(.ns == $n)] as $in
          | right("create"; "pods"; ""; $n; ""; $in), right("create"; "pods"; "exec"; $n; ""; $in), right("get"; "pods"; "exec"; $n; ""; $in),
            right("get"; "secrets"; ""; $n; ""; $in), right("list"; "secrets"; ""; $n; ""; $in), right("watch"; "secrets"; ""; $n; ""; $in)),
         ($sas[] | right("create"; "serviceaccounts"; "token"; .ns; .name; [.]), right("impersonate"; "serviceaccounts"; ""; .ns; .name; [.]))]' \
      "$WORK/become.tsv")"
    targets_of=()
    while IFS=$'\t' read -r label targets; do targets_of[$label]="$targets"; done < <(jq -r '.[] | [.label, (.targets | join(" "))] | @tsv' <<<"$rights")
    if ! review "$kube" "$(principals_json "$WORK/principals.rec")" "$rights"; then
      unknown "$step" "$REVIEW_ERROR"
      return 0
    fi
    while IFS="$US" read -r -u 4 principal label verdict error reason; do
      [ "$principal" != witness ] || continue
      case "$verdict" in
        allowed)
          [ "${targets_of[$label]:-}" != "${principal#system:serviceaccount:}" ] || continue
          finding "$principal" "$label$suffix" "$label" "$resource" "$label" "$reason"
          if [[ "$principal" =~ ^system:serviceaccount:([^:]+):([^:]+)$ ]] && [ -z "${FOUND_SAS[${BASH_REMATCH[1]}:${BASH_REMATCH[2]}]:-}" ]; then
            FOUND_SAS["${BASH_REMATCH[1]}:${BASH_REMATCH[2]}"]="$label"
          fi
          ;;
        error) unknown "$step" "the review of $principal for $label ended in an evaluation error, so its refusal proves nothing: $error" ;;
      esac
    done 4<"$WORK/review.rec"
  done
  for sa in ${FOUND_SAS[@]+"${!FOUND_SAS[@]}"}; do
    [ -n "${followed[$sa]:-}" ] ||
      unknown "$step" "who can become a ServiceAccount with a finding is followed 5 steps; system:serviceaccount:$sa, found in the fifth, is not followed further"
  done
}
# custom_rights KUBECONFIG STYLE: sets CUSTOM_RIGHTS, the JSON array, as rights_json writes rights,
# of create on each custom resource the API server's discovery lists as namespaced and creatable,
# in each guarded pair's namespace: a custom resource's controller may make a pod from it (an agent
# sandbox does), so create on one there is the same path as create pods. Custom is any API group
# outside Kubernetes' own: not the core group, no dotless group (apps, batch), and no *.k8s.io
# group other than a *.x-k8s.io one. STYLE `guard` labels each right as GUARD_RIGHTS does. Returns
# 1 with CUSTOM_ERROR when the discovery could not be read whole; CUSTOM_RIGHTS holds what it listed.
custom_rights() {
  CUSTOM_ERROR=""
  if ! kubectl --kubeconfig "$1" api-resources --namespaced=true --verbs=create -o name >"$WORK/api-resources" 2>"$WORK/api-resources.err"; then
    CUSTOM_ERROR="the API server's discovery of namespaced resources could not be read whole, so create on a custom resource it leaves out is not reviewed: $(first_line "$WORK/api-resources.err")"
  fi
  CUSTOM_RIGHTS="$(printf '%s\n' "${GUARDED[@]}" | jq -R -s -c --rawfile names "$WORK/api-resources" --arg style "$2" '
    [split("\n")[] | select(length > 0) | split("/") | {ns: .[0], sa: .[1]}] as $pairs
    | [$names | split("\n")[] | select(contains("."))
       | {resource: (split(".")[0]), group: (split(".")[1:] | join("."))}
       | select(.group | contains("."))
       | select(((.group | endswith(".k8s.io")) | not) or (.group | endswith(".x-k8s.io")))] as $custom
    | [([$pairs[].ns] | unique)[] as $n
       | ([$pairs[] | select(.ns == $n) | "\(.ns)/\(.sa)"] | join(", ")) as $guarding
       | $custom[]
       | {verb: "create", group, resource, sub: "", ns: $n, name: "",
          label: ("create \(.resource).\(.group) in \($n)" + (if $style == "guard" then ", guarding \($guarding)" else "" end))}
       | . + {key: ([.verb, .group, .resource, .sub, .ns, .name] | join("|"))}]')"
  [ -z "$CUSTOM_ERROR" ]
}
# bound_subjects KUBECONFIG: the cluster's binding subjects (the header's "Binding subjects"), added
# to $WORK/principals.rec: each User subject of its ClusterRoleBindings and RoleBindings that
# k8s_subject does not allow and that the file does not hold already, with system:authenticated
# (system:anonymous with system:unauthenticated). A person's User subject, every Group subject and
# every ServiceAccount subject are left out. Sets BOUND to how many it added; returns 1 with
# BOUND_ERROR when the API server refused the list or answered no JSON.
bound_subjects() {
  local kind name label
  local -A held=()
  BOUND=0 BOUND_ERROR=""
  if ! kubectl --kubeconfig "$1" get clusterrolebindings,rolebindings --all-namespaces -o json >"$WORK/bindings.json" 2>"$WORK/bindings.err"; then
    BOUND_ERROR="the API server refused the bindings' list (it needs list on clusterrolebindings and rolebindings, container.clusterRoleBindings.list and container.roleBindings.list on GKE): $(first_line "$WORK/bindings.err")"
    return 1
  fi
  if ! jq -r "$JQ_REC"'[.items[]?.subjects[]? | select(.kind == "User") | [.kind, .name]] | unique[] | rec' \
    "$WORK/bindings.json" >"$WORK/bindings.rec" 2>"$WORK/bindings.err"; then
    BOUND_ERROR="the API server's list of bindings is not JSON: $(first_line "$WORK/bindings.err")"
    return 1
  fi
  while IFS="$US" read -r label _; do held[$label]=1; done <"$WORK/principals.rec"
  while IFS="$US" read -r -u 7 kind name; do
    k8s_subject "$kind" "" "$name"
    [ "$SUBJECT_OK" != true ] || continue
    case "$SUBJECT" in system:serviceaccount:*) continue ;; esac
    [ -z "${held[$SUBJECT]:-}" ] || continue
    held[$SUBJECT]=1
    if [ "$name" = system:anonymous ]; then
      printf '%s\x1f%s\x1fsystem:unauthenticated\n' "$SUBJECT" "$name" >>"$WORK/principals.rec"
    else
      printf '%s\x1f%s\x1fsystem:authenticated\n' "$SUBJECT" "$name" >>"$WORK/principals.rec"
    fi
    BOUND=$((BOUND + 1))
  done 7<"$WORK/bindings.rec"
}

# --- live: RBAC on the operator's cluster ---
# Every ServiceAccount outside the system namespaces is a principal, as on every other cluster of
# the pool: a CI runner's ServiceAccount in a namespace no agent uses can still run a pod as the
# operator. The agent namespaces name the groups also reviewed alone, and must hold a ServiceAccount.
step="live: RBAC on $cluster_name"
RBAC_OK=false
if [ "$OPERATOR_FOUND" != true ]; then
  unknown "$step" "the operator's cluster could not be found (live: clusters)"
elif [ "${#AGENT_NAMESPACES[@]}" -eq 0 ]; then
  unknown "$step" "no --agent-namespace: the namespaces whose ServiceAccounts agents run as are not named"
elif ! kube_for "$CLUSTER" "$OPERATOR_LOCATION" "$PROJECT"; then
  unknown "$step" "$KUBE_ERROR"
elif ! kubectl --kubeconfig "$KUBE" get serviceaccounts --all-namespaces -o json >"$WORK/serviceaccounts.json" 2>"$WORK/serviceaccounts.err"; then
  unknown "$step" "the API server refused the ServiceAccounts' list (it needs list on serviceaccounts, container.serviceAccounts.list on GKE): $(first_line "$WORK/serviceaccounts.err")"
else
  mark
  # Every guarded pair's rights and the other clusters' guard rights, each right once: where both
  # list one, the full list's label stays.
  rights="$(jq -c --argjson guard "$GUARD_RIGHTS" '. + $guard | reduce .[] as $r ([]; if any(.[]; .key == $r.key) then . else . + [$r] end)' <<<"$(rights_json db)")"
  custom_rights "$KUBE" plain || unknown "$step" "$CUSTOM_ERROR"
  rights="$(jq -c --argjson custom "$CUSTOM_RIGHTS" '. + $custom | reduce .[] as $r ([]; if any(.[]; .key == $r.key) then . else . + [$r] end)' <<<"$rights")"
  jq -r "$JQ_REC"'.items[]? | [.metadata.namespace, .metadata.name, (.metadata.annotations["iam.gke.io/gcp-service-account"] // "")] | rec' \
    "$WORK/serviceaccounts.json" >"$WORK/serviceaccounts.rec"
  printf '%s\n%s\n' "$WITNESS_RECORD" "$ANONYMOUS_RECORD" >"$WORK/principals.rec"
  declare -A seen_groups=() seen_accounts=() pattern_matched=()
  accounts=0 agents=0
  while IFS="$US" read -r -u 3 ns name bound; do
    ! system_namespace "$ns" || continue
    accounts=$((accounts + 1))
    printf 'system:serviceaccount:%s:%s\x1fsystem:serviceaccount:%s:%s\x1fsystem:serviceaccounts,system:serviceaccounts:%s,system:authenticated\n' \
      "$ns" "$name" "$ns" "$name" "$ns" >>"$WORK/principals.rec"
    is_agent=false
    for pattern in "${AGENT_NAMESPACES[@]}"; do
      # shellcheck disable=SC2053 # the pattern is a glob on purpose: legion-*-sessions
      if [[ "$ns" == $pattern ]]; then
        is_agent=true
        pattern_matched[$pattern]=1
      fi
    done
    if [ "$is_agent" = true ]; then
      agents=$((agents + 1))
      seen_groups[system:serviceaccounts]=1 seen_groups[system:serviceaccounts:$ns]=1 seen_groups[system:authenticated]=1
    fi
    # The Google service account a ServiceAccount is bound to acts as a User of its email; the
    # operator's own is allowed, as k8s_subject says.
    if [ -n "$bound" ]; then
      k8s_subject User "" "$bound"
      [ "$SUBJECT_OK" = true ] || seen_accounts[$bound]=1
    fi
  done 3<"$WORK/serviceaccounts.rec"
  [ "$agents" -gt 0 ] ||
    unknown "$step" "the cluster lists no ServiceAccount in the agent namespaces (${AGENT_NAMESPACES[*]}), so no agent's principal is among those reviewed"
  # Each pattern alone: a mistyped one (legion-*-session) drops its namespaces while the others match.
  for pattern in "${AGENT_NAMESPACES[@]}"; do
    [ -n "${pattern_matched[$pattern]:-}" ] ||
      unknown "$step" "--agent-namespace $pattern matches no namespace that holds a ServiceAccount, so the agents it was to name are not reviewed as agents"
  done
  while IFS= read -r bound; do
    printf 'serviceAccount:%s\x1f%s\x1fsystem:authenticated\n' "$bound" "$bound" >>"$WORK/principals.rec"
  done < <(printf '%s\n' ${seen_accounts[@]+"${!seen_accounts[@]}"} | sed '/^$/d' | sort)
  while IFS= read -r group; do
    printf 'group:%s\x1f\x1f%s\n' "$group" "$group" >>"$WORK/principals.rec"
  done < <(printf '%s\n' ${seen_groups[@]+"${!seen_groups[@]}"} | sed '/^$/d' | sort)
  bound_subjects "$KUBE" || unknown "$step" "$BOUND_ERROR"
  if [ "$((accounts + ${#seen_accounts[@]} + BOUND))" -eq 0 ]; then
    unknown "$step" "the cluster lists no ServiceAccount outside its system namespaces and no binding subject, so there is no principal to review"
  elif ! review "$KUBE" "$(principals_json "$WORK/principals.rec")" "$rights"; then
    unknown "$step" "$REVIEW_ERROR"
  elif judge_reviews "$step" "" "cluster:$CLUSTER"; then
    RBAC_OK=true
    follow_becomes "$KUBE" "$step" "" "cluster:$CLUSTER"
    if unchanged; then
      pass "$step" "$((accounts + ${#seen_accounts[@]} + ${#seen_groups[@]} + BOUND + 1)) principals ($accounts ServiceAccounts outside the system namespaces, $agents of them in the agent namespaces, ${#seen_accounts[@]} Google service accounts, ${#seen_groups[@]} groups, $BOUND binding subjects, and anyone unauthenticated), $(jq length <<<"$rights") rights each: none allowed"
    fi
  fi
fi

# --- The live IAM checks. ---

# scope_of PROJECT: the analysis scope for a resource of PROJECT, --analysis-scope, else the
# project's top ancestor, so a grant made on its folder or its organization is seen. Sets
# SCOPE_OF ("organizations/N", ...), SCOPE_OF_FLAG (the flag analyze passes) and SCOPE_OF_ERROR.
scope_of() {
  SCOPE_OF="$ANALYSIS_SCOPE" SCOPE_OF_ERROR=""
  if [ -z "$SCOPE_OF" ] && [ -n "$1" ]; then
    if ! have gcloud; then
      SCOPE_OF_ERROR="gcloud is not on PATH"
    elif ! gcloud projects get-ancestors "$1" --format=json --quiet >"$WORK/ancestors.json" 2>"$WORK/ancestors.err"; then
      SCOPE_OF_ERROR="gcloud projects get-ancestors $1 was refused (it needs resourcemanager.projects.get), so the analysis scope is unknown: $(first_line "$WORK/ancestors.err")"
    else
      SCOPE_OF="$(jq -r 'last | if .type == "organization" then "organizations/\(.id)" elif .type == "folder" then "folders/\(.id)" elif .type == "project" then "projects/\(.id)" else "" end' \
        "$WORK/ancestors.json" 2>/dev/null || true)"
      [ -n "$SCOPE_OF" ] || SCOPE_OF_ERROR="gcloud projects get-ancestors $1 answered no ancestor"
    fi
  fi
  case "$SCOPE_OF" in
    organizations/*) SCOPE_OF_FLAG="--organization=${SCOPE_OF#organizations/}" ;;
    folders/*) SCOPE_OF_FLAG="--folder=${SCOPE_OF#folders/}" ;;
    *) SCOPE_OF_FLAG="--project=${SCOPE_OF#projects/}" ;;
  esac
}
scope_of "$PROJECT"
SCOPE="$SCOPE_OF" SCOPE_FLAG="$SCOPE_OF_FLAG" SCOPE_ERROR="$SCOPE_OF_ERROR"
# A check's own scope, for a resource outside --project: CHECK_SCOPE, CHECK_SCOPE_FLAG and
# CHECK_SCOPE_ERROR, keyed by the check; a check with none uses the project's.
declare -A CHECK_SCOPE=() CHECK_SCOPE_FLAG=() CHECK_SCOPE_ERROR=()
if [ -n "$POOL_PROJECT" ] && [ "$POOL_PROJECT" != "$PROJECT" ]; then
  scope_of "$POOL_PROJECT"
  CHECK_SCOPE[fleet]="$SCOPE_OF" CHECK_SCOPE_FLAG[fleet]="$SCOPE_OF_FLAG" CHECK_SCOPE_ERROR[fleet]="$SCOPE_OF_ERROR"
fi

# analyze RESOURCE PERMISSIONS_CSV SCOPE_FLAG SCOPE: Policy Analyzer's holders of any of the
# permissions on the resource, with service account impersonation: a principal that can mint a
# holder's token, sign as it, or act as it holds what the holder holds. Written as records to
# $WORK/analysis.rec: a `row` per binding and identity (identity, role, the resource the binding is
# made on, the permissions it grants, comma-separated, and `expanded` for a group whose members the
# analysis lists), and a `chain` per impersonator of a service account holder, through any number
# of service accounts (the outermost impersonator, its role and where that is granted, the holder's
# permissions, `expanded`, `designed` for a link the design makes, the holder's role and where it
# is granted, the holder, and the chain written out). Returns 1 with ANALYZE_ERROR, also for an
# impersonation analysis not fully explored or naming no service account: an impersonator it did
# not list would read as none.
#
# The designed links are a Workload Identity member acting as the account it is bound to (the
# operator's member as --gsa, the broker's as --broker-account) and the broker minting --gsa's
# token: its outermost principal is the account it acts as, already judged, so it is no finding.
analyze() {
  ANALYZE_ERROR=""
  if ! gcloud asset analyze-iam-policy "$3" --full-resource-name="$1" --permissions="$2" \
    --expand-groups --output-group-edges --expand-roles --analyze-service-account-impersonation \
    --show-response --format=json --quiet >"$WORK/analysis.json" 2>"$WORK/analysis.err"; then
    ANALYZE_ERROR="Policy Analyzer refused the analysis (it needs cloudasset.assets.analyzeIamPolicy on $4): $(first_line "$WORK/analysis.err")"
    return 1
  fi
  if ! jq -r --arg wi "$WI_MEMBER" --arg gsa "$GSA" --arg bwi "$BROKER_WI_MEMBER" --arg broker "$BROKER" "$JQ_REC"'
      def sa_email: if test("^serviceAccount:[^@\\[]+@") then sub("^serviceAccount:"; "") else empty end;
      if (.mainAnalysis | type) != "object" then ["error", "the answer holds no mainAnalysis"] | rec
      elif .mainAnalysis.fullyExplored != true then
        ["error", "the analysis was not fully explored, so a holder it did not reach is not listed: \([.mainAnalysis.nonCriticalErrors[]?.cause // empty] | join("; "))"] | rec
      elif any((.serviceAccountImpersonationAnalysis // [])[]; .fullyExplored != true) then
        ["error", "an impersonation analysis was not fully explored, so an impersonator it did not reach is not listed"] | rec
      else
        [(.serviceAccountImpersonationAnalysis // [])[]
          | (((.analysisQuery.resourceSelector.fullResourceName // "") | capture("/serviceAccounts/(?<e>[^/]+)$").e) // "") as $target
          | if $target == "" then {bad: true}
            else .analysisResults[]? as $r
              | ([$r.identityList.groupEdges[]?.sourceNode]) as $expanded
              | $r.identityList.identities[]?.name as $id
              | {target: $target, id: $id, role: ($r.iamBinding.role // ""), attached: ($r.attachedResourceFullName // ""),
                 expanded: any($expanded[]; . == $id)}
            end] as $imp
        | if any($imp[]; .bad == true) then
            ["error", "an impersonation analysis names no service account, so who impersonates which holder cannot be read"] | rec
          else
            def designed($l): ($l.id == $wi and $l.target == $gsa)
              or ($bwi != "" and $l.id == $bwi and $l.target == $broker)
              or ($broker != "" and $l.id == "serviceAccount:\($broker)" and $l.target == $gsa);
            # chains(E; SEEN): each chain of impersonators of the service account E, innermost first,
            # never through a principal twice.
            def chains($email; $seen):
              $imp[] | select(.target == $email) | . as $l | select(any($seen[]; . == $l.id) | not)
              | [$l], (($l.id | sa_email) as $next | [$l] + chains($next; $seen + [$l.id]));
            .mainAnalysis.analysisResults[]? as $r
            | ([$r.identityList.groupEdges[]?.sourceNode]) as $expanded
            | ([$r.accessControlLists[]?.accesses[]?.permission // empty] | unique | join(",")) as $perms
            | $r.identityList.identities[]?.name as $id
            | (["row", $id, ($r.iamBinding.role // ""), ($r.attachedResourceFullName // ""), $perms,
                (if any($expanded[]; . == $id) then "expanded" else "" end)] | rec),
              (($id | sa_email) as $email | chains($email; [$id]) as $c | ($c | last) as $o
               | ["chain", $o.id, $o.role, $o.attached, $perms, (if $o.expanded then "expanded" else "" end),
                  (if designed($o) then "designed" else "" end), ($r.iamBinding.role // ""), ($r.attachedResourceFullName // ""), $id,
                  ($o.id + ([$c | reverse[] | " -> impersonates serviceAccount:\(.target) (\(.role))"] | join(""))
                   + " -> " + ($r.iamBinding.role // ""))] | rec)
          end
      end' "$WORK/analysis.json" >"$WORK/analysis.rec" 2>"$WORK/analysis.err"; then
    ANALYZE_ERROR="the analysis is not the JSON Policy Analyzer answers: $(first_line "$WORK/analysis.err")"
    return 1
  fi
  if [ "$(cut -d"$US" -f1 "$WORK/analysis.rec" | head -n 1)" = error ]; then
    ANALYZE_ERROR="$(cut -d"$US" -f2- "$WORK/analysis.rec" | head -n 1)"
    return 1
  fi
}

# live_iam CHECK [PASS_NOTE]: the check's analysis, each holder judged.
live_iam() {
  local check="$1" step="${CHECK_STEP[$1]}" resource="${CHECK_RESOURCE[$1]}" witness_member="${CHECK_WITNESS[$1]:-}"
  local holders what identity role attached perms how designed hattached text chains=0
  if [ -n "${CHECK_MISSING[$check]:-}" ]; then
    unknown "$step" "${CHECK_MISSING[$check]}"
    return
  fi
  local scope="${CHECK_SCOPE[$check]-$SCOPE}" scope_flag="${CHECK_SCOPE_FLAG[$check]-$SCOPE_FLAG}" scope_error="${CHECK_SCOPE_ERROR[$check]-$SCOPE_ERROR}"
  if [ -n "$scope_error" ]; then
    unknown "$step" "$scope_error"
    return
  fi
  if ! have gcloud; then
    unknown "$step" "gcloud is not on PATH"
    return
  fi
  if [ "$check" = signing ] && [ -z "$PROJECT_NUMBER" ]; then
    unknown "$step" "the project number, which the key's resource name holds, is unknown: $PROJECT_NUMBER_ERROR"
    return
  fi
  if ! analyze "$resource" "${CHECK_PERMS[$check]// /,}" "$scope_flag" "$scope"; then
    unknown "$step" "$ANALYZE_ERROR"
    return
  fi
  if [ ! -s "$WORK/analysis.rec" ]; then
    unknown "$step" "the analysis lists no holder of ${CHECK_PERMS[$check]// /, } on $resource, though its project's owners hold them, so it saw nothing"
    return
  fi
  mark
  if [ "$check" = dbsecret ] && [ "$RBAC_OK" != true ]; then
    unknown "$step" "the RBAC step's principals' reviews for get on it could not be made (live: RBAC)"
  fi
  # Never `cut | grep -q` under pipefail: grep stops at its first match, and a long answer's next
  # write then fails the pipeline with SIGPIPE, reading a witness that is there as one that is not.
  if [ -n "$witness_member" ] && ! grep -qxF -- "$witness_member" < <(awk -F"$US" '$1 == "row" { print $2 }' "$WORK/analysis.rec"); then
    unknown "$step" "the analysis does not list $witness_member, which holds a permission on it by design, so it cannot be read as complete"
  fi
  holders="$(awk -F"$US" '$1 == "row" { print $2 }' "$WORK/analysis.rec" | sort -u | wc -l | tr -d ' ')"
  # A chain record's holder role and holder are in its written-out chain, the sentence's part.
  while IFS="$US" read -r -u 3 what identity role attached perms how designed _ hattached _ text; do
    if [ "$what" = chain ]; then
      # The outermost impersonator, judged as a holder of what the holder holds; a designed link's
      # principal is the account it acts as, already judged.
      chains=$((chains + 1))
      [ "$designed" != designed ] || continue
      iam_verdict "$check" "$identity" "$role" "$attached" "$how"
      [ "$VERDICT" != finding ] ||
        finding "$identity" "$(held_text "$check" "$perms") on $resource: $text, granted at $hattached$NOTE" \
          "$role" "$resource" "$(held_list "$check" "$perms")" "$attached"
      continue
    fi
    iam_verdict "$check" "$identity" "$role" "$attached" "$how"
    [ "$VERDICT" != finding ] ||
      finding "$identity" "$(held_text "$check" "$perms") on $resource through $role, granted at $attached$NOTE" \
        "$role" "$resource" "$(held_list "$check" "$perms")" "$attached"
  done 3<"$WORK/analysis.rec"
  if unchanged; then
    pass "$step" "$holders holders listed, each a person, a group judged by its members, Google's service agent or the one allowed by name, and $chains impersonation chains, each from such a principal or by design${2:+; $2}"
  fi
}

live_iam gke
live_iam gsa
[ "${CHECK_MISSING[broker]:-}" = skip ] || live_iam broker

# --- live: Workload Identity: every other cluster that uses the pool ---

pool_name="the workload pool's project"
[ -z "$POOL_PROJECT" ] || pool_name="project $POOL_PROJECT"

# read_other_cluster STEP PRINCIPAL WHERE: on the cluster KUBE names, no guarded pair's namespace may
# exist (else a finding for PRINCIPAL, the cluster), and no ServiceAccount outside the cluster's
# system namespaces, and no binding subject, may hold a right of GUARD_RIGHTS (each a finding, its
# right followed by WHERE). The reviews carry the witness. PASS when it recorded neither a finding
# nor an UNKNOWN.
read_other_cluster() {
  local cstep="$1" principal="$2" where="$3" ns name pair acts accounts=0
  local -A exists=()
  mark
  for pair in "${GUARDED[@]}"; do
    ns="${pair%%/*}"
    if [ -z "${exists[$ns]:-}" ]; then
      exists[$ns]=no
      if kubectl --kubeconfig "$KUBE" get namespace "$ns" -o name >/dev/null 2>"$WORK/namespace.err"; then
        exists[$ns]=yes
      elif ! grep -q '(NotFound)' "$WORK/namespace.err"; then
        exists[$ns]=unknown
        unknown "$cstep" "the API server refused the read of namespace $ns (it needs get on namespaces, container.namespaces.get on GKE): $(first_line "$WORK/namespace.err")"
      fi
    fi
    [ "${exists[$ns]}" = yes ] || continue
    acts="the Google service account the pool binds it to"
    [ "$pair" != "$NAMESPACE/$KSA" ] || acts="$operator_account"
    [ -z "$BROKER" ] || [ "$pair" != "$BROKER_WORKLOAD" ] || acts="the broker account $BROKER"
    finding "$principal" "namespace $ns exists there, so its ServiceAccount $pair acts as $acts through the workload pool $POOL_PROJECT.svc.id.goog" \
      "namespace $ns, guarding $pair" "$principal" "" ""
  done
  if ! kubectl --kubeconfig "$KUBE" get serviceaccounts --all-namespaces -o json >"$WORK/serviceaccounts.json" 2>"$WORK/serviceaccounts.err"; then
    unknown "$cstep" "the API server refused the ServiceAccounts' list (it needs list on serviceaccounts, container.serviceAccounts.list on GKE): $(first_line "$WORK/serviceaccounts.err")"
    return
  fi
  jq -r "$JQ_REC"'.items[]? | [.metadata.namespace, .metadata.name] | rec' "$WORK/serviceaccounts.json" >"$WORK/serviceaccounts.rec"
  printf '%s\n%s\n' "$WITNESS_RECORD" "$ANONYMOUS_RECORD" >"$WORK/principals.rec"
  while IFS="$US" read -r -u 3 ns name; do
    ! system_namespace "$ns" || continue
    accounts=$((accounts + 1))
    printf 'system:serviceaccount:%s:%s\x1fsystem:serviceaccount:%s:%s\x1fsystem:serviceaccounts,system:serviceaccounts:%s,system:authenticated\n' \
      "$ns" "$name" "$ns" "$name" "$ns" >>"$WORK/principals.rec"
  done 3<"$WORK/serviceaccounts.rec"
  bound_subjects "$KUBE" || unknown "$cstep" "$BOUND_ERROR"
  custom_rights "$KUBE" guard || unknown "$cstep" "$CUSTOM_ERROR"
  guard_rights="$(jq -c --argjson custom "$CUSTOM_RIGHTS" '. + $custom | reduce .[] as $r ([]; if any(.[]; .key == $r.key) then . else . + [$r] end)' <<<"$GUARD_RIGHTS")"
  if [ "$((accounts + BOUND))" -eq 0 ]; then
    unknown "$cstep" "the cluster lists no ServiceAccount outside its system namespaces and no binding subject to review, so there is no principal to review"
  elif ! review "$KUBE" "$(principals_json "$WORK/principals.rec")" "$guard_rights"; then
    unknown "$cstep" "$REVIEW_ERROR"
  elif judge_reviews "$cstep" "$where" "$principal"; then
    follow_becomes "$KUBE" "$cstep" "$where" "$principal"
    ! unchanged ||
      pass "$cstep" "no guarded namespace ($GUARDED_TEXT), and none of its $accounts ServiceAccounts outside the system namespaces, $BOUND binding subjects and anyone unauthenticated may make a namespace, or a ServiceAccount, a pod or a custom resource in a guarded namespace"
  fi
}

# The project's other clusters. A cluster is the operator's by name and location: one of the same
# name elsewhere is another cluster of the pool, and while which one is the operator's is unknown,
# none of that name is read.
step="live: Workload Identity"
if [ "$CLUSTERS_LISTED" != true ]; then
  unknown "$step" "the project's clusters could not be listed (live: clusters)"
else
  [ "$CLUSTERS_COMPLETE" = true ] ||
    unknown "$step" "the project's cluster list is incomplete (live: clusters), so a cluster it leaves out is not read"
  if [ "$CLUSTERS_COMPLETE" = true ] && [ "$OPERATOR_FOUND" = true ] && [ "$(wc -l <"$WORK/clusters.tsv")" -eq 1 ]; then
    pass "$step" "the project runs the operator's cluster $CLUSTER alone"
  fi
  while IFS=$'\t' read -r -u 5 cluster location; do
    label="$PROJECT/$location/$cluster"
    if [ "$cluster" = "$CLUSTER" ]; then
      if [ "$OPERATOR_FOUND" != true ]; then
        unknown "live: Workload Identity on $label" "it has --cluster's name, and which cluster of that name is the operator's is unknown (live: clusters), so it is not read"
        continue
      fi
      [ "$location" != "$OPERATOR_LOCATION" ] || continue
    fi
    cstep="live: Workload Identity on $label"
    if ! kube_for "$cluster" "$location" "$PROJECT"; then
      unknown "$cstep" "$KUBE_ERROR"
      continue
    fi
    read_other_cluster "$cstep" "cluster:$label" " on cluster $label"
  done 5<"$WORK/clusters.tsv"
fi

# fleet_memberships: every membership of the pool project's fleet, from the GKE Hub API, every
# page, as records in $WORK/fleet.rec: `unreachable` and the locations the listing could not reach,
# when there are any, then one `member` per membership, with its id and location and, for a GKE
# cluster, the cluster's project, location and name. Returns 1 with FLEET_ERROR when the listing
# was refused or its answer cannot be read. The call is the API's own, not `gcloud container fleet
# memberships list`: that command's list drops the answer's `unreachable` field, so a listing
# that missed a location would read as complete.
fleet_memberships() {
  local token page="" pages=0 status why
  local -a query
  FLEET_ERROR=""
  if ! have curl; then
    FLEET_ERROR="curl is not on PATH"
    return 1
  fi
  if ! token="$(gcloud auth print-access-token --quiet 2>"$WORK/token.err")" || [ -z "$token" ]; then
    FLEET_ERROR="gcloud auth print-access-token gave no access token for the running identity: $(last_line "$WORK/token.err")"
    return 1
  fi
  : >"$WORK/fleet-pages.json"
  while :; do
    pages=$((pages + 1))
    if [ "$pages" -gt 1000 ]; then
      FLEET_ERROR="the listing did not end within 1000 pages"
      return 1
    fi
    query=(--data-urlencode pageSize=100)
    [ -z "$page" ] || query+=(--data-urlencode "pageToken=$page")
    status=0
    # The token goes in through curl's configuration on stdin, never on a command line.
    printf 'header = "Authorization: Bearer %s"\n' "$token" |
      curl -sS --fail-with-body --max-time 60 -G -K - "${query[@]}" \
        "https://gkehub.googleapis.com/v1/projects/$POOL_PROJECT/locations/-/memberships" >"$WORK/fleet-page.json" 2>"$WORK/fleet.err" || status=$?
    if [ "$status" -ne 0 ]; then
      why="$(jq -r '[.error.code, .error.status, .error.message] | map(select(. != null) | tostring) | join(" ")' "$WORK/fleet-page.json" 2>/dev/null |
        sed -n '1{s/[[:cntrl:]]/ /g;p;}')" || true
      [ -n "$why" ] || why="$(last_line "$WORK/fleet.err")"
      FLEET_ERROR="the GKE Hub API refused the listing of projects/$POOL_PROJECT/locations/-/memberships (it needs gkehub.memberships.list there): $why"
      return 1
    fi
    if ! jq -c . "$WORK/fleet-page.json" >>"$WORK/fleet-pages.json" 2>"$WORK/fleet.err"; then
      FLEET_ERROR="the GKE Hub API's answer is not JSON: $(first_line "$WORK/fleet.err")"
      return 1
    fi
    page="$(jq -r '.nextPageToken // ""' "$WORK/fleet-page.json")"
    [ -n "$page" ] || break
  done
  if ! jq -r -s "$JQ_REC"'
      ([.[].unreachable[]?] | unique) as $unreachable
      | (if ($unreachable | length) > 0 then ["unreachable", ($unreachable | join(", "))] | rec else empty end),
        (.[].resources[]? as $m
         | (($m.name // "") | capture("/locations/(?<l>[^/]+)/memberships/(?<id>[^/]+)$") // {l: "", id: ($m.name // "")}) as $n
         | (($m.endpoint.gkeCluster.resourceLink // "")
            | capture("^//container\\.googleapis\\.com/projects/(?<p>[^/]+)/(locations|zones)/(?<l>[^/]+)/clusters/(?<c>[^/]+)$")
            // {p: "", l: "", c: ""}) as $g
         | ["member", $n.id, $n.l, $g.p, $g.l, $g.c] | rec)' "$WORK/fleet-pages.json" >"$WORK/fleet.rec" 2>"$WORK/fleet.err"; then
    FLEET_ERROR="the GKE Hub API's answer holds no list of memberships: $(first_line "$WORK/fleet.err")"
    return 1
  fi
}

# The pool project's fleet: every member cluster shares the workload pool, so a ServiceAccount
# envoy-operator/envoy-operator there is the operator's member too. The operator's own cluster,
# and a cluster the project's list held, are read above.
step="live: Workload Identity: the fleet of $pool_name"
if [ -z "$POOL_PROJECT" ]; then
  unknown "$step" "no --project or --pool-project: the workload pool's project is not named"
elif ! have gcloud; then
  unknown "$step" "gcloud is not on PATH"
elif ! fleet_memberships; then
  unknown "$step" "$FLEET_ERROR"
else
  fleet_f="${#FINDINGS[@]}" fleet_u="${#UNKNOWNS[@]}" members=0 own=0 above=0 here=0
  # The projects of the GKE members read here other than --project, whose IAM the step after this
  # one reads.
  declare -A OTHER_PROJECTS=()
  while IFS="$US" read -r -u 6 what id location gke_project gke_location gke_cluster; do
    if [ "$what" = unreachable ]; then
      unknown "$step" "the listing could not reach the locations $id, so a membership registered there is not listed"
      continue
    fi
    members=$((members + 1))
    if [ -n "$gke_cluster" ]; then
      # The operator's cluster is --cluster in the operator's location, never by name alone; a member
      # of that name in the project, while the operator's location is unknown, cannot be told from it.
      if [ "$gke_project" = "$PROJECT" ] && [ "$gke_cluster" = "$CLUSTER" ]; then
        if [ -z "$OPERATOR_LOCATION" ]; then
          unknown "live: Workload Identity on fleet member $id (cluster $gke_project/$gke_location/$gke_cluster)" \
            "it has --cluster's name, and the operator's cluster's location is unknown (live: clusters), so it is not read"
          continue
        fi
        if [ "$gke_location" = "$OPERATOR_LOCATION" ]; then
          own=$((own + 1))
          continue
        fi
      fi
      if [ -n "${PROJECT_CLUSTERS["$gke_project/$gke_location/$gke_cluster"]:-}" ]; then
        above=$((above + 1))
        continue
      fi
      here=$((here + 1))
      [ "$gke_project" = "$PROJECT" ] || OTHER_PROJECTS[$gke_project]=1
      label="$gke_project/$gke_location/$gke_cluster"
      cstep="live: Workload Identity on fleet member $id (cluster $label)"
      if ! kube_for "$gke_cluster" "$gke_location" "$gke_project"; then
        unknown "$cstep" "$KUBE_ERROR"
        continue
      fi
      read_other_cluster "$cstep" "cluster:$label" " on cluster $label"
    else
      here=$((here + 1))
      label="$POOL_PROJECT/$location/$id"
      cstep="live: Workload Identity on fleet member $id"
      if ! kube_for_membership "$id" "$location"; then
        unknown "$cstep" "$KUBE_ERROR"
        continue
      fi
      read_other_cluster "$cstep" "membership:$label" " on fleet member $label"
    fi
  done 6<"$WORK/fleet.rec"
  if [ "${#FINDINGS[@]}" -eq "$fleet_f" ] && [ "${#UNKNOWNS[@]}" -eq "$fleet_u" ]; then
    if [ "$members" -eq 0 ]; then
      pass "$step" "the GKE Hub API lists no membership in the fleet, and no location it could not reach"
    else
      pass "$step" "$members membership(s) listed, every location reached: $own the operator's cluster, $above read above as clusters of the project, $here read here"
    fi
  fi
fi

# --- live: GKE IAM on the projects of the fleet's other GKE members. GKE authorizes IAM beside
# RBAC, so a holder of container.namespaces.create, container.serviceAccounts.create or
# container.pods.create there makes a guarded pair's namespace, ServiceAccount or pod on that member
# as its RBAC would; IAM grants them in every namespace. --project's holders are the GKE IAM step's.
# ---
for other in $(printf '%s\n' ${OTHER_PROJECTS[@]+"${!OTHER_PROJECTS[@]}"} | sort); do
  check="gke:$other"
  CHECK_STEP[$check]="live: GKE IAM on project $other, of a fleet member"
  CHECK_RESOURCE[$check]="//cloudresourcemanager.googleapis.com/projects/$other"
  CHECK_PERMS[$check]="container.namespaces.create container.serviceAccounts.create container.pods.create"
  scope_of "$other"
  CHECK_SCOPE[$check]="$SCOPE_OF" CHECK_SCOPE_FLAG[$check]="$SCOPE_OF_FLAG" CHECK_SCOPE_ERROR[$check]="$SCOPE_OF_ERROR"
  live_iam "$check"
done

# --- live: who can set IAM on the projects' ancestors. A holder of a folder's or the organization's
# setIamPolicy can grant itself any role below it, container.admin on the project included, and a
# holder of the organization's iam.roles.update can add a permission to a custom role it holds. ---
ancestor_projects=("$PROJECT")
[ -z "$POOL_PROJECT" ] || [ "$POOL_PROJECT" = "$PROJECT" ] || ancestor_projects+=("$POOL_PROJECT")
declare -A ANCESTORS_SEEN=()
for anc_project in "${ancestor_projects[@]}"; do
  step="live: who can set IAM on the ancestors of project ${anc_project:-?}"
  if [ -z "$anc_project" ]; then
    unknown "$step" "no --project: the project that runs the operator's cluster is not named"
    continue
  elif ! have gcloud; then
    unknown "$step" "gcloud is not on PATH"
    continue
  elif ! gcloud projects get-ancestors "$anc_project" --format=json --quiet >"$WORK/ancestors.json" 2>"$WORK/ancestors.err"; then
    unknown "$step" "gcloud projects get-ancestors $anc_project was refused (it needs resourcemanager.projects.get): $(first_line "$WORK/ancestors.err")"
    continue
  elif ! jq -r '.[] | select(.type == "folder" or .type == "organization") | "\(.type)s/\(.id)"' "$WORK/ancestors.json" \
    >"$WORK/ancestors.list" 2>"$WORK/ancestors.err"; then
    unknown "$step" "gcloud projects get-ancestors $anc_project answered no JSON list: $(first_line "$WORK/ancestors.err")"
    continue
  elif [ ! -s "$WORK/ancestors.list" ]; then
    pass "$step" "the project has no folder or organization above it"
    continue
  fi
  while IFS= read -r -u 8 anc; do
    [ -z "${ANCESTORS_SEEN[$anc]:-}" ] || continue
    ANCESTORS_SEEN[$anc]=1
    check="ancestor:$anc"
    CHECK_STEP[$check]="live: who can set IAM on $anc"
    CHECK_RESOURCE[$check]="//cloudresourcemanager.googleapis.com/$anc"
    case "$anc" in
      folders/*) CHECK_PERMS[$check]="resourcemanager.folders.setIamPolicy" ;;
      *) CHECK_PERMS[$check]="resourcemanager.organizations.setIamPolicy iam.roles.update" ;;
    esac
    scope_of "$anc_project"
    CHECK_SCOPE[$check]="$SCOPE_OF" CHECK_SCOPE_FLAG[$check]="$SCOPE_OF_FLAG" CHECK_SCOPE_ERROR[$check]="$SCOPE_OF_ERROR"
    live_iam "$check"
  done 8<"$WORK/ancestors.list"
done

# --- live: who can register a cluster to the pool project's fleet. A registered cluster shares the
# pool's Workload Identity, so whoever registers one makes the operator's member on it. ---
live_iam fleet

# --- live: hold writers ---
live_iam sql
live_iam dbsecret "the RBAC step's principals' get on it is in that step"
live_iam state
live_iam signing

# --- admin tag ---
step="admin tag of $SECRET"
if ! have agent-secrets; then
  unknown "$step" "agent-secrets is not on PATH"
elif agent-secrets secret show "$SECRET" --json >"$WORK/secret.json" 2>"$WORK/secret.err"; then
  if ! jq -e 'type == "object"' "$WORK/secret.json" >/dev/null 2>&1; then
    unknown "$step" "agent-secrets secret show $SECRET --json printed no JSON object"
  else
    admin="$(jq -r '.admin // ""' "$WORK/secret.json")"
    tier="$(jq -r '.tier // ""' "$WORK/secret.json")"
    [ "$admin" = true ] || finding "secret:$SECRET" "admin is \"$admin\", not \"true\": without the administrator tag a model judge may decide its release" \
      admin "secret:$SECRET" "" ""
    [ "$tier" = human ] || finding "secret:$SECRET" "tier is \"$tier\", not \"human\"" tier "secret:$SECRET" "" ""
    if [ "$admin" = true ] && [ "$tier" = human ]; then
      pass "$step" "admin is \"true\" and tier is \"human\": each release waits on a person's approval"
    fi
  fi
# A missing secret is a finding, in each wording agent-secrets has had for it: gRPC's own
# `rpc error: code = NotFound` (legion-envoy-v30.43.0, read on 2026-11-21), the store interceptor's
# `GetSecret answered NotFound`, and AWS's ResourceNotFoundException from DescribeSecret.
elif grep -Eq 'code = NotFound|GetSecret answered NotFound|DescribeSecret.*ResourceNotFoundException' "$WORK/secret.err"; then
  finding "secret:$SECRET" "no such secret: the operator's credential must be stored at human tier with --admin true" secret "secret:$SECRET" "" ""
else
  unknown "$step" "agent-secrets secret show $SECRET --json was refused: $(last_line "$WORK/secret.err")"
fi

# --- The result. ---
findings="${#FINDINGS[@]}"
unknowns="${#UNKNOWNS[@]}"
write_report
FINISHED=true
if [ "$findings" -gt 0 ]; then
  echo "operator-token-minters: $findings finding(s), $unknowns unknown: exit 1"
  exit 1
fi
if [ "$unknowns" -gt 0 ]; then
  echo "operator-token-minters: no finding, $unknowns unknown: exit 2"
  exit 2
fi
echo "operator-token-minters: no finding and nothing unknown: exit 0"
