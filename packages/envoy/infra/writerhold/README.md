# writerhold: the NATS writer hold

`writerhold` is `envoy-infra hold` and the held commands of `envoy-infra buckets` and
`envoy-infra streams update|census`. It works on the clustered hub only, the StatefulSet
`envoy-nats` in namespace `envoy`.

While a hold is set, three things are true:

- the auth service admits no client but the operator (`natscallout`'s `held` refusal);
- every server of the hub has been restarted since that refusal began, so no connection made before
  it is still open;
- the operator's own grant (`natsgrant.Operator`) holds exactly what the hold's command needs.

The hold row, its log and each hold's journal live in the broker's Postgres. The operator reads
and writes them only through the broker's signed routes, `/v1/nats-hold/*` (package
`internal/holdrecord`). Every row and record is signed with the operator's hold key, and the
broker and the auth service each keep a floor that refuses an older signed record put back.

## Reading the hold: `envoy-infra hold status`

`status` changes nothing. It needs three things:

- `--broker-url`: the secrets broker's base URL;
- `--token-file`: a file holding the operator's Kubernetes ServiceAccount token for
  `envoy-operator/envoy-operator`, minted for the broker's NATS audience
  (`BROKER_NATS_K8S_AUDIENCE`), for example with `kubectl create token envoy-operator -n
  envoy-operator --audience <audience>`;
- `--verify-keys`: the hold's public signing keys, the broker's `BROKER_NATS_HOLD_SIGNING_KEYS`
  value.

```sh
kubectl -n envoy-operator create token envoy-operator --audience "$BROKER_NATS_K8S_AUDIENCE" > /tmp/operator-token
envoy-infra hold status --broker-url "$BROKER_URL" --token-file /tmp/operator-token \
  --verify-keys "$BROKER_NATS_HOLD_SIGNING_KEYS" [--hold ID]
```

It prints the verified row, then the journal of the hold the row names, or of the hold `--hold`
names:

```text
row: gen 7, phase held, hold "J7Ge6lY74jslyOppV9afTV", signed at 2026-11-21T16:20:01.123456Z
hold J7Ge6lY74jslyOppV9afTV: workflow buckets-create, record gen 3, complete false, hold_gen 7
  1. hold set intent
  2. hold set done
```

The row's phase is `released` (no hold), `prepare` (a hold being prepared, with its cohort
record) or `held`.

## The commands

| Command | What it does |
|---|---|
| `hold prepare --workflow W --reason R --minters-report F` | Checks the minter report against `envoy-operator/envoy-operator-accepted-minters`, moves the row to `prepare` under a new hold id, and starts the hold's journal. It prints the id. |
| `hold set (--hold ID \| --workflow W --reason R) --minters-report F` | Runs §3a's steps, listed below, and moves the row to `held`. |
| `hold abort --hold ID --reason R` | The way out of a hold: see "Abort" below. |
| `hold release --hold ID --reason R` | Ends a hold whose stops all left nothing changed, or whose workflow is complete. In hub-restore, each claim must first be on its expected disk. |
| `hold restore [--bootstrap \| --hold ID] --reason R` | Writes a lost row back. See "Restore" below. |
| `hold prove --hold ID --restored T=D...` | hub-restore's proof of the disks a restore landed on: a restart of the hub by eviction, with step 4's proof of each restored server. |
| `hold keygen --signing-key-secret projects/P/secrets/operator-only-envoy-hold-signing` | Makes the operator's hold signing key and prints its public half. |

Every command but `keygen` takes `--broker-url`, `--token-file` and `--verify-keys`. Every command
that writes the hold takes `--signing-key-secret`. `set` and `prove` also need:

- `--nats URL` (repeat), the hub's client URLs;
- `--probe-token-file`;
- the hub's Kubernetes objects, through `--kube-namespace` (`envoy`), `--statefulset` (`envoy-nats`)
  and `--configmap` (`envoy-nats-config`), read with the in-cluster or KUBECONFIG configuration.

### `hold set`'s steps

1. The preflight: the render from ConfigMap `envoy-nats-config` and StatefulSet `envoy-nats`, the
   minter report, each server's `/varz`, `/routez`, `/leafz` and `/gatewayz`, and each protected
   stream.
2. The row moves to `held`; the probe is refused.
3. The writer census: complete listings, literal destinations, push consumers, sources and message
   schedules.
4. The restart of every declared server, one eviction at a time under the disruption budget. Each
   restarted server is proved by its new server id, the pinned image's digest and tag version, its
   revision, and the claim on its recorded disk. The next eviction waits until each present
   protected stream's group has a leader with every copy current.
5. The meta barrier through `HoldBarrierStream`.
6. The report: every connection left is the caller's or the auth service's, read through `/connz`
   paged past 1,024.

## Workflows and their commands

A hold has one workflow. Its journal admits only that workflow's commands, in order
(`holdrecord.Admit`):

| Workflow | Commands |
|---|---|
| `cutover` | `create-bucket`, `replay` (repeatable), `convert`, `streams census` |
| `rollback` | `restore --prepare`, `restore --key` (repeatable), `restore --finish` |
| `selection-recovery` | `create-bucket --after-loss`, `replay` (repeatable), `replay --final` |
| `buckets-create` | `buckets create` |
| `buckets-recreate` | `buckets backup`, `buckets recreate`, which deletes only buckets the backup's manifest covers |
| `buckets-restore` | `buckets backup`, `buckets restore --from` |
| `streams-update` | `streams update` |
| `streams-census` | `streams census` |
| `hub-restore` | `hold prove` (repeatable), `streams census`, `hold abort` |

Every command writes an `intent` entry before its first change, and then `done`, or `stop` with
what it left. Once `hold release` or `hold abort` is `done`, the hold has ended and no command is
admitted in it: "the hold has ended: hold release is done".

## Abort

`hold abort` ends a hold:

- From `prepare`, it deletes the hold's cohort record and moves the row to `released`. It refuses
  when the journal holds `hold set`'s `done` entry, because such a row was put back by a database
  writer.
- In a set hold, it is admitted once a command has stopped and no intent is pending. It prints
  what each stop left.
- In hub-restore, after a done `hold prove`, it is admitted only after a stop. It then checks each
  claim against the newest done prove's disk, and names a proven disk that is gone (a 404, or
  another id under the name). Before it releases, it reconciles each daemon store of the
  `daemon/<P>` cutover rows (`--daemon-database P=<file>`, repeat). It releases only when every
  store is changed, gone or already consistent; otherwise it stops, naming each store.
- A crashed abort's rerun runs the whole abort again. An abort killed after its row move writes its
  `done` entry when rerun.

## Restore

`hold restore` writes a lost row back at the log's next gen:

- `--hold ID` of a set hold that has not ended: the row comes back `held` with its id.
- `--hold ID` of a hold lost in `prepare` or that has ended, or no `--hold`: the row comes back
  `released`. After `prepare`, the output names the cohort record that stays, and the next step,
  `hold abort` or a new `hold prepare`.
- The broker refuses a `released` row mid-hold (400 `bad_transition`); the refusal names `--hold`.
- `--bootstrap` writes the first row, and only into an empty log.

## Refusals and exit codes

- 0: done.
- 1: refused, naming what. Examples: "no command has stopped"; a claim not on its expected disk;
  "the hold has ended".
- 2: a usage error, or UNKNOWN, a reading that is not whole, which is never a pass. Examples: a
  record the broker answers below its floor; a daemon store left unreconciled; a monitoring page
  missing.

The operator's grant per phase is `natsgrant.Operator(phase)`; plan E's t-e-55 names the table's
source (`t-e-55-plan-v130.md`). Only the clustered hub is built: no VM hub path, no
`compute.instances.*` read, and no IAP forwarding.
