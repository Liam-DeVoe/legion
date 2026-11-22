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
| `hold prepare --workflow W --reason R --minters-report F` | Checks the minter report against `envoy-operator/envoy-operator-accepted-minters`, moves the row to `prepare` under a new hold id, and starts the hold's journal. It prints each allowance the minter run was given, then the id. |
| `hold set (--hold ID \| --workflow W --reason R) --minters-report F` | Runs §3a's steps, listed below, and moves the row to `held`. It prints each allowance the minter run was given (`--service-agent`, `--rbac-only-member`, `--fleet none`, the report's `allowances`) and records each in its `done` entry as `minters_allowance.<i>`; an allowance refuses nothing, and a report with no `allowances` list, or an allowance it cannot read whole, is UNKNOWN. |
| `hold abort --hold ID --reason R` | The way out of a hold: see "Abort" below. |
| `hold release --hold ID --reason R` | Ends a hold whose stops all left nothing changed, or whose workflow is complete. In hub-restore, each claim must first be on its expected disk, and after its row move it reads the disks again: a disk that changed between its check and its move (a restore outside any hold) is named, and it exits 1 with the hold released; a read that fails is UNKNOWN and claims no change. On a row restored `released` from `prepare`, whose hold has not ended, it is refused, naming `hold abort`. |
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
   stream. While the row is still `prepare`, the probe runs once and must be admitted, so its
   refusal in that hold cannot come from its own credential; a probe the hub refuses here stops
   `hold set`, naming the probe's error, before its intent and its move of the row. The intent
   records the admitted credential's fingerprint (a sha256 of the account the token names and its
   audiences, never the token) and its expiry.
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

`hold set` writes its intent before the row moves, and a step that refuses after it writes a stop.
Its rerun, `hold set --hold ID`, goes through the hold's admission like every command: it takes
the one-command lock and writes a new intent before it restarts anything, then runs the steps
again, and its done entry follows that intent. A buckets or streams command given `--hold` of a
hold whose `hold set` is not done (one `hold prepare` made, or one whose set stopped) runs that set
first, and that set checks `--minters-report` as `hold set` does, printing each allowance and
recording it in its done entry. On a `held` row the rerun runs no probe
outside the hold, so it counts the probe's refusal as the hold's only on this hold's own proof:
the journal holds the admitted-probe record (a), the rerun's credential has its fingerprint (b),
and that credential lapses after now plus the probe's timeout (c). Otherwise the rerun is UNKNOWN,
naming which, and changes nothing. The residual: a credential the auth service stops admitting
between the two runs, its probe entry dropped from the services it lists, still has its fingerprint
and its expiry, so its refusal reads as the hold's.

## Workflows and their commands

A hold has one workflow. Its journal admits only that workflow's commands, in order
(`holdrecord.Admit`):

| Workflow | Commands |
|---|---|
| `cutover` | `create-bucket`, `replay` (repeatable), `convert`, `streams census` |
| `rollback` | `restore --prepare`, `restore --key` (repeatable), `restore --finish` |
| `selection-recovery` | `create-bucket --after-loss`, `replay` (repeatable), `replay --final` |
| `buckets-create` | `buckets create` |
| `buckets-recreate` | `buckets backup`, `buckets recreate`, which deletes only buckets the backup's signed done entry covers, each archive there and hashing to the sha256 it records |
| `buckets-restore` | `buckets backup`, `buckets restore --from` |
| `streams-update` | `streams update` |
| `streams-census` | `streams census` |
| `hub-restore` | `hold prove` (repeatable), `streams census`, `hold abort` |

Every command writes an `intent` entry before its first change, and then `done`, or `stop` with
what it left. Once `hold release` or `hold abort` is `done`, the hold has ended and no command is
admitted in it: "the hold has ended: hold release is done".

A held command's stop records every change its runs in the hold made and did not undo: each stream
it made or changed, each consumer or stream it removed, and this hold's barrier key in each bucket
the census put it in, which nothing deletes. It records `unchanged` only when no run changed
anything outside the journal and every reading was whole. After an UNKNOWN it records `unknown:
read <what the run read since its intent>; unread <what it could not read>; <what the runs
changed>`, never `unchanged`. `hold release` follows only stops that record `unchanged`; over any
other, `hold abort` is the way out. `hold set`'s own stops record no such account: its restart and
its meta barrier change no stored data, and release's rule reads none of set's entries.

`streams census` reads and reconciles every family the broker's cutover rows hold (`session`,
`dispatch` and each `daemon/<P>`), as hub-restore's abort reads the daemon ones, and its convert
check refuses one whose row is not `done`. `--family` is optional; when any is given, the values
must be exactly the rows' families, or the census is UNKNOWN, naming each one missing or extra,
before it reads or removes anything.

## Abort

`hold abort` ends a hold:

- From `prepare`, it deletes the hold's cohort record and moves the row to `released`. It refuses
  when the journal holds `hold set`'s `done` entry, because such a row was put back by a database
  writer. A prepared hold with no journal at all, where `hold prepare` moved the row and its
  journal insert did not land, takes the same path: with no journal there is no `hold set` entry.
- In a set hold, it is admitted once a command has stopped and no intent is pending. It prints
  what each stop left.
- In hub-restore, after a done `hold prove`, it is admitted only after a stop. It then checks each
  claim against the newest done prove's disk, and names a proven disk that is gone (a 404, or
  another id under the name). Before it releases, it reconciles each daemon store of the
  `daemon/<P>` cutover rows (`--daemon-database P=<file>`, repeat). It releases only when every
  store is changed, gone or already consistent; otherwise it stops, naming each store. Each store
  found gone (the server's SQLSTATE 3D000 answer, at the database and instance the daemon's
  Deployment and the operator's file both name) is recorded in the journal: in the stop's reason
  beside the stores left unreconciled, and in the done entry as `gone.daemon/<P>`, beside
  `stream_id.daemon/<P>` for each store it rewrote.
- On a row restored `released` with its hold id while its journal shows `prepare` (no done
  `hold set`, not ended), it is admitted, deletes the cohort record and writes its `done` entry,
  which ends the hold. With no journal at all (a row lost in `prepare` before its journal was
  written), it deletes the cohort record alone and writes no entry, since there is no journal.
- A crashed abort's rerun runs the whole abort again. An abort killed after its row move writes its
  `done` entry when rerun.

## Restore

`hold restore` writes a lost row back at the log's next gen. It sends `released` first, and `held`
only when the broker refuses `released` with 400 `bad_transition`: the broker's signed row log
decides, not the journal.

- The broker refuses `released` after a `held` row of a hold that has not ended, a row lost
  mid-hold. With `--hold ID` the row then comes back `held` with its id, and the hold's way out
  works as before: set's rerun, `hold abort` after a stop, or `hold release` with nothing changed.
  With no `--hold` the refusal names `--hold`, and no row is written.
- After any other entry the row comes back `released`: with the hold's id when `--hold` names a hold
  that has not ended (a row lost in `prepare`, before `hold set`'s move, after an abort from
  `prepare`, or before the hold's journal was written), and the output says whether the hold's
  cohort record is still there, and names the next step, `hold abort`; with no hold id for a hold
  that has ended, or with no `--hold`. An id with neither a journal nor a cohort record is refused
  before anything is written, since the broker would take it after a `released` entry and no hold
  of it is left to end: restore with no `--hold`. After a `prepare` entry the broker stores
  `released` only with that entry's hold id (409 `hold_mismatch` otherwise), and the refusal says
  to name it with `--hold`.
- `--bootstrap` writes the first row, and only into an empty log.
- A restore the broker refuses 409 `clock_skew` (this machine's clock and the broker's differ by
  more than the allowance, or the row would be signed before the latest row the broker accepted)
  or 409 `below_floor` (a log or journal it judges by does not verify) stores nothing, and the
  command is UNKNOWN, naming the code. Neither is a reason to send `held`.

## Creates after a restart

For a while after a hold's restart, JetStream's meta leader can still hold a restarted peer
offline, and it refuses to place a three-copy stream then (error code 10005, "no suitable peers
for placement"). The operator holds no `$SYS` user, so nothing it can read says every peer is back:
the create's own refusal is the only signal. `buckets create`, `recreate` and `restore` make each
create again while JetStream answers that code (`RetryPlacement`), every 250 ms and up to the
restart's limit, printing each retry; `streams update` sends its whole run again when a create or
raise of its report failed with that code (`streams.Finding.Err`), and the run's changes are those
of every attempt. Any other error returns at once. JetStream answers the same code for a stream no
retry can place, such as more copies than the cluster has members, so past the limit the command
is UNKNOWN naming the create, the code and the last refusal's text: a peer still held offline
reads "no suitable peers for placement, peer offline", and five copies asked of three members
reads "no suitable peers for placement" alone.

## Stated residuals

- The command bounds no restore gen of its own. `holdrecord.RestoreRow` and `Bootstrap` restore at
  one above the highest row gen their client has verified, and a new `hold restore` process has
  verified none. After a row loss the broker's per-replica floor of the row and its signature check
  are the guards.
- A prepared hold with no journal takes abort's `prepare` path. After a broker restart, whose floor
  of the records starts empty, a database admin who deletes a set hold's journal and puts back its
  signed `prepare` row makes the abort release it. This is the plan's stated residual for the
  database admin (§3a, "A stated residual").

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
