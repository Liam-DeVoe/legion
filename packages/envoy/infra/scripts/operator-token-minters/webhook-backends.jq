# operator-token-minters/webhook-backends.jq: admission_power's webhooks (admission-backends.sh), read
# from a cluster's list of mutating and validating webhook configurations, with $guarded the guarded
# namespaces. A webhook whose namespaceSelector can match a guarded namespace is judged: one that
# calls a Service gives, as rights (as rights_json writes them), the writes to that Service, its
# Endpoints and EndpointSlices, its TLS Secret and the workload behind it, in the Service's namespace;
# one that calls a URL is listed in `urls`. A selector is read against kubernetes.io/metadata.name
# alone, which the API server sets to the namespace's name: any other requirement can match, since a
# namespace's other labels can change, and an objectSelector can match a pod made to fit it.
def can_match($ns):
  (.namespaceSelector // {}) as $s
  | (($s.matchLabels // {}) | to_entries | all(.key != "kubernetes.io/metadata.name" or .value == $ns))
    and (($s.matchExpressions // []) | all(
      if .key != "kubernetes.io/metadata.name" then true
      elif .operator == "In" then any(.values[]; . == $ns)
      elif .operator == "NotIn" then all(.values[]; . != $ns)
      elif .operator == "Exists" then true
      else false end));
[.items[]? | (.kind // "") as $kind | .metadata.name as $config | .webhooks[]? | . as $w
  | select(any($guarded[]; . as $ns | $w | can_match($ns)))
  | {id: "\($kind) \($config)/\(.name)", service: .clientConfig.service, url: .clientConfig.url}]
| {urls: [.[] | select(.service == null) | {id, url: (.url // "no URL and no Service")}],
   rights: ([.[] | select(.service != null) | . as $h | $h.service as $svc
     | (["", "services"], ["", "endpoints"], ["discovery.k8s.io", "endpointslices"], ["", "secrets"], ["", "pods"],
        ["apps", "deployments"], ["apps", "replicasets"], ["apps", "statefulsets"], ["apps", "daemonsets"]) as [$g, $res]
     | ("create", "update", "patch") as $verb
     | {verb: $verb, group: $g, resource: $res, sub: "", ns: $svc.namespace, name: "",
        label: "\($verb) \($res)\(if $g != "" then "." + $g else "" end) in \($svc.namespace), backing the webhook \($h.id)"}]
     | unique_by([.verb, .group, .resource, .ns])
     | map(. + {key: ([.verb, .group, .resource, .sub, .ns, .name] | join("|"))}))}
