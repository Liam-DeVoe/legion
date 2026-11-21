# shellcheck shell=bash
# shellcheck disable=SC2034,SC2153,SC2154 # globals set and read across the sourced files, in operator-token-minters.sh's order
# operator-token-minters/step-code-rbac.sh: the step "code: RBAC". Sourced by
# operator-token-minters.sh, whose header says what each step checks.
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
    # Every right the live steps ask, outright or limited by name: the list, the guard rights with
    # the certificate chain's, and the admission rights. A part of the chain goes to csr_part, so
    # a principal is a finding for the whole chain alone, as on a live cluster. Custom resources are
    # each cluster's own, read live from its discovery.
    code_rights="$(with_rights "$(with_rights "$(rights_json db)" "$GUARD_RIGHTS")" "$ADMISSION_CODE_RIGHTS")"
    csr_reset
    if ! jq -r -n --argjson rights "$code_rights" --argjson named_rights "$code_rights" \
      --argjson live false -L "$LIB" -f "$LIB/code-rbac.jq" "${RBAC_RENDERS[@]}" >"$WORK/render.rec" 2>"$WORK/render.err"; then
      unknown "$step" "a render is not JSON: $(first_line "$WORK/render.err")"
    else
      declared=0
      while IFS="$US" read -r -u 3 what id detail kind ns name; do
        case "$what" in
          empty) unknown "$step" "the renders declare no RoleBinding or ClusterRoleBinding (they hold $id objects), so they show nothing" ;;
          grant)
            declared=$((declared + 1))
            k8s_subject "$kind" "$ns" "$name"
            [ "$SUBJECT_OK" != true ] || continue
            csr_part "$SUBJECT" "$detail" "$id" || finding "$SUBJECT" "$detail, declared by $id" "$detail" "cluster:$cluster_name" "$detail" "$id"
            ;;
          role)
            k8s_subject "$kind" "$ns" "$name"
            [ "$SUBJECT_OK" = true ] ||
              unknown "$step" "$id binds $SUBJECT to $detail, which the render does not hold (or aggregates), so the rights it gives are unknown"
            ;;
        esac
      done 3<"$WORK/render.rec"
      csr_chains ", declared by the renders" "cluster:$cluster_name"
      if unchanged; then
        pass "$step" "the renders give the rights, the guard rights, the certificate chain and the admission rights, outright or by name, to no one but people, system namespaces and components, and the operator's own account ($declared grants read; custom resources are read live)"
      fi
    fi
  fi
fi
